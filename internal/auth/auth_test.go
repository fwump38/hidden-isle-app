package auth

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fwump38/hidden-isle-app/internal/config"
	"github.com/fwump38/hidden-isle-app/internal/db"
)

type fixture struct {
	a      *Authenticator
	player db.User
	cookie *http.Cookie
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	g, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := LoadSessions(filepath.Join(dir, "session.key"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		LANCIDRs:         []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("127.0.0.0/8")},
		AuthentikProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.5/32")},
	}
	pin, _ := Hash("4321")
	email := "ana@example.com"
	p := db.User{Name: "Ana", Role: db.RolePlayer, Active: true, PINHash: pin, Email: &email}
	if err := g.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	f := &fixture{a: New(g, cfg, s), player: p}
	rec := httptest.NewRecorder()
	s.Issue(rec, p.ID, KindLocal)
	f.cookie = rec.Result().Cookies()[0]
	return f
}

// who runs a request through the middleware and returns the identified user's name and info.
func (f *fixture) who(l Listener, remote string, mutate func(*http.Request)) (string, RequestInfo) {
	var name string
	var info RequestInfo
	h := f.a.Middleware(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := User(r.Context()); u != nil {
			name = u.Name
		}
		info = Info(r.Context())
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	if mutate != nil {
		mutate(r)
	}
	h.ServeHTTP(httptest.NewRecorder(), r)
	return name, info
}

func TestCookieOnlyOnLAN(t *testing.T) {
	f := setup(t)
	withCookie := func(r *http.Request) { r.AddCookie(f.cookie) }
	if name, _ := f.who(LAN, "192.168.1.20:5000", withCookie); name != "Ana" {
		t.Errorf("LAN cookie: got %q, want Ana", name)
	}
	if name, info := f.who(Tunnel, "192.168.1.20:5000", withCookie); name != "" || info.Err != ErrSSORequired {
		t.Errorf("tunnel must ignore cookies: got %q, %v", name, info.Err)
	}
	cf := func(r *http.Request) { r.AddCookie(f.cookie); r.Header.Set("Cf-Connecting-Ip", "1.2.3.4") }
	if name, info := f.who(LAN, "192.168.1.20:5000", cf); name != "" || !info.Tunnel {
		t.Errorf("Cloudflare headers on the LAN listener must be treated as tunnel: got %q, tunnel=%v", name, info.Tunnel)
	}
}

func TestAuthentikHeaderOnlyFromProxy(t *testing.T) {
	f := setup(t)
	hdr := func(r *http.Request) { r.Header.Set("X-Authentik-Email", "Ana@Example.com") }
	if name, _ := f.who(Tunnel, "10.0.0.5:4000", hdr); name != "Ana" {
		t.Errorf("header from proxy: got %q, want Ana", name)
	}
	if name, _ := f.who(Tunnel, "10.0.0.6:4000", hdr); name != "" {
		t.Errorf("header from a non-proxy peer must be ignored: got %q", name)
	}
	unknown := func(r *http.Request) { r.Header.Set("X-Authentik-Email", "eve@example.com") }
	if _, info := f.who(Tunnel, "10.0.0.5:4000", unknown); info.Err == nil || !strings.Contains(info.Err.Error(), "guest list") {
		t.Errorf("unknown email: got %v", info.Err)
	}
}

func TestPINLogin(t *testing.T) {
	f := setup(t)
	login := func(l Listener, remote string, extra func(*http.Request), pin string) (int, error) {
		var err error
		rec := httptest.NewRecorder()
		h := f.a.Middleware(l)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err = f.a.Login(w, r, f.player.ID, pin)
		}))
		r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{}.Encode()))
		r.RemoteAddr = remote
		if extra != nil {
			extra(r)
		}
		h.ServeHTTP(rec, r)
		return len(rec.Result().Cookies()), err
	}
	if n, err := login(LAN, "192.168.1.20:1", nil, "4321"); err != nil || n != 1 {
		t.Fatalf("LAN PIN login: cookies=%d err=%v", n, err)
	}
	if _, err := login(Tunnel, "192.168.1.20:1", nil, "4321"); err == nil {
		t.Error("PIN login must fail on the tunnel listener")
	}
	if _, err := login(LAN, "8.8.8.8:1", nil, "4321"); err == nil {
		t.Error("PIN login must fail from outside HI_LAN_CIDR")
	}
	if _, err := login(LAN, "192.168.1.20:1", func(r *http.Request) { r.Header.Set("Cf-Connecting-Ip", "1.1.1.1") }, "4321"); err == nil {
		t.Error("PIN login must fail when Cloudflare headers are present")
	}
	for i := 0; i < 5; i++ {
		login(LAN, "192.168.1.30:1", nil, "0000")
	}
	if _, err := login(LAN, "192.168.1.30:1", nil, "4321"); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("expected lockout after 5 failures, got %v", err)
	}
	if _, err := login(LAN, "192.168.1.31:1", nil, "4321"); err != nil {
		t.Errorf("lockout must be per client IP: %v", err)
	}
}

func TestSessionTamper(t *testing.T) {
	f := setup(t)
	bad := *f.cookie
	// Flip a character inside the signature.
	i := len(bad.Value) - 5
	flip := byte('A')
	if bad.Value[i] == 'A' {
		flip = 'B'
	}
	bad.Value = bad.Value[:i] + string(flip) + bad.Value[i+1:]
	if name, _ := f.who(LAN, "192.168.1.20:1", func(r *http.Request) { r.AddCookie(&bad) }); name != "" {
		t.Errorf("tampered cookie accepted as %q", name)
	}
}

func TestValidPIN(t *testing.T) {
	for pin, want := range map[string]bool{"1234": true, "12345678": true, "123": false, "123456789": false, "12a4": false} {
		if ValidPIN(pin) != want {
			t.Errorf("ValidPIN(%q) = %v", pin, !want)
		}
	}
}
