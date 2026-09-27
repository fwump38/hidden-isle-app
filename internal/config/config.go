// Package config reads the app's settings from environment variables.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DataDir    string // HI_DATA_DIR: database, game data snapshots, backups, secrets
	LANAddr    string // HI_LAN_ADDR: listener for the home network (PIN login allowed)
	TunnelAddr string // HI_TUNNEL_ADDR: listener cloudflared points at (SSO required)
	PublicURL  string // HI_PUBLIC_URL: external base URL, e.g. https://isle.example.com (MCP resource id)

	SeerEmail    string // HI_SEER_EMAIL: SSO email that maps to the Seer
	SeerName     string // HI_SEER_NAME
	SeerPassword string // HI_SEER_PASSWORD: sets the Seer's LAN password on startup (optional)

	LANCIDRs []netip.Prefix // HI_LAN_CIDR: extra guard; PIN login also requires a client IP in these ranges

	CFTeamDomain string // HI_CF_TEAM_DOMAIN: e.g. myteam.cloudflareaccess.com
	CFAudience   string // HI_CF_AUD: the Access application's AUD tag

	AuthentikProxies []netip.Prefix // HI_AUTHENTIK_PROXY_IPS: only these peers may send X-authentik-email

	OAuthIssuer   string // HI_OAUTH_ISSUER: Authentik provider issuer, for MCP bearer tokens
	OAuthAudience string // HI_OAUTH_AUDIENCE: expected aud (the Authentik client ID)

	GameDataRepo     string        // GAMEDATA_REPO: git URL or local path
	GameDataRef      string        // GAMEDATA_REF
	GameDataToken    string        // GAMEDATA_TOKEN: GitHub fine-grained PAT (Contents: read-only)
	GameDataInterval time.Duration // GAMEDATA_INTERVAL

	BackupInterval time.Duration // HI_BACKUP_INTERVAL: consistent SQLite snapshot for the NAS backup (0 = off)
}

func Load() (*Config, error) {
	c := &Config{
		DataDir:       env("HI_DATA_DIR", "/data"),
		LANAddr:       env("HI_LAN_ADDR", ":8080"),
		TunnelAddr:    env("HI_TUNNEL_ADDR", ":8081"),
		PublicURL:     strings.TrimRight(os.Getenv("HI_PUBLIC_URL"), "/"),
		SeerEmail:     strings.ToLower(strings.TrimSpace(os.Getenv("HI_SEER_EMAIL"))),
		SeerName:      env("HI_SEER_NAME", "Seer"),
		SeerPassword:  os.Getenv("HI_SEER_PASSWORD"),
		CFTeamDomain:  strings.TrimSuffix(strings.TrimPrefix(os.Getenv("HI_CF_TEAM_DOMAIN"), "https://"), "/"),
		CFAudience:    os.Getenv("HI_CF_AUD"),
		OAuthIssuer:   os.Getenv("HI_OAUTH_ISSUER"),
		OAuthAudience: os.Getenv("HI_OAUTH_AUDIENCE"),
		GameDataRepo:  os.Getenv("GAMEDATA_REPO"),
		GameDataRef:   env("GAMEDATA_REF", "main"),
		GameDataToken: os.Getenv("GAMEDATA_TOKEN"),
	}
	var err error
	if c.LANCIDRs, err = prefixes("HI_LAN_CIDR", "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.0/8,::1/128,fc00::/7"); err != nil {
		return nil, err
	}
	if c.AuthentikProxies, err = prefixes("HI_AUTHENTIK_PROXY_IPS", ""); err != nil {
		return nil, err
	}
	if c.GameDataInterval, err = duration("GAMEDATA_INTERVAL", "1h"); err != nil {
		return nil, err
	}
	if c.BackupInterval, err = duration("HI_BACKUP_INTERVAL", "24h"); err != nil {
		return nil, err
	}
	if (c.CFTeamDomain == "") != (c.CFAudience == "") {
		return nil, fmt.Errorf("set both HI_CF_TEAM_DOMAIN and HI_CF_AUD, or neither")
	}
	return c, nil
}

// SSOConfigured reports whether any SSO source is set up (needed for the tunnel listener).
func (c *Config) SSOConfigured() bool {
	return c.CFTeamDomain != "" || len(c.AuthentikProxies) > 0
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func duration(key, def string) (time.Duration, error) {
	v := env(key, def)
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func prefixes(key, def string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range strings.Split(env(key, def), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			addr, err := netip.ParseAddr(s)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// Contains reports whether addr is inside any of the prefixes.
func Contains(ps []netip.Prefix, addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range ps {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
