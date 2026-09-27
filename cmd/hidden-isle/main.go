// Command hidden-isle serves the Hidden Isle web app and MCP endpoint.
//
//	hidden-isle              run the server (configured by environment variables; see README)
//	hidden-isle healthcheck  exit 0 if the local server answers /healthz (for Docker HEALTHCHECK)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gorm.io/gorm"

	"github.com/fwump38/hidden-isle-app/internal/auth"
	"github.com/fwump38/hidden-isle-app/internal/campaign"
	"github.com/fwump38/hidden-isle-app/internal/config"
	"github.com/fwump38/hidden-isle-app/internal/db"
	"github.com/fwump38/hidden-isle-app/internal/gamedata"
	"github.com/fwump38/hidden-isle-app/internal/mcpsrv"
	"github.com/fwump38/hidden-isle-app/internal/rules"
	"github.com/fwump38/hidden-isle-app/internal/web"
)

// build is set at link time: -ldflags "-X main.build=v1.2.3".
var build = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	g, err := db.Open(filepath.Join(cfg.DataDir, "hidden-isle.db"))
	if err != nil {
		return err
	}
	if err := ensureSeer(g, cfg); err != nil {
		return err
	}
	sessions, err := auth.LoadSessions(filepath.Join(cfg.DataDir, "secrets", "session.key"))
	if err != nil {
		return err
	}
	authn := auth.New(g, cfg, sessions)
	if cfg.HomeDetect {
		authn.Home = auth.NewHome(filepath.Join(cfg.DataDir, "home-networks.json"), cfg.HomeNetworks)
		go authn.Home.Run(ctx, 5*time.Minute)
	}
	if authn.OIDC == nil && cfg.OAuthIssuer != "" {
		slog.Info("set HI_OAUTH_CLIENT_SECRET to enable Sign in with Authentik in the browser")
	}

	data := gamedata.NewStore(cfg.DataDir, cfg.GameDataRepo, cfg.GameDataRef, cfg.GameDataToken)
	data.SetKnownHosts(cfg.GameDataKnownHosts)
	if pub, err := data.DeployPublicKey(); err != nil {
		return fmt.Errorf("deploy key: %w", err)
	} else if pub != "" {
		slog.Info("rules repo deploy key (add it to the repo as a read-only deploy key)", "key", pub)
	}
	if err := data.LoadExisting(); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("couldn't load the saved game data snapshot", "err", err)
	}
	go data.Run(ctx, cfg.GameDataInterval)
	go db.RunBackups(ctx, g, filepath.Join(cfg.DataDir, "backup", "hidden-isle.db"), cfg.BackupInterval)

	svc := campaign.New(g, data)
	ui, err := web.New(g, cfg, authn, data, svc, build)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	ui.Register(mux)
	idx, err := rules.New(g)
	if err != nil {
		return err
	}
	index := func(snap *gamedata.Snapshot) {
		if err := idx.Build(snap); err != nil {
			slog.Error("rules index", "err", err)
		}
	}
	data.OnLoad(index)
	go index(data.Current())
	mcpsrv.New(g, cfg, data, svc, idx, build).Register(mux)

	// CSRF: reject cross-site browser writes. /mcp uses bearer tokens, not cookies, so it's exempt.
	cop := http.NewCrossOriginProtection()
	cop.AddInsecureBypassPattern("/mcp")

	if !cfg.SSOConfigured() {
		slog.Warn("no SSO configured (HI_CF_TEAM_DOMAIN/HI_CF_AUD or HI_AUTHENTIK_PROXY_IPS): the tunnel listener will only serve /mcp and /healthz")
	}
	if cfg.PublicURL == "" {
		slog.Warn("HI_PUBLIC_URL is not set: MCP OAuth discovery needs the external URL")
	}

	servers := []*http.Server{
		newServer(cfg.LANAddr, cop.Handler(authn.Middleware(auth.LAN)(mux))),
		newServer(cfg.TunnelAddr, cop.Handler(authn.Middleware(auth.Tunnel)(mux))),
	}
	errc := make(chan error, len(servers))
	for i, s := range servers {
		name := []string{"lan", "tunnel"}[i]
		slog.Info("listening", "listener", name, "addr", s.Addr, "build", build)
		go func() {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("%s listener: %w", name, err)
			}
		}()
	}
	select {
	case err := <-errc:
		stop()
		shutdown(servers)
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdown(servers)
	return nil
}

func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
}

func shutdown(servers []*http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(ctx)
	}
}

// ensureSeer creates the Seer account on first run and applies HI_SEER_EMAIL / HI_SEER_PASSWORD.
func ensureSeer(g *gorm.DB, cfg *config.Config) error {
	var seer db.User
	err := g.Where("role = ?", db.RoleSeer).Order("id").First(&seer).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if cfg.SeerEmail == "" && cfg.SeerPassword == "" {
			slog.Warn("no Seer account yet: set HI_SEER_EMAIL (SSO) and/or HI_SEER_PASSWORD (LAN) and restart")
			return nil
		}
		seer = db.User{Name: cfg.SeerName, Role: db.RoleSeer, Active: true}
	} else if err != nil {
		return err
	}
	if cfg.SeerEmail != "" {
		seer.Email = &cfg.SeerEmail
	}
	if cfg.SeerPassword != "" {
		if len(cfg.SeerPassword) < 8 {
			return errors.New("HI_SEER_PASSWORD must be at least 8 characters")
		}
		if seer.PasswordHash, err = auth.Hash(cfg.SeerPassword); err != nil {
			return err
		}
	}
	seer.Active = true
	if err := g.Save(&seer).Error; err != nil {
		return fmt.Errorf("save Seer account: %w", err)
	}
	return nil
}

func healthcheck() int {
	addr := os.Getenv("HI_LAN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
