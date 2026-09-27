package config

import (
	"testing"
	"time"
)

func TestLoadReadsEveryVariable(t *testing.T) {
	for k, v := range map[string]string{
		"HI_DATA_DIR": "/d", "HI_LAN_ADDR": ":1", "HI_TUNNEL_ADDR": ":2", "HI_PUBLIC_URL": "https://x.example/",
		"HI_SEER_EMAIL": " Me@Example.com ", "HI_SEER_NAME": "S", "HI_SEER_PASSWORD": "pw",
		"HI_LAN_CIDR": "192.168.1.0/24", "HI_AUTHENTIK_PROXY_IPS": "10.0.0.5", "HI_HOME_NETWORKS": "2001:db8::/64",
		"HI_CF_TEAM_DOMAIN": "https://team.cloudflareaccess.com/", "HI_CF_AUD": "aud",
		"HI_OAUTH_ISSUER": "https://auth/", "HI_OAUTH_AUDIENCE": "cid", "HI_OAUTH_CLIENT_SECRET": "sec",
		"GAMEDATA_REPO": "git@github.com:a/b.git", "GAMEDATA_REF": "v1.2.0", "GAMEDATA_TOKEN": "tok",
		"GAMEDATA_KNOWN_HOSTS": "/kh", "GAMEDATA_INTERVAL": "30m", "HI_BACKUP_INTERVAL": "0", "HI_HOME_DETECT": "off",
	} {
		t.Setenv(k, v)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]bool{
		"DataDir": c.DataDir == "/d", "LANAddr": c.LANAddr == ":1", "TunnelAddr": c.TunnelAddr == ":2",
		"PublicURL": c.PublicURL == "https://x.example", "SeerEmail": c.SeerEmail == "me@example.com",
		"SeerName": c.SeerName == "S", "SeerPassword": c.SeerPassword == "pw",
		"LANCIDRs": len(c.LANCIDRs) == 1, "AuthentikProxies": len(c.AuthentikProxies) == 1, "HomeNetworks": len(c.HomeNetworks) == 1,
		"CFTeamDomain": c.CFTeamDomain == "team.cloudflareaccess.com", "CFAudience": c.CFAudience == "aud",
		"OAuthIssuer": c.OAuthIssuer == "https://auth/", "OAuthAudience": c.OAuthAudience == "cid", "OAuthClientSecret": c.OAuthClientSecret == "sec",
		"GameDataRepo": c.GameDataRepo == "git@github.com:a/b.git", "GameDataRef": c.GameDataRef == "v1.2.0",
		"GameDataToken": c.GameDataToken == "tok", "GameDataKnownHosts": c.GameDataKnownHosts == "/kh",
		"GameDataInterval": c.GameDataInterval == 30*time.Minute, "BackupInterval": c.BackupInterval == 0, "HomeDetect": !c.HomeDetect,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s not loaded from the environment", name)
		}
	}
}

func TestDefaults(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.GameDataRef != "release" || !c.HomeDetect || c.DataDir != "/data" || c.LANAddr != ":8080" {
		t.Errorf("defaults: ref=%q homeDetect=%v dataDir=%q lan=%q", c.GameDataRef, c.HomeDetect, c.DataDir, c.LANAddr)
	}
}
