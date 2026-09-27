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

	OAuthIssuer       string // HI_OAUTH_ISSUER: Authentik provider issuer, for MCP bearer tokens
	OAuthAudience     string // HI_OAUTH_AUDIENCE: the Authentik client ID (expected token audience)
	OAuthClientSecret string // HI_OAUTH_CLIENT_SECRET: enables "Sign in with Authentik" in the browser

	HomeNetworks []netip.Prefix // HI_HOME_NETWORKS: extra networks that count as home through the tunnel
	HomeDetect   bool           // HI_HOME_DETECT: learn the home public IP from Cloudflare (default on)

	GameDataRepo       string        // GAMEDATA_REPO: git URL or local path
	GameDataRef        string        // GAMEDATA_REF: release (pinned releases, default), a tag like v1.2.0, or a branch
	GameDataToken      string        // GAMEDATA_TOKEN: HTTPS token (not needed with an SSH URL + deploy key)
	GameDataKnownHosts string        // GAMEDATA_KNOWN_HOSTS: known_hosts file for SSH hosts other than github.com
	GameDataInterval   time.Duration // GAMEDATA_INTERVAL

	BackupInterval time.Duration // HI_BACKUP_INTERVAL: consistent SQLite snapshot for the NAS backup (0 = off)

	AnthropicAPIKey   string  // ANTHROPIC_API_KEY: enables the in-app player chat (unset = disabled)
	ChatModel         string  // HI_CHAT_MODEL
	ChatMonthlyCapUSD float64 // HI_CHAT_MONTHLY_CAP_USD: table-wide monthly spend cap (0 = no cap)
	ChatPlayerCapUSD  float64 // HI_CHAT_PLAYER_CAP_USD: per-player monthly spend cap (0 = no cap)
	ChatPriceInUSD    float64 // HI_CHAT_PRICE_IN: $ per million input tokens, for the cost estimate
	ChatPriceOutUSD   float64 // HI_CHAT_PRICE_OUT: $ per million output tokens
}

func Load() (*Config, error) {
	c := &Config{
		DataDir:            env("HI_DATA_DIR", "/data"),
		LANAddr:            env("HI_LAN_ADDR", ":8080"),
		TunnelAddr:         env("HI_TUNNEL_ADDR", ":8081"),
		PublicURL:          strings.TrimRight(os.Getenv("HI_PUBLIC_URL"), "/"),
		SeerEmail:          strings.ToLower(strings.TrimSpace(os.Getenv("HI_SEER_EMAIL"))),
		SeerName:           env("HI_SEER_NAME", "Seer"),
		SeerPassword:       os.Getenv("HI_SEER_PASSWORD"),
		CFTeamDomain:       strings.TrimSuffix(strings.TrimPrefix(os.Getenv("HI_CF_TEAM_DOMAIN"), "https://"), "/"),
		CFAudience:         os.Getenv("HI_CF_AUD"),
		OAuthIssuer:        os.Getenv("HI_OAUTH_ISSUER"),
		OAuthAudience:      os.Getenv("HI_OAUTH_AUDIENCE"),
		OAuthClientSecret:  os.Getenv("HI_OAUTH_CLIENT_SECRET"),
		HomeDetect:         env("HI_HOME_DETECT", "on") != "off",
		GameDataRepo:       os.Getenv("GAMEDATA_REPO"),
		GameDataRef:        env("GAMEDATA_REF", "release"),
		GameDataToken:      os.Getenv("GAMEDATA_TOKEN"),
		GameDataKnownHosts: os.Getenv("GAMEDATA_KNOWN_HOSTS"),
		AnthropicAPIKey:    os.Getenv("ANTHROPIC_API_KEY"),
		ChatModel:          env("HI_CHAT_MODEL", "claude-sonnet-5"),
	}
	var err error
	if c.ChatMonthlyCapUSD, err = floatEnv("HI_CHAT_MONTHLY_CAP_USD", "20"); err != nil {
		return nil, err
	}
	if c.ChatPlayerCapUSD, err = floatEnv("HI_CHAT_PLAYER_CAP_USD", "5"); err != nil {
		return nil, err
	}
	if c.ChatPriceInUSD, err = floatEnv("HI_CHAT_PRICE_IN", "3"); err != nil {
		return nil, err
	}
	if c.ChatPriceOutUSD, err = floatEnv("HI_CHAT_PRICE_OUT", "15"); err != nil {
		return nil, err
	}
	if c.LANCIDRs, err = prefixes("HI_LAN_CIDR", "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.0/8,::1/128,fc00::/7"); err != nil {
		return nil, err
	}
	if c.AuthentikProxies, err = prefixes("HI_AUTHENTIK_PROXY_IPS", ""); err != nil {
		return nil, err
	}
	if c.HomeNetworks, err = prefixes("HI_HOME_NETWORKS", ""); err != nil {
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

// SSOConfigured reports whether any SSO source is set up for the tunnel listener.
func (c *Config) SSOConfigured() bool {
	return c.CFTeamDomain != "" || len(c.AuthentikProxies) > 0 || c.OAuthClientSecret != ""
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

func floatEnv(key, def string) (float64, error) {
	v := env(key, def)
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return f, nil
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
