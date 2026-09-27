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

// Issue sets a signed cookie for userID. The cookie is only honored on the LAN listener.
func (s *Sessions) Issue(w http.ResponseWriter, userID uint) {
	payload := make([]byte, 16)
	binary.BigEndian.PutUint64(payload[:8], uint64(userID))
	binary.BigEndian.PutUint64(payload[8:], uint64(time.Now().Add(sessionTTL).Unix()))
	v := base64.RawURLEncoding.EncodeToString(append(payload, s.mac(payload)...))
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: v, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds())})
}

// Clear removes the session cookie.
func (s *Sessions) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// UserID returns the user in a valid, unexpired cookie, or 0.
func (s *Sessions) UserID(r *http.Request) uint {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != 16+sha256.Size {
		return 0
	}
	payload, sig := raw[:16], raw[16:]
	if !hmac.Equal(sig, s.mac(payload)) {
		return 0
	}
	if time.Now().Unix() > int64(binary.BigEndian.Uint64(payload[8:])) {
		return 0
	}
	return uint(binary.BigEndian.Uint64(payload[:8]))
}

func (s *Sessions) mac(b []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("hi-session-v1"))
	m.Write(b)
	return m.Sum(nil)
}
