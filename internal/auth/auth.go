// Package auth works out who is making a request.
//
// Two listeners: the LAN listener accepts SSO or a PIN/password login cookie; the tunnel
// listener (what cloudflared points at) accepts only an SSO identity, verified on every
// request, and ignores cookies. A LAN request carrying Cloudflare headers is treated as tunnel
// traffic, so a mis-pointed tunnel can't expose PIN login.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/fwump38/hidden-isle-app/internal/config"
	"github.com/fwump38/hidden-isle-app/internal/db"
)

type Listener int

const (
	LAN Listener = iota
	Tunnel
)

func (l Listener) String() string {
	if l == Tunnel {
		return "tunnel"
	}
	return "lan"
}

var (
	ErrSSORequired = errors.New("sign-in through Cloudflare Access or Authentik is required here")
)

// UnknownEmailError means SSO succeeded but no user has that email.
type UnknownEmailError struct{ Email string }

func (e UnknownEmailError) Error() string {
	return fmt.Sprintf("%s isn't on the guest list; ask the Seer to add this email", e.Email)
}

type Authenticator struct {
	DB       *gorm.DB
	Cfg      *config.Config
	Sessions *Sessions
	CF       *JWTVerifier // nil when Cloudflare Access isn't configured
	OIDC     *OIDC        // "Sign in with Authentik"; nil when not configured
	Home     *Home        // recognizes tunnel requests from the home network; nil = never
	Limiter  *Limiter
}

func New(g *gorm.DB, cfg *config.Config, s *Sessions) *Authenticator {
	a := &Authenticator{DB: g, Cfg: cfg, Sessions: s, Limiter: NewLimiter(5, 10*time.Minute, 10*time.Minute)}
	if cfg.CFTeamDomain != "" {
		a.CF = NewCloudflareAccess(cfg.CFTeamDomain, cfg.CFAudience)
	}
	if cfg.OAuthIssuer != "" && cfg.OAuthAudience != "" && cfg.OAuthClientSecret != "" && cfg.PublicURL != "" {
		a.OIDC = NewOIDCLogin(g, s, cfg.OAuthIssuer, cfg.OAuthAudience, cfg.OAuthClientSecret, cfg.PublicURL)
	}
	return a
}

type ctxKey int

const (
	userKey ctxKey = iota
	reqInfoKey
)

// RequestInfo describes how a request arrived.
type RequestInfo struct {
	Listener Listener
	Tunnel   bool       // tunnel listener, or Cloudflare headers seen on the LAN listener
	Home     bool       // a tunnel request from the home network (counts like the LAN)
	Client   netip.Addr // the browser's address: Cf-Connecting-IP through the tunnel, else the TCP peer
	Via      string     // "sso", "cookie" or ""
	Err      error      // why no user was identified (SSO failure, unknown email)
}

// AtHome reports whether the request counts as coming from home (PIN login allowed).
func (i RequestInfo) AtHome() bool { return !i.Tunnel || i.Home }

// User returns the identified user, or nil.
func User(ctx context.Context) *db.User {
	u, _ := ctx.Value(userKey).(*db.User)
	return u
}

// Info returns how the request arrived.
func Info(ctx context.Context) RequestInfo {
	i, _ := ctx.Value(reqInfoKey).(RequestInfo)
	return i
}

