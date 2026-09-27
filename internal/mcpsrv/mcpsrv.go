// Package mcpsrv is the Seer's MCP endpoint (/mcp, streamable HTTP). Only the Seer may use it:
// every request needs a bearer token that is either a Seer API token (Claude Code) or an OAuth
// access token from the configured issuer (Authentik) whose email belongs to the Seer.
package mcpsrv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"gorm.io/gorm"

	"github.com/fwump38/hidden-isle-app/internal/auth"
	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/config"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
	"github.com/fwump38/hidden-isle-app/internal/rules"
)

// TokenPrefix starts every API token, so the verifier can tell them from JWTs.
const TokenPrefix = "hi_"

type Server struct {
	db    *gorm.DB
	cfg   *config.Config
	data  *gamedata.Store
	svc   *campaign.Service
	rules *rules.Index
	oidc  *auth.JWTVerifier // nil when HI_OAUTH_ISSUER isn't set
	mcp   *mcp.Server
	build string

	testUser *db.User // tests only: the user when there's no bearer token
}

func New(g *gorm.DB, cfg *config.Config, data *gamedata.Store, svc *campaign.Service, idx *rules.Index, build string) *Server {
	s := &Server{db: g, cfg: cfg, data: data, svc: svc, rules: idx, build: build}
	if cfg.OAuthIssuer != "" {
		s.oidc = auth.NewOIDC(cfg.OAuthIssuer, cfg.OAuthAudience)
	}
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "hidden-isle", Title: "The Hidden Isle", Version: build},
		&mcp.ServerOptions{Instructions: instructions})
	s.addTools()
	return s
}

// Register mounts /mcp and the OAuth protected-resource metadata on mux.
func (s *Server) Register(mux *http.ServeMux) {
	prmURL := s.cfg.PublicURL + "/.well-known/oauth-protected-resource/mcp"
	meta := &oauthex.ProtectedResourceMetadata{
		Resource:               s.cfg.PublicURL + "/mcp",
		ResourceName:           "The Hidden Isle",
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        []string{"openid", "email", "profile"},
	}
	if s.cfg.OAuthIssuer != "" {
		meta.AuthorizationServers = []string{s.cfg.OAuthIssuer}
	}
	prm := mcpauth.ProtectedResourceMetadataHandler(meta)
	mux.Handle("/.well-known/oauth-protected-resource", prm)
	mux.Handle("/.well-known/oauth-protected-resource/mcp", prm)

	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, nil)
	mux.Handle("/mcp", mcpauth.RequireBearerToken(s.verify, &mcpauth.RequireBearerTokenOptions{
		ResourceMetadataURL: prmURL, ClockSkew: 30 * time.Second})(streamable))
}

// verify accepts a Seer API token or an OAuth access token for the Seer's email.
func (s *Server) verify(ctx context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
	if strings.HasPrefix(token, TokenPrefix) {
		var t db.APIToken
		err := s.db.WithContext(ctx).Preload("User").
			Where("hash = ? AND revoked_at IS NULL", HashToken(token)).First(&t).Error
		if err != nil || !t.User.Active || !t.User.IsSeer() {
			return nil, fmt.Errorf("%w: unknown or revoked API token", mcpauth.ErrInvalidToken)
		}
		now := time.Now()
		s.db.Model(&t).Update("last_used_at", now)
		return &mcpauth.TokenInfo{UserID: fmt.Sprint(t.UserID), Expiration: now.Add(time.Hour)}, nil
	}
	if s.oidc == nil {
		return nil, fmt.Errorf("%w: OAuth isn't configured (HI_OAUTH_ISSUER); use an API token", mcpauth.ErrInvalidToken)
	}
	c, err := s.oidc.Verify(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", mcpauth.ErrInvalidToken, err)
	}
	var u db.User
	if err := s.db.WithContext(ctx).Where("email = ? AND active = ?", c.Email, true).First(&u).Error; err != nil || !u.IsSeer() {
		return nil, fmt.Errorf("%w: %s is not the Seer", mcpauth.ErrInvalidToken, c.Email)
	}
	return &mcpauth.TokenInfo{UserID: fmt.Sprint(u.ID), Expiration: c.Expiry, Scopes: c.Scopes}, nil
}

// HashToken is how API tokens are stored.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *Server) snapshot() (*gamedata.Snapshot, error) {
	snap := s.data.Current()
	if snap == nil {
		return nil, errors.New("game data hasn't loaded yet; check the admin page")
	}
	return snap, nil
}

// actor is the Seer behind this request (MCP is Seer-only).
func (s *Server) actor(ctx context.Context, req *mcp.CallToolRequest) (campaign.Actor, error) {
	var u db.User
	switch {
	case req != nil && req.Extra != nil && req.Extra.TokenInfo != nil:
		if err := s.db.WithContext(ctx).First(&u, "id = ?", req.Extra.TokenInfo.UserID).Error; err != nil {
			return campaign.Actor{}, err
		}
	case s.testUser != nil:
		u = *s.testUser
	default:
		return campaign.Actor{}, errors.New("not authenticated")
	}
	if !u.IsSeer() || !u.Active {
		return campaign.Actor{}, errors.New("only the Seer may use MCP")
	}
	return campaign.Actor{User: &u, Via: "mcp"}, nil
}
