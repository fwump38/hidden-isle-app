# Hidden Isle app

A self-hosted web app and MCP server for running a campaign of **The Hidden Isle** (a tarot RPG set in 1562). The app:
- gives players live character sheets from their phones;
- gives the Seer (the GM) campaign tracking and table tools;
- lets the Seer's Claude read and update the campaign over MCP.

It runs as a single Go binary in one container, with its state in SQLite.

This repo holds **no game text**. The rules data (classes, abilities, cards, tables, limits) comes from a separate rules repository, which the app syncs on a timer through that repo's `hidden-isle-data.yaml` manifest.

> **Status: Phase 1 (skeleton).** Working: sign-in (SSO and LAN PIN), the Seer's admin page, rules-data sync, and the MCP endpoint with `whoami`, `get_class` and `draw_cards`. Character sheets and campaign tracking are next.

## How it's put together

```
                LAN :8080 (PIN or SSO)              Tunnel :8081 (SSO required)
phones / TV ─────────────┐              cloudflared ────────────┐
                         ▼                                       ▼
              ┌─────────────── hidden-isle (one Go binary) ───────────────┐
              │ web UI (html/template + htmx + Bootstrap 5.3, dark)        │
              │ /mcp (MCP streamable HTTP, bearer tokens)                  │
              │ rules data: synced from git → validated snapshot           │
              └──────────────── SQLite (WAL) on the /data volume ──────────┘
```

**Two listeners.** cloudflared usually runs on the NAS, so tunnel traffic would also look like it comes from the home network. The app therefore listens twice:
- **`:8080` (LAN):** accepts a verified SSO identity, or a PIN or Seer-password login cookie. Only publish this port on your LAN.
- **`:8081` (tunnel):** point cloudflared here. It needs a verified SSO identity **on every request**, and ignores login cookies.

A request that reaches `:8080` carrying Cloudflare headers is treated as tunnel traffic, so a tunnel pointed at the wrong port can't expose PIN login.

**Who can do what:**
- The **Seer** can do everything.
- **Players** can edit only their own things, and never see Seer-only data (from Phase 2).
- **MCP** is Seer-only.

## Deploy (Portainer)

Players only use the app **at home**, signing in with a PIN, so they never need an SSO account. Only the Seer uses it remotely (web and MCP), through Authentik.

```
at home:   phone ──────────────────────────────────▶ :8080   name + PIN (players), password (Seer)
remote:    browser ─▶ cloudflared ─▶ Authentik outpost ─▶ :8081   Seer only
claude.ai: ─▶ cloudflared ─▶ Authentik outpost (unauthenticated path) ─▶ :8081/mcp   OAuth token from Authentik
```

1. **Create the stack** from [`compose.yaml`](compose.yaml) and fill in the environment (see the table below). Put the secrets in Portainer's environment variables.
2. **Rules repo deploy key:** the app makes its own SSH key on first start.
   1. Open **Admin → Rules data → Deploy key** and copy the key.
   2. On GitHub, open the rules repo → **Settings → Deploy keys → Add deploy key** and paste it. Leave write access **off**.
   3. Press **Sync now**.

   Deploy keys don't expire and can only read that one repo. GitHub's SSH host keys are pinned in the app.
