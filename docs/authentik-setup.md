# Authentik setup

How to set up Authentik and the Cloudflare Tunnel for the Hidden Isle app. Authentik's menus move a little between versions, so labels here may differ slightly from yours.

The examples use `isle.example.com` for the app, `auth.example.com` for Authentik, and `hidden-isle` for the app's container name. Replace them with yours.

## What each piece does

| Piece | Purpose | Needed? |
|---|---|---|
| **OAuth2/OpenID provider** (part A) | claude.ai logs you in through Authentik and gets a token for `/mcp`. The app checks the token's signature and that its email is the Seer's. | Yes, to use claude.ai. Claude Code can use an API token from Admin instead. |
| **Proxy provider** (part B) | Puts an Authentik login in front of the **web pages** on the tunnel, and tells the app who you are. | Only if you want the web UI while away from home. Players use it only at home (PIN), so they never need Authentik accounts. |

Do part A first. Without part B, the web pages on the tunnel just say "sign in required", which is fine if you only use the web UI at home.

---

## Part A: OAuth2 provider for claude.ai (MCP)

### A1. Create the provider and application
1. In Authentik go to **Admin interface → Applications → Applications → Create with provider** (the wizard).
2. **Application:**
   - Name: `Hidden Isle MCP`
   - Slug: `hidden-isle`. The slug becomes part of the issuer URL.
   - Launch URL: leave empty.
3. **Provider type:** **OAuth2/OpenID Provider**.
4. **Provider settings:**
   - **Authorization flow:** `default-provider-authorization-explicit-consent`. The implicit-consent flow also works and skips the "allow access?" screen.
   - **Client type:** **Confidential**.
   - **Client ID / Client Secret:** leave the generated values and copy both. You'll need them in A3 and A4.
   - **Redirect URIs:** add `https://claude.ai/api/mcp/auth_callback` as a *Strict* match.
     - Check this against the claude.ai connector help page. If Authentik later shows a "redirect URI mismatch" error, the URL in the error is the one to add.
   - **Signing Key:** choose a certificate, e.g. `authentik Self-signed Certificate`. **This is required.** With a signing key, tokens are signed RS256 JWTs that the app can verify through Authentik's public keys. Without one, they're signed with the client secret, and the app can't verify them.
   - **Advanced protocol settings → Scopes:** make sure `openid`, `email` and `profile` are selected (the default authentik mappings).
   - **Access token validity:** the default is fine. claude.ai refreshes tokens with the refresh token.
5. Finish the wizard.

### A2. Only you may use it
Open the application → **Policy / Group / User Bindings** → **Bind existing policy/group/user** → **User** → your user. Anyone else is then refused at Authentik's login.

The app also checks that the token's email matches `HI_SEER_EMAIL`, so this is a second lock.

### A3. Tell the app
1. Open the provider's page and copy the **Issuer**, from its "OpenID Configuration Issuer" line.
   - It looks like `https://auth.example.com/application/o/hidden-isle/`.
   - Copy it exactly, trailing slash included. The app compares it character for character.
2. In the Portainer stack, set:
   ```yaml
   HI_PUBLIC_URL: https://isle.example.com
   HI_SEER_EMAIL: you@example.com                # the email on your Authentik user
   HI_OAUTH_ISSUER: https://auth.example.com/application/o/hidden-isle/
   HI_OAUTH_AUDIENCE: <client ID from A1>
   ```
3. Redeploy the stack.

### A4. Cloudflare Tunnel route for `/mcp`
claude.ai's servers must reach `/mcp` without a browser login. Send those paths **straight to the app** in the tunnel's public hostnames. Order matters: the more specific entries go first.

| Order | Hostname | Path | Service |
|---|---|---|---|
| 1 | `isle.example.com` | `^/mcp` | `http://hidden-isle:8081` |
| 2 | `isle.example.com` | `^/\.well-known/oauth-protected-resource` | `http://hidden-isle:8081` |
| 3 | `isle.example.com` | *(empty)* | See below |

What goes in the service column for row 3:
- **Without part B:** `http://hidden-isle:8081` as well. The web pages will just ask you to sign in.
- **With part B:** your Authentik server, e.g. `http://authentik-server:9000`.

`http://hidden-isle:8081` only works if cloudflared is on the same Docker network as the app. Otherwise use the host port from `compose.yaml`, e.g. `http://<unraid-ip>:8391`.

