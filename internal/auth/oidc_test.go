package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeIdP is a minimal OpenID provider: discovery, JWKS, and a token endpoint that returns an
// RS256 ID token for the email and nonce the test sets.
type fakeIdP struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	mu    sync.Mutex
	email string
	nonce string
	aud   string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, aud: "client-1"}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		b64 := base64.RawURLEncoding.EncodeToString
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes())}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.FormValue("code") != "good-code" || r.FormValue("code_verifier") == "" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		claims := jwt.MapClaims{"iss": f.srv.URL, "aud": f.aud, "sub": "u1", "email": f.email, "nonce": f.nonce,
			"exp": time.Now().Add(5 * time.Minute).Unix(), "iat": time.Now().Unix()}
		f.mu.Unlock()
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "k1"
		signed, _ := tok.SignedString(key)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"at","token_type":"Bearer","expires_in":300,"id_token":%q}`, signed)
	})
	t.Cleanup(f.srv.Close)
	return f
}

func TestOIDCLogin(t *testing.T) {
	f := setup(t)
	idp := newFakeIdP(t)
	o := NewOIDCLogin(f.a.DB, f.a.Sessions, idp.srv.URL, "client-1", "secret", "https://isle.example.com")

	// start returns the login-state cookie and the provider's authorize URL.
	start := func() (*http.Cookie, url.Values) {
		rec := httptest.NewRecorder()
		if err := o.Start(rec, httptest.NewRequest(http.MethodGet, "/auth/login?next=/c/1", nil), "/c/1"); err != nil {
			t.Fatal(err)
		}
		loc, _ := url.Parse(rec.Header().Get("Location"))
		if !strings.HasPrefix(loc.String(), idp.srv.URL+"/authorize") || loc.Query().Get("code_challenge_method") != "S256" {
			t.Fatalf("bad authorize redirect: %s", loc)
		}
		return rec.Result().Cookies()[0], loc.Query()
	}
	callback := func(c *http.Cookie, query string) (string, *http.Cookie, error) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/auth/callback?"+query, nil)
		r.AddCookie(c)
		next, err := o.Callback(rec, r)
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == cookieName {
				return next, ck, err
			}
		}
		return next, nil, err
	}

	// Happy path: an SSO session for the user with that email.
	c, q := start()
	idp.email, idp.nonce = "ana@example.com", q.Get("nonce")
	next, sess, err := callback(c, "code=good-code&state="+q.Get("state"))
	if err != nil || next != "/c/1" || sess == nil {
		t.Fatalf("callback: next=%q session=%v err=%v", next, sess, err)
	}
	if id, kind := f.a.Sessions.UserID(func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(sess)
		return r
	}()); id != f.player.ID || kind != KindSSO {
		t.Errorf("session = user %d kind %d", id, kind)
	}

	// Wrong state, wrong nonce, unknown email, wrong audience: all refused.
	c, q = start()
	if _, _, err := callback(c, "code=good-code&state=forged"); err == nil {
		t.Error("forged state accepted")
	}
	c, q = start()
	idp.nonce = "replayed"
	if _, _, err := callback(c, "code=good-code&state="+q.Get("state")); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Errorf("wrong nonce: %v", err)
	}
	c, q = start()
	idp.email, idp.nonce = "stranger@example.com", q.Get("nonce")
	if _, _, err := callback(c, "code=good-code&state="+q.Get("state")); err == nil || !strings.Contains(err.Error(), "guest list") {
		t.Errorf("unknown email: %v", err)
	}
	c, q = start()
	idp.email, idp.nonce, idp.aud = "ana@example.com", q.Get("nonce"), "some-other-client"
	if _, _, err := callback(c, "code=good-code&state="+q.Get("state")); err == nil {
		t.Error("ID token for another client accepted")
	}
	if got := SafeNext("//evil.example.com"); got != "/" {
		t.Errorf("SafeNext allowed %q", got)
	}
}
