package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	cookieName = "hi_session"
	sessionTTL = 30 * 24 * time.Hour
)

// Sessions signs LAN login cookies with a key kept in the data directory.
type Sessions struct {
	key []byte
}

// LoadSessions reads the signing key from path, creating it on first run.
func LoadSessions(path string) (*Sessions, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) < 32 {
		return nil, errors.New("session key is too short; delete it to regenerate")
	}
	return &Sessions{key: key}, nil
}

// How a session was earned. Local sessions (PIN or the Seer's LAN password) only count at home;
// SSO sessions (Sign in with Authentik) count anywhere.
const (
	KindLocal byte = 0
	KindSSO   byte = 1
)

const payloadLen = 17 // user id (8) + expiry (8) + kind (1)

// Issue sets a signed session cookie for userID.
func (s *Sessions) Issue(w http.ResponseWriter, userID uint, kind byte) {
	payload := make([]byte, payloadLen)
	binary.BigEndian.PutUint64(payload[:8], uint64(userID))
	binary.BigEndian.PutUint64(payload[8:16], uint64(time.Now().Add(sessionTTL).Unix()))
	payload[16] = kind
	v := base64.RawURLEncoding.EncodeToString(append(payload, s.mac(payload)...))
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: v, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds())})
}

// Clear removes the session cookie.
func (s *Sessions) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// UserID returns the user and session kind in a valid, unexpired cookie, or 0.
func (s *Sessions) UserID(r *http.Request) (uint, byte) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return 0, 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != payloadLen+sha256.Size {
		return 0, 0
	}
	payload, sig := raw[:payloadLen], raw[payloadLen:]
	if !hmac.Equal(sig, s.mac(payload)) {
		return 0, 0
	}
	if time.Now().Unix() > int64(binary.BigEndian.Uint64(payload[8:16])) {
		return 0, 0
	}
	return uint(binary.BigEndian.Uint64(payload[:8])), payload[16]
}

// Sign and Verify protect small values (the OIDC login state) with the session key.
func (s *Sessions) Sign(v []byte) string {
	return base64.RawURLEncoding.EncodeToString(append(append([]byte{}, v...), s.macWith("hi-sign-v1", v)...))
}

func (s *Sessions) Verify(token string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < sha256.Size {
		return nil, false
	}
	v, sig := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	return v, hmac.Equal(sig, s.macWith("hi-sign-v1", v))
}

func (s *Sessions) mac(b []byte) []byte { return s.macWith("hi-session-v2", b) }

// macWith keys the MAC by purpose, so a value signed for one use can't pass as another.
func (s *Sessions) macWith(purpose string, b []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(purpose + "\x00"))
	m.Write(b)
	return m.Sum(nil)
}
