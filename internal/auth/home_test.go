package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"testing"
)

func traceServer(ip string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "fl=1\nh=1.1.1.1\nip=%s\nts=1\n", ip)
	}))
}

func TestHomeDetection(t *testing.T) {
	ts := traceServer("203.0.113.5")
	defer ts.Close()
	file := filepath.Join(t.TempDir(), "home.json")
	h := NewHome(file, []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")})
	h.trace = []string{ts.URL, "http://127.0.0.1:1/unreachable"}
	h.Refresh(context.Background())
	for addr, want := range map[string]bool{"203.0.113.5": true, "203.0.113.6": false, "198.51.100.77": true, "2001:db8:1:2::9": false} {
		if got := h.IsHome(netip.MustParseAddr(addr)); got != want {
			t.Errorf("IsHome(%s) = %v", addr, got)
		}
	}
	p, err := h.Learn(netip.MustParseAddr("2001:db8:1:2::9"))
	if err != nil || p.String() != "2001:db8:1:2::/64" {
		t.Fatalf("Learn = %v, %v", p, err)
	}
	// Learned networks survive a restart.
	h2 := NewHome(file, nil)
	if !h2.IsHome(netip.MustParseAddr("2001:db8:1:2::abcd")) || h2.IsHome(netip.MustParseAddr("2001:db8:1:3::1")) {
		t.Error("learned /64 not reloaded correctly")
	}
	if err := h2.Forget(p); err != nil || h2.IsHome(netip.MustParseAddr("2001:db8:1:2::9")) {
		t.Errorf("Forget: %v", err)
	}
	// A failed refresh keeps the last known address.
	h.trace = []string{"http://127.0.0.1:1/unreachable"}
	h.Refresh(context.Background())
	if !h.IsHome(netip.MustParseAddr("203.0.113.5")) || h.Status().Error == "" {
		t.Error("a failed refresh should keep the old address and report the error")
	}
}

func TestTunnelFromHome(t *testing.T) {
	f := setup(t)
	ts := traceServer("203.0.113.5")
	defer ts.Close()
	f.a.Home = NewHome(filepath.Join(t.TempDir(), "h.json"), nil)
	f.a.Home.trace = []string{ts.URL}
	f.a.Home.Refresh(context.Background())

	fromHome := func(r *http.Request) { r.AddCookie(f.cookie); r.Header.Set("Cf-Connecting-Ip", "203.0.113.5") }
	fromAway := func(r *http.Request) { r.AddCookie(f.cookie); r.Header.Set("Cf-Connecting-Ip", "192.0.2.44") }
	if name, info := f.who(Tunnel, "172.17.0.2:1", fromHome); name != "Ana" || !info.Home {
		t.Errorf("PIN session through the tunnel from home: got %q home=%v", name, info.Home)
	}
	if name, info := f.who(Tunnel, "172.17.0.2:1", fromAway); name != "" || info.Err != ErrSSORequired {
		t.Errorf("PIN session from away must not count: got %q %v", name, info.Err)
	}

	// An Authentik (SSO) session works from anywhere.
	rec := httptest.NewRecorder()
	f.a.Sessions.Issue(rec, f.player.ID, KindSSO)
	sso := rec.Result().Cookies()[0]
	if name, _ := f.who(Tunnel, "172.17.0.2:1", func(r *http.Request) { r.AddCookie(sso); r.Header.Set("Cf-Connecting-Ip", "192.0.2.44") }); name != "Ana" {
		t.Errorf("SSO session from away: got %q", name)
	}

	// PIN login: allowed through the tunnel only from home.
	login := func(ip string) error {
		var err error
		h := f.a.Middleware(Tunnel)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err = f.a.Login(w, r, f.player.ID, "4321")
		}))
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		r.RemoteAddr = "172.17.0.2:1"
		r.Header.Set("Cf-Connecting-Ip", ip)
		h.ServeHTTP(httptest.NewRecorder(), r)
		return err
	}
	if err := login("203.0.113.5"); err != nil {
		t.Errorf("PIN login from home through the tunnel: %v", err)
	}
	if err := login("192.0.2.44"); err == nil {
		t.Error("PIN login from away through the tunnel must fail")
	}
}

func TestSignedStateCantPassAsSession(t *testing.T) {
	f := setup(t)
	payload := make([]byte, payloadLen)
	payload[7] = byte(f.player.ID)
	for i := 8; i < 16; i++ {
		payload[i] = 0x7f
	}
	forged := &http.Cookie{Name: cookieName, Value: f.a.Sessions.Sign(payload)}
	if name, _ := f.who(LAN, "192.168.1.20:1", func(r *http.Request) { r.AddCookie(forged) }); name != "" {
		t.Error("a value signed for OIDC state was accepted as a session")
	}
}
