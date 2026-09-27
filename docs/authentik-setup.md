# Sign-in, Authentik and the tunnel

How to set up sign-in for the Hidden Isle app: Authentik (one OAuth2 provider) and the Cloudflare Tunnel. Authentik's menus move a little between versions, so labels may differ slightly.

The examples use `isle.example.com` for the app, `auth.example.com` for Authentik, and `<unraid-ip>` for the NAS. Replace them with yours.

## How sign-in works

| Who, where | Address | Signs in with |
|---|---|---|
| Anyone **at home** | `https://isle.example.com` (or `http://<unraid-ip>:8390`) | Name + PIN (the Seer: name + password) |
| The Seer, **away** | `https://isle.example.com` | **Sign in with Authentik** (the app sends you there and back) |
| **claude.ai** (MCP) | `https://isle.example.com/mcp` | An OAuth token from the same Authentik provider |
| **Claude Code** (MCP) | `https://isle.example.com/mcp` | An API token from Admin |

How the app knows you're at home:
- Everything comes through the tunnel, and Cloudflare tells the app each visitor's real address.
- The app asks Cloudflare every few minutes what the house's public IP is, so it follows IP changes by itself.
- A visitor from that address counts as home.
- Phones on the home Wi-Fi often use IPv6, so there's also a button in Admin that adds the house's IPv6 network (step A6).

Players never need Authentik accounts. Nothing sits in front of the app: no Authentik proxy and no Cloudflare Access.

---

## A1. Create the provider and application
1. In Authentik go to **Admin interface → Applications → Applications → Create with provider** (the wizard).
2. **Application:**
   - Name: `Hidden Isle`
   - Slug: `hidden-isle`. The slug becomes part of the issuer URL.
3. **Provider type:** **OAuth2/OpenID Provider**.
4. **Provider settings:**
   - **Authorization flow:** your implicit-consent flow (no "allow access?" screen), or explicit consent if you prefer.
   - **Client type:** **Confidential**. Copy the **Client ID** and **Client Secret**.
   - **Redirect URIs**, both as *Strict*:
     - `https://claude.ai/api/mcp/auth_callback`: for the claude.ai connector. If Authentik ever shows "redirect URI mismatch", add the URL from the error instead.
     - `https://isle.example.com/auth/callback`: for Sign in with Authentik in the browser.
   - **Signing Key:** choose a certificate, e.g. `authentik Self-signed Certificate`. **This is required.** Without it, tokens are signed with the client secret, and the app can't verify them.
   - **Scopes:** `openid`, `email`, `profile`.
5. Finish the wizard.

If you already created the provider for MCP, just add the second redirect URI.

## A2. Only you may use it
Open the application → **Policy / Group / User Bindings** → bind **your user**. Anyone else is refused at Authentik. The app also only accepts the email in `HI_SEER_EMAIL`, or emails you've given to players in Admin.

## A3. App settings (Portainer stack)
```yaml
HI_PUBLIC_URL: https://isle.example.com
HI_SEER_EMAIL: you@example.com                                     # the email on your Authentik user
HI_OAUTH_ISSUER: https://auth.example.com/application/o/hidden-isle/   # provider page → "OpenID Configuration Issuer", exactly
HI_OAUTH_AUDIENCE: ${HI_OAUTH_CLIENT_ID}                           # the Client ID
HI_OAUTH_CLIENT_SECRET: ${HI_OAUTH_CLIENT_SECRET}                  # the Client Secret: turns on Sign in with Authentik
```
- The app checks the issuer character for character, so copy it exactly, trailing slash included.
- Put the client ID and secret in the stack's environment variables, not in the compose file.

## A4. Cloudflare Tunnel
One public hostname:

| Hostname | Path | Service |
|---|---|---|
| `isle.example.com` | *(empty)* | `http://<unraid-ip>:8391`, or `http://hidden-isle:8081` if cloudflared shares a Docker network with the app |

If you earlier added separate `/mcp` routes, or pointed this hostname at Authentik, remove them: everything goes straight to the app now. **Don't** put Cloudflare Access in front of this hostname.

Check from any machine:
```sh
curl -si https://isle.example.com/.well-known/oauth-protected-resource/mcp   # JSON naming your Authentik issuer
curl -si -X POST https://isle.example.com/mcp | head -3                       # 401 with a WWW-Authenticate header
```

## A5. claude.ai connector
1. Go to **Settings → Connectors → Add custom connector**.
2. Use URL `https://isle.example.com/mcp`. Under *Advanced settings*, enter the Client ID and Client Secret.
3. Press **Connect**, log in to Authentik, and ask Claude to run the Hidden Isle `whoami` tool.

## A6. Check the home network
1. On your home Wi-Fi, open `https://isle.example.com/admin` on a **phone** and sign in with your password. If it asks for Authentik instead, see below.
2. **Admin → Home network** shows "This request: through the tunnel from …: counts as home".
3. If it says **doesn't count as home**, press **"I'm at home: treat this network as home"**. The phone was using IPv6, and this adds the house's IPv6 network (a /64). Once is enough, for all devices.

If the page asks for Authentik at home, sign in with Authentik once, then press the button in Admin. After that, PIN and password sign-in work at home.

**Troubleshooting:**

| Symptom | Fix |
|---|---|
| Players at home are asked to sign in with Authentik | Open Admin → Home network from their phone's network (step 3). "Home public address: unknown" means the container can't reach Cloudflare; check its internet access. |
| `token has no email claim` | Add the `email` scope mapping in A1. |
| `issuer` or `audience` errors | Recheck `HI_OAUTH_ISSUER` (exact) and `HI_OAUTH_AUDIENCE` (the client ID). |
| "Sign in with Authentik" missing | `HI_OAUTH_CLIENT_SECRET` isn't set. |
| `… isn't on the guest list` | The Authentik user's email isn't `HI_SEER_EMAIL`, and isn't on a player in Admin. |

## Alternatives (still supported)
- **Authentik proxy provider or forward auth in front of port 8081:** set `HI_AUTHENTIK_PROXY_IPS` to the outpost's address. The app then trusts its `X-authentik-email` header.
- **Cloudflare Access in front:** set `HI_CF_TEAM_DOMAIN` and `HI_CF_AUD` (Zero Trust → Access → Applications → your app → *Application Audience (AUD) Tag*). Add a Bypass policy for `/mcp` and `/.well-known/oauth-protected-resource`.
- **`HI_HOME_NETWORKS`:** fixed networks that always count as home, e.g. `2001:db8:1:2::/64`.
- **`HI_HOME_DETECT=off`:** turns off home detection; only the local address then allows PIN sign-in.
