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

1. Create a stack from [`compose.yaml`](compose.yaml) and fill in the environment (table below). Set the secrets in Portainer's environment variables.
2. **Cloudflare Tunnel:** route `https://isle.example.com` to `http://<nas>:8081`, or to `http://hidden-isle:8081` if cloudflared shares a Docker network with the container.
3. **Cloudflare Access:** protect the hostname with an Access application, and copy its **AUD tag** into `HI_CF_AUD`. The app verifies the `Cf-Access-Jwt-Assertion` JWT itself, and doesn't trust an email header on its own.
4. **Let MCP through Access.** claude.ai's servers can't pass an Access login. Add a second Access application, with a **Bypass** policy, for these paths:
   - `isle.example.com/mcp`
   - `isle.example.com/.well-known/oauth-protected-resource`

   The app protects `/mcp` itself with bearer tokens.
5. Open `http://<nas>:8080` on your home network and sign in as the Seer (with the password from `HI_SEER_PASSWORD`). Then go to **Admin** and:
   - add players, each with a PIN and/or an SSO email;
   - check that the rules data synced.

### Using Authentik instead of (or as well as) Cloudflare Access
With Authentik forward auth in front of the app, set `HI_AUTHENTIK_PROXY_IPS` to the proxy's IP. The app trusts `X-authentik-email` **only** from those peers.

## Connect Claude (MCP)

**Claude Code:** in Admin, create an API token. The page then shows the command to run:
```sh
claude mcp add --transport http hidden-isle https://isle.example.com/mcp --header "Authorization: Bearer hi_…"
```

**claude.ai (custom connector, OAuth):**
1. **In Authentik, create an OAuth2/OpenID provider and application:**
   - confidential client;
   - a signing key, so access tokens are RS256 JWTs;
   - redirect URI `https://claude.ai/api/mcp/auth_callback` (check claude.ai's connector docs for the current callback URL);
   - scopes `openid email profile`.
2. **Point the app at it:**
   - set `HI_OAUTH_ISSUER` to the provider's issuer URL, e.g. `https://auth.example.com/application/o/hidden-isle/`;
   - set `HI_OAUTH_AUDIENCE` to the client ID.
3. **In claude.ai:** go to Settings → Connectors → Add custom connector. Use URL `https://isle.example.com/mcp`, and enter the client ID and secret under *Advanced settings*.

The app advertises Authentik in `/.well-known/oauth-protected-resource`. It accepts only tokens whose `email` claim belongs to the Seer.

> This OAuth path is the Phase 1 spike. If claude.ai and Authentik disagree on something (audience, claims), the fix goes in `internal/auth/jwt.go` and `internal/mcpsrv`.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `HI_DATA_DIR` | `/data` | Database, rules snapshots, backups, session key |
| `HI_LAN_ADDR` / `HI_TUNNEL_ADDR` | `:8080` / `:8081` | The two listeners |
| `HI_PUBLIC_URL` | | External URL (the tunnel hostname), used for MCP discovery |
| `HI_SEER_NAME` | `Seer` | The Seer's display name (first run) |
| `HI_SEER_EMAIL` | | The Seer's SSO email |
| `HI_SEER_PASSWORD` | | The Seer's LAN password (8+ characters). Applied at every start. |
| `HI_LAN_CIDR` | private ranges | PIN login is allowed only from these ranges |
| `HI_CF_TEAM_DOMAIN`, `HI_CF_AUD` | | Cloudflare Access team domain and application AUD |
| `HI_AUTHENTIK_PROXY_IPS` | | Peers allowed to send `X-authentik-email` |
| `HI_OAUTH_ISSUER`, `HI_OAUTH_AUDIENCE` | | OAuth provider for MCP bearer tokens (claude.ai) |
| `GAMEDATA_REPO` | | Rules repo: a git HTTPS URL or a local path |
| `GAMEDATA_REF` | `main` | Branch to follow |
| `GAMEDATA_TOKEN` | | GitHub fine-grained token, **Contents: read-only**, on the rules repo |
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
