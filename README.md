# Hidden Isle app

A self-hosted web app and MCP server for running a campaign of **The Hidden Isle** (a tarot RPG set in 1562). The app:
- gives players live character sheets from their phones;
- gives the Seer (the GM) campaign tracking and table tools;
- lets the Seer's Claude read and update the campaign over MCP.

It runs as a single Go binary in one container, with its state in SQLite.

This repo holds **no game text**. The rules data (classes, abilities, cards, tables, limits) comes from a separate rules repository, which the app syncs on a timer through that repo's `hidden-isle-data.yaml` manifest.

> **Status: Phase 4 (table tools).** Working:
> - sign-in (PIN at home, Authentik away);
> - campaigns and player-owned Agents with live character sheets;
> - clocks, adversaries, territories, sessions, house rulings, Seer notes and journals;
> - a change log with undo; export and print; rules data by release;
> - an MCP server with tools for all of this, rules search, and the plugin's skills as prompts;
> - a challenge helper on the sheet (card counts and resolution, pp. 15-19), and the same as
>   MCP tools;
> - an oracle page (fate questions, random events, NPCs, complications, mission types) and
>   matching MCP tools;
> - live updates over SSE, a Seer dashboard for the table, a secret-link TV view, and
>   handouts pushed to players' phones;
> - a step-by-step creation wizard (pp. 40-42);
> - downtime a player plans and submits, and the Seer approves before it touches the sheet.
>
> Next: an in-app Claude chat for players (character creation help, rules Q&A), on the Seer's
> own API budget and locked to what each player may see.

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

> Step-by-step sign-in setup (Authentik, the tunnel, claude.ai, the home network): [docs/authentik-setup.md](docs/authentik-setup.md).

Everyone uses `https://<your domain>` through the Cloudflare Tunnel:
- **At home:** the app sees your house's public IP and offers name + PIN, so players need no accounts.
- **Away:** only the Seer gets in, via *Sign in with Authentik*.
- **claude.ai:** reaches `/mcp` with an OAuth token from the same Authentik provider.

`http://<nas>:8390` also works at home, as a fallback when the internet is down.

1. **Create the folder:** `mkdir -p /mnt/user/appdata/hidden-isle && chown 99:100 /mnt/user/appdata/hidden-isle`.
2. **Create the stack** from [`compose.yaml`](compose.yaml). Put the secrets in the stack's environment variables: `HI_SEER_PASSWORD`, `HI_OAUTH_CLIENT_ID` and `HI_OAUTH_CLIENT_SECRET`.
3. **Set up Authentik and the tunnel:** one OAuth2 provider with two redirect URIs, and one tunnel route to port 8391. See the guide.
4. **Rules data:**
   1. Open **Admin → Rules data → Deploy key** and add it to the rules repo as a read-only deploy key.
   2. Press **Check now**. The app installs the newest release and pins it; Admin shows when a newer one exists.
5. **Players:** **Admin → People → Add a player** with a name and a PIN (no email needed), then add them to a campaign under its settings.
6. **Home network:** on the home Wi-Fi, open Admin from a phone. If "This request" doesn't count as home, press the button (see guide A6).

**Security notes:**
- Never forward the LAN port (8390) to the internet.
- The tunnel port (8391) must only be reachable through cloudflared, because the app trusts the `Cf-Connecting-IP` header there.
- PIN sessions only work at home. Authentik sessions work anywhere.

## Connect Claude (MCP)

Setup is in [docs/authentik-setup.md](docs/authentik-setup.md): claude.ai uses OAuth through Authentik, and Claude Code uses an API token from Admin:
```sh
claude mcp add --transport http hidden-isle https://isle.example.com/mcp --header "Authorization: Bearer hi_…"
```

**What Claude gets.** Only the Seer can use MCP. Everything runs through the same checks as the web UI: limits from the rules data, permissions, and the change log (as "<Seer> (Claude)"). Every change can be undone.

| Group | Tools |
|---|---|
| Campaign state | `list_campaigns`, `get_campaign`, `get_agent`, `list_records`, `get_record`, `get_log`, `list_entries` |
| Changes | `create_campaign`, `create_agent`, `move_agent`, `create_record`, `update_record`, `delete_record`, `undo_change`, `write_entry` |
| At the table | `add_harm`, `heal`, `award_xp`, `tick_clock`, `advance_adversary`, `drift_contacts`, `draw_cards` |
| Rules | `search_rules`, `read_rules` (full text with page cites; adventures Seer-only), `get_class`, `lookup_card`, `get_table` |

- **Prompts:** the rules repo's plugin skills are served as prompts (`create-agent`, `challenge`, `downtime`, `wrap-up`, …), so a claude.ai chat with only this connector has them.
- **Refreshing:** prompts and the search index refresh when you install a new rules release.

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
| `HI_OAUTH_ISSUER`, `HI_OAUTH_AUDIENCE` | | Authentik OAuth2 provider: its issuer and client ID (claude.ai MCP tokens, browser sign-in) |
| `HI_OAUTH_CLIENT_SECRET` | | Turns on *Sign in with Authentik* in the browser |
| `HI_HOME_DETECT` | `on` | Learn the house's public IP from Cloudflare; tunnel requests from it get PIN sign-in |
| `HI_HOME_NETWORKS` | | Extra networks that always count as home (e.g. an IPv6 /64) |
| `GAMEDATA_REPO` | | Rules repo: an SSH URL (`git@github.com:owner/repo.git`, uses the app's deploy key), an HTTPS URL, or a local path |
| `GAMEDATA_REF` | `release` | `release` = pinned releases updated from Admin; or a fixed tag (`v1.2.0`); or a branch (`main`) |
| `GAMEDATA_TOKEN` | | For HTTPS URLs only: a fine-grained token with **Contents: read-only** on the rules repo. These expire, so the deploy key is preferred |
| `GAMEDATA_KNOWN_HOSTS` | | A known_hosts file for SSH hosts other than github.com (GitHub's keys are built in) |
| `GAMEDATA_INTERVAL` | `1h` | How often to check for new releases |
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
