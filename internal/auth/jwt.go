package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// JWTVerifier checks RS/ES-signed JWTs against a JWKS, issuer and audience. The JWKS is fetched
// lazily on first use (and refreshed in the background by keyfunc), so the app starts even
// when the identity provider is unreachable.
type JWTVerifier struct {
	issuer, audience string
	jwksURL          func(context.Context) (string, error)

	mu sync.Mutex
	kf keyfunc.Keyfunc
	at time.Time // last failed discovery
}

// Claims are the parts of a verified token the app uses.
type Claims struct {
	Email   string
	Subject string
	Scopes  []string
	Expiry  time.Time
}

// NewCloudflareAccess verifies the Cf-Access-Jwt-Assertion header for one Access application.
func NewCloudflareAccess(teamDomain, aud string) *JWTVerifier {
	base := "https://" + teamDomain
	return &JWTVerifier{issuer: base, audience: aud,
		jwksURL: func(context.Context) (string, error) { return base + "/cdn-cgi/access/certs", nil }}
}

// NewOIDC verifies access tokens from an OIDC provider (e.g. an Authentik OAuth2 provider),
// discovering the JWKS from <issuer>/.well-known/openid-configuration.
func NewOIDC(issuer, aud string) *JWTVerifier {
	return &JWTVerifier{issuer: issuer, audience: aud, jwksURL: func(ctx context.Context) (string, error) {
		return discoverJWKS(ctx, issuer)
	}}
}

func (v *JWTVerifier) keyfunc(ctx context.Context) (keyfunc.Keyfunc, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.kf != nil {
		return v.kf, nil
	}
	// After a failed discovery, retry at most once a minute.
	if !v.at.IsZero() && time.Since(v.at) < time.Minute {
		return nil, errors.New("identity provider unavailable; retrying shortly")
	}
	url, err := v.jwksURL(ctx)
	if err == nil {
		// Background refresh must outlive this request.
		v.kf, err = keyfunc.NewDefaultCtx(context.Background(), []string{url})
	}
	if err != nil {
		v.kf, v.at = nil, time.Now()
		return nil, err
	}
	return v.kf, nil
}

// Verify parses and validates token, returning its claims.
func (v *JWTVerifier) Verify(ctx context.Context, token string) (*Claims, error) {
	kf, err := v.keyfunc(ctx)
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	opts := []jwt.ParserOption{jwt.WithIssuer(v.issuer), jwt.WithExpirationRequired(), jwt.WithLeeway(30 * time.Second),
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "PS256"})}
	if v.audience != "" {
		opts = append(opts, jwt.WithAudience(v.audience))
	}
	mc := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(token, mc, kf.KeyfuncCtx(ctx), opts...); err != nil {
		return nil, err
	}
	c := &Claims{}
	c.Email, _ = mc["email"].(string)
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	c.Subject, _ = mc.GetSubject()
	if exp, _ := mc.GetExpirationTime(); exp != nil {
		c.Expiry = exp.Time
	}
	if s, ok := mc["scope"].(string); ok {
		c.Scopes = strings.Fields(s)
	}
	if c.Email == "" {
		return nil, errors.New("token has no email claim")
	}
	return c, nil
}

func discoverJWKS(ctx context.Context, issuer string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openid-configuration: %s", resp.Status)
	}
	var doc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", err
	}
	if doc.JWKSURI == "" {
		return "", errors.New("openid-configuration has no jwks_uri")
	}
	return doc.JWKSURI, nil
}