3. **Authentik (remote sign-in for the Seer):** create a **Proxy provider** (forward auth or proxy mode) for `https://isle.example.com` whose upstream is the app's port **8081**.
   - Under *Unauthenticated Paths*, add `^/mcp` and `^/\.well-known/oauth-protected-resource`. The app protects `/mcp` itself with bearer tokens.
   - Bind the application to **only your user**. Players never go through it.
   - Set `HI_AUTHENTIK_PROXY_IPS` to the address the outpost connects to the app from (its IP, or its Docker network's subnet). The app ignores `X-authentik-email` from anyone else.
4. **Cloudflare Tunnel:** route `https://isle.example.com` to the Authentik outpost, not to the app directly.
5. **At home:** open `http://<nas>:8080`, sign in as the Seer with `HI_SEER_PASSWORD`, then **Admin → People → Add a player** with a name and a PIN. No email is needed.

**Security notes:**
- Never forward port 8080 to the internet. It accepts PINs.
- Port 8081 needs an SSO identity on every request and ignores PIN cookies.
- If a reverse proxy on your LAN also forwards to 8080 (for a nicer local hostname), that's fine: PIN login is still limited to `HI_LAN_CIDR`.

### Cloudflare Access instead of Authentik
Set `HI_CF_TEAM_DOMAIN` and `HI_CF_AUD` (Zero Trust → Access → Applications → your app → *Application Audience (AUD) Tag*), and route the tunnel straight to port 8081. For MCP, add a second Access application with a **Bypass** policy for `/mcp` and `/.well-known/oauth-protected-resource`.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `HI_DATA_DIR` | `/data` | Database, rules snapshots, backups, session key, deploy key (`secrets/`) |
| `HI_LAN_ADDR` / `HI_TUNNEL_ADDR` | `:8080` / `:8081` | The two listeners |
| `HI_PUBLIC_URL` | | External URL (the tunnel hostname), used for MCP discovery |
| `HI_SEER_NAME` | `Seer` | The Seer's display name (first run) |
| `HI_SEER_EMAIL` | | The Seer's SSO email |
| `HI_SEER_PASSWORD` | | The Seer's LAN password (8+ characters). Applied at every start. |
| `HI_LAN_CIDR` | private ranges | PIN login is allowed only from these ranges |
| `HI_CF_TEAM_DOMAIN`, `HI_CF_AUD` | | Cloudflare Access team domain and application AUD |
| `HI_AUTHENTIK_PROXY_IPS` | | Peers allowed to send `X-authentik-email` |
| `HI_OAUTH_ISSUER`, `HI_OAUTH_AUDIENCE` | | OAuth provider for MCP bearer tokens (claude.ai) |
| `GAMEDATA_REPO` | | Rules repo: an SSH URL (`git@github.com:owner/repo.git`, uses the app's deploy key), an HTTPS URL, or a local path |
| `GAMEDATA_REF` | `main` | Branch to follow |
| `GAMEDATA_TOKEN` | | For HTTPS URLs only: a fine-grained token with **Contents: read-only** on the rules repo. These expire, so the deploy key is preferred |
| `GAMEDATA_KNOWN_HOSTS` | | A known_hosts file for SSH hosts other than github.com (GitHub's keys are built in) |
| `GAMEDATA_INTERVAL` | `1h` | How often to sync |
| `HI_BACKUP_INTERVAL` | `24h` | Writes a consistent `/data/backup/hidden-isle.db` for your NAS backup (`0` = off) |

**Rules data sync:**
- A sync that fails validation keeps the last good snapshot, and Admin shows the error.
- A manifest `schema_version` newer than this build supports is refused. Update the app when that happens.
- Adventure text is Seer-only.

**Backups:** back up the `/data` volume as usual. Use `/data/backup/hidden-isle.db` rather than the live `hidden-isle.db`, because copying a live WAL database can produce a torn copy.

## Development

```sh
go test ./...                                                         # unit tests
HI_TEST_GAMEDATA=/path/to/The-Hidden-Isle go test ./internal/gamedata # + sync tests against the rules repo
HI_DATA_DIR=./data HI_SEER_PASSWORD=changeme1 GAMEDATA_REPO=/path/to/The-Hidden-Isle go run ./cmd/hidden-isle
scripts/vendor.sh                                                     # refresh Bootstrap / htmx
```

- **Layout:**
  - `cmd/hidden-isle`: entry point, listeners, Seer bootstrap
  - `internal/config`: environment variables
  - `internal/auth`: SSO, PIN login, sessions
  - `internal/db`: GORM models and migrations
  - `internal/gamedata`: sync and validation
  - `internal/cards`: draws
  - `internal/mcpsrv`: MCP
  - `internal/web`: UI
- **Schema changes:** add models to `internal/db/models.go`, where `AutoMigrate` applies additive changes. Put renames and data fixes in `migrations`.
- **CI:** gofmt, vet and race tests on every push. On pushes to `main` and on `v*` tags, it also builds a multi-arch image (amd64 and arm64) and pushes it to `ghcr.io/fwump38/hidden-isle-app`.
