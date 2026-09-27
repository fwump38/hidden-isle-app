package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"gorm.io/gorm"

	"github.com/fwump38/hidden-isle-app/internal/db"
)

// OIDC is "Sign in with Authentik" for the browser: the authorization-code flow with PKCE, using
// the same OAuth2 provider as the MCP connector (add <public URL>/auth/callback as a redirect URI).
type OIDC struct {
	issuer, clientID, secret, redirect string
	verifier                           *JWTVerifier
	sessions                           *Sessions
	db                                 *gorm.DB

	mu  sync.Mutex
	cfg *oauth2.Config
}

const oidcCookie = "hi_oidc"

func NewOIDCLogin(g *gorm.DB, s *Sessions, issuer, clientID, secret, publicURL string) *OIDC {
	return &OIDC{issuer: issuer, clientID: clientID, secret: secret, redirect: publicURL + "/auth/callback",
		verifier: NewOIDC(issuer, clientID), sessions: s, db: g}
}

type providerDoc struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

func discover(ctx context.Context, issuer string) (*providerDoc, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openid-configuration: %s", resp.Status)
	}
	var d providerDoc
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.JWKSURI == "" {
		return nil, errors.New("openid-configuration is missing endpoints")
	}
	return &d, nil
}

func (o *OIDC) config(ctx context.Context) (*oauth2.Config, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cfg != nil {
		return o.cfg, nil
	}
	d, err := discover(ctx, o.issuer)
	if err != nil {
		return nil, err
	}
	o.cfg = &oauth2.Config{ClientID: o.clientID, ClientSecret: o.secret, RedirectURL: o.redirect,
		Endpoint: oauth2.Endpoint{AuthURL: d.AuthorizationEndpoint, TokenURL: d.TokenEndpoint},
		Scopes:   []string{"openid", "email", "profile"}}
	return o.cfg, nil
}

type loginState struct {
	State    string    `json:"s"`
	Nonce    string    `json:"n"`
	Verifier string    `json:"v"`
	Next     string    `json:"x"`
	Expires  time.Time `json:"e"`
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// SafeNext keeps a post-login redirect on this site.
func SafeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

// Start redirects the browser to Authentik.
func (o *OIDC) Start(w http.ResponseWriter, r *http.Request, next string) error {
	cfg, err := o.config(r.Context())
	if err != nil {
		return fmt.Errorf("can't reach Authentik: %w", err)
	}
	st := loginState{State: random(), Nonce: random(), Verifier: oauth2.GenerateVerifier(), Next: SafeNext(next),
		Expires: time.Now().Add(10 * time.Minute)}
	b, _ := json.Marshal(st)
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: o.sessions.Sign(b), Path: "/auth/", MaxAge: 600,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, cfg.AuthCodeURL(st.State, oauth2.S256ChallengeOption(st.Verifier),
		oauth2.SetAuthURLParam("nonce", st.Nonce)), http.StatusFound)
	return nil
}

// Callback finishes the login: it checks the state, exchanges the code, verifies the ID token
// (signature, issuer, audience, expiry, nonce) and issues an SSO session for the user with
// that email. It returns where to go next.
func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) (string, error) {
	c, err := r.Cookie(oidcCookie)
	if err != nil {
		return "", errors.New("the sign-in took too long or cookies are blocked; try again")
	}
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/auth/", MaxAge: -1, HttpOnly: true, Secure: true})
	raw, ok := o.sessions.Verify(c.Value)
	var st loginState
	if !ok || json.Unmarshal(raw, &st) != nil || time.Now().After(st.Expires) {
		return "", errors.New("the sign-in expired; try again")
	}
	if e := r.URL.Query().Get("error"); e != "" {
		return "", fmt.Errorf("Authentik said: %s %s", e, r.URL.Query().Get("error_description"))
	}
	if r.URL.Query().Get("state") != st.State {
		return "", errors.New("sign-in state mismatch; try again")
	}
	cfg, err := o.config(r.Context())
	if err != nil {
		return "", err
	}
	tok, err := cfg.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return "", fmt.Errorf("token exchange failed: %w", err)
	}
	idt, _ := tok.Extra("id_token").(string)
	if idt == "" {
		return "", errors.New("Authentik returned no ID token (is the openid scope enabled?)")
	}
	claims, err := o.verifier.Verify(r.Context(), idt)
	if err != nil {
		return "", fmt.Errorf("ID token rejected: %w", err)
	}
	if claims.Nonce != st.Nonce {
		return "", errors.New("ID token nonce mismatch")
	}
	var u db.User
	if err := o.db.Where("email = ? AND active = ?", claims.Email, true).First(&u).Error; err != nil {
		return "", UnknownEmailError{claims.Email}
	}
	o.sessions.Issue(w, u.ID, KindSSO)
	return st.Next, nil
}
