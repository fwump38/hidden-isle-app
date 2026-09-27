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
	"github.com/fwump38/hidden-isle-app/internal/cards"
	"github.com/fwump38/hidden-isle-app/internal/config"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
)

// TokenPrefix starts every API token, so the verifier can tell them from JWTs.
const TokenPrefix = "hi_"

type Server struct {
	db    *gorm.DB
	cfg   *config.Config
	data  *gamedata.Store
	oidc  *auth.JWTVerifier // nil when HI_OAUTH_ISSUER isn't set
	mcp   *mcp.Server
	build string
}

func New(g *gorm.DB, cfg *config.Config, data *gamedata.Store, build string) *Server {
	s := &Server{db: g, cfg: cfg, data: data, build: build}
	if cfg.OAuthIssuer != "" {
		s.oidc = auth.NewOIDC(cfg.OAuthIssuer, cfg.OAuthAudience)
	}
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "hidden-isle", Title: "The Hidden Isle", Version: build},
		&mcp.ServerOptions{Instructions: "Campaign state and rules data for The Hidden Isle tarot RPG. " +
			"The table draws real tarot cards: ask the Seer what was drawn and use draw_cards only when asked for a digital draw."})
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

func (s *Server) seer(ctx context.Context, req *mcp.CallToolRequest) (*db.User, error) {
	if req.Extra == nil || req.Extra.TokenInfo == nil {
		return nil, errors.New("not authenticated")
	}
	var u db.User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", req.Extra.TokenInfo.UserID).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

type empty struct{}

type whoamiOut struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	Build    string `json:"build"`
	GameData string `json:"game_data" jsonschema:"snapshot id of the rules data in use"`
}

type classIn struct {
	Class string `json:"class" jsonschema:"class id or name, e.g. hunter or Hunter"`
}

type drawIn struct {
	Deck  string          `json:"deck" jsonschema:"vision (22 Majors + 16 Courts) or pips (Ace-10 in four suits)"`
	Hands []cards.Request `json:"hands" jsonschema:"one entry per hand, e.g. [{name: agent, count: 3}, {name: seer, count: 4}]"`
}

type drawOut struct {
	Hands []cards.Hand `json:"hands"`
	Note  string       `json:"note"`
}

func (s *Server) addTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "whoami", Description: "Who this connection is, the app build and the game data in use. Use it to check the connection."},
		func(ctx context.Context, req *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, whoamiOut, error) {
			u, err := s.seer(ctx, req)
			if err != nil {
				return nil, whoamiOut{}, err
			}
			out := whoamiOut{Name: u.Name, Role: string(u.Role), Build: s.build}
			if snap := s.data.Current(); snap != nil {
				out.GameData = snap.ID
			}
			return nil, out, nil
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "get_class", Description: "A class with its motto, pre-filled skills, items and every ability. Ability text is verbatim from Rulebook 1.4 (with page); sheet_text is the Character Sheets 1.3 wording where it differs. Quote abilities exactly."},
		func(ctx context.Context, req *mcp.CallToolRequest, in classIn) (*mcp.CallToolResult, gamedata.Class, error) {
			snap, err := s.snapshot()
			if err != nil {
				return nil, gamedata.Class{}, err
			}
			c := snap.Class(strings.ToLower(in.Class))
			if c == nil {
				c = snap.Class(in.Class)
			}
			if c == nil {
				return nil, gamedata.Class{}, fmt.Errorf("no class %q", in.Class)
			}
			return nil, *c, nil
		})

	mcp.AddTool(s.mcp, &mcp.Tool{Name: "draw_cards", Description: "Digital card draw, only when the Seer asks for one (the table normally draws real cards). Hands come from one shuffled deck, so no card is in two hands. Ace = 11 in challenges, 1 for fate numbers."},
		func(ctx context.Context, req *mcp.CallToolRequest, in drawIn) (*mcp.CallToolResult, drawOut, error) {
			snap, err := s.snapshot()
			if err != nil {
				return nil, drawOut{}, err
			}
			hands, err := cards.Draw(snap, in.Deck, in.Hands)
			if err != nil {
				return nil, drawOut{}, err
			}
			return nil, drawOut{Hands: hands, Note: "The Seer picks the Seer's card; each player picks their own."}, nil
		})
}