Check it from any machine:
```sh
curl -si https://isle.example.com/.well-known/oauth-protected-resource/mcp   # JSON naming your Authentik issuer
curl -si -X POST https://isle.example.com/mcp | head -3                       # 401 with a WWW-Authenticate header
```

### A5. Add the connector in claude.ai
1. Go to **Settings → Connectors → Add custom connector**.
2. Fill in:
   - Name: `The Hidden Isle`
   - URL: `https://isle.example.com/mcp`
   - **Advanced settings:** the OAuth **Client ID** and **Client Secret** from A1.
3. Press **Connect**. You'll be sent to Authentik: log in, allow access, and you're returned to claude.ai.
4. Test it: in a chat, ask Claude to run the Hidden Isle `whoami` tool. It should answer with your Seer name and the rules-data snapshot.

**If it fails,** send the container log and what claude.ai says. Common causes:

| Symptom | Likely fix |
|---|---|
| `token has no email claim` | In A1's scopes, make sure the `email` scope mapping is selected. |
| `issuer` or `audience` errors | Recheck `HI_OAUTH_ISSUER` (exact, trailing slash) and `HI_OAUTH_AUDIENCE` (the client ID). |
| `signing method` errors | No signing key is selected (A1, step 4). |
| `... is not the Seer` | The Authentik user's email differs from `HI_SEER_EMAIL`. |

---

## Part B (optional): proxy provider for remote web access

The web pages reach you like this: browser → cloudflared → Authentik's embedded outpost → app port 8081. Authentik handles the login and adds `X-authentik-email` to each request. The app trusts that header only from the addresses in `HI_AUTHENTIK_PROXY_IPS`.

### B1. Create the provider and application
1. Go to **Applications → Applications → Create with provider**.
2. **Application:** Name `Hidden Isle`, Slug `hidden-isle-web`.
3. **Provider type:** **Proxy Provider**, mode **Proxy**. In this mode the outpost forwards requests to the app itself, so no separate reverse proxy is needed.
4. **Provider settings:**
   - **External host:** `https://isle.example.com`
   - **Internal host:** `http://hidden-isle:8081`, or `http://<unraid-ip>:8391`
   - **Authorization flow:** your usual implicit-consent flow.
   - **Advanced protocol settings:**
     - **Unauthenticated Paths:** add these two lines. They're a safety net: the tunnel already sends these paths straight to the app (A4).
       ```
       ^/mcp
       ^/\.well-known/oauth-protected-resource
       ```
     - **Intercept header authentication:** turn **off**. Otherwise Authentik may try to handle `Authorization: Bearer` headers itself.
5. Finish the wizard.

### B2. Only you may use it
Bind **your user** to the application, as in A2. Players never go through Authentik.

### B3. Attach it to the outpost
Go to **Applications → Outposts**, edit **authentik Embedded Outpost**, and add `Hidden Isle` to its applications.

### B4. Point the tunnel's catch-all route at Authentik
Change row 3 from A4 to your Authentik server, e.g. `http://authentik-server:9000`. The embedded outpost recognizes `isle.example.com` by its hostname.

### B5. Tell the app which address to trust
`HI_AUTHENTIK_PROXY_IPS` must cover the address the **Authentik server container** connects to the app from. Choose one:
- **Exact IP (best):** give the Authentik server a fixed IP on a shared Docker network (`ipv4_address:` in its compose), and use that IP.
- **Subnet (simpler):** use a small Docker network that only Authentik, cloudflared and the app share, and put its subnet here, e.g. `172.30.0.0/24`. Any container on that network could claim to be you, so keep it private to those three.

Redeploy the app. Then, from outside your home network, open `https://isle.example.com`, log in through Authentik, and you should land on the app signed in as the Seer.

---

## Summary of app settings

```yaml
HI_PUBLIC_URL: https://isle.example.com
HI_SEER_EMAIL: you@example.com
HI_SEER_PASSWORD: ${HI_SEER_PASSWORD}          # for signing in at home
HI_OAUTH_ISSUER: https://auth.example.com/application/o/hidden-isle/   # part A
HI_OAUTH_AUDIENCE: ${HI_OAUTH_CLIENT_ID}                               # part A
HI_AUTHENTIK_PROXY_IPS: 172.30.0.10                                    # part B only
```