// WithUser returns ctx carrying u (used by tests and the MCP bearer path).
func WithUser(ctx context.Context, u *db.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// Middleware identifies the user on every request. It never rejects; use RequireUser.
func (a *Authenticator) Middleware(l Listener) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			info := RequestInfo{Listener: l, Tunnel: l == Tunnel || fromCloudflare(r), Client: PeerIP(r)}
			if info.Tunnel {
				// Only cloudflared reaches the tunnel listener, and it sets this header.
				if c, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip"))); err == nil {
					info.Client = c.Unmap()
				}
				info.Home = a.Home != nil && a.Home.IsHome(info.Client)
			}
			u, via, err := a.identify(r, info)
			info.Via, info.Err = via, err
			ctx := context.WithValue(r.Context(), reqInfoKey, info)
			if u != nil {
				ctx = context.WithValue(ctx, userKey, u)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func fromCloudflare(r *http.Request) bool {
	return r.Header.Get("Cf-Access-Jwt-Assertion") != "" || r.Header.Get("Cf-Connecting-Ip") != ""
}

func (a *Authenticator) identify(r *http.Request, info RequestInfo) (*db.User, string, error) {
	email, err := a.ssoEmail(r)
	if err != nil {
		return nil, "", err
	}
	if email != "" {
		var u db.User
		if err := a.DB.Where("email = ? AND active = ?", email, true).First(&u).Error; err != nil {
			return nil, "", UnknownEmailError{email}
		}
		return &u, "sso", nil
	}
	// PIN/password sessions only count at home; Authentik sessions count anywhere.
	if id, kind := a.Sessions.UserID(r); id != 0 && (info.AtHome() || kind == KindSSO) {
		var u db.User
		if err := a.DB.Where("id = ? AND active = ?", id, true).First(&u).Error; err == nil {
			return &u, "cookie", nil
		}
	}
	if !info.AtHome() {
		return nil, "", ErrSSORequired
	}
	return nil, "", nil
}

// ssoEmail returns the verified SSO email, "" if the request carries no SSO identity.
func (a *Authenticator) ssoEmail(r *http.Request) (string, error) {
	if tok := r.Header.Get("Cf-Access-Jwt-Assertion"); tok != "" && a.CF != nil {
		c, err := a.CF.Verify(r.Context(), tok)
		if err != nil {
			return "", fmt.Errorf("cloudflare access token rejected: %w", err)
		}
		return c.Email, nil
	}
	if e := r.Header.Get("X-Authentik-Email"); e != "" && config.Contains(a.Cfg.AuthentikProxies, PeerIP(r)) {
		return strings.ToLower(strings.TrimSpace(e)), nil
	}
	return "", nil
}

// PeerIP is the TCP peer's address (headers are never trusted for this).
func PeerIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, _ := netip.ParseAddr(host)
	return addr.Unmap()
}

// PINAllowed reports whether this request may use PIN or password login: on the LAN listener
// from HI_LAN_CIDR, or through the tunnel from the home network.
func (a *Authenticator) PINAllowed(r *http.Request) bool {
	info := Info(r.Context())
	if info.Tunnel {
		return info.Home
	}
	return info.Listener == LAN && config.Contains(a.Cfg.LANCIDRs, PeerIP(r))
}

// Login checks a PIN (players) or password (Seer) and issues a cookie. Errors are safe to show.
func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request, userID uint, secret string) (*db.User, error) {
	if !a.PINAllowed(r) {
		return nil, errors.New("PIN login only works on the home network")
	}
	key := fmt.Sprintf("%d|%s", userID, Info(r.Context()).Client)
	if locked, left := a.Limiter.Locked(key); locked {
		return nil, fmt.Errorf("too many tries; wait %d minutes", int(left.Minutes())+1)
	}
	var u db.User
	if err := a.DB.Where("id = ? AND active = ?", userID, true).First(&u).Error; err != nil {
		return nil, errors.New("pick your name from the list")
	}
	hash := u.PINHash
	if u.IsSeer() {
		hash = u.PasswordHash
	}
	if hash == "" || bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret)) != nil {
		a.Limiter.Fail(key)
		if u.IsSeer() {
			return nil, errors.New("wrong password")
		}
		return nil, errors.New("wrong PIN")
	}
	a.Limiter.Succeed(key)
	a.Sessions.Issue(w, u.ID, KindLocal)
	return &u, nil
}

// Hash hashes a PIN or password for storage.
func Hash(secret string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	return string(b), err
}

// ValidPIN reports whether pin is 4-8 digits.
func ValidPIN(pin string) bool {
	if len(pin) < 4 || len(pin) > 8 {
		return false
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
