# Remote access to Swarm

Reach the Swarm board from a phone anywhere through a personal Railway
relay, from the LAN through `swarm.local`, or locally as today.

## Deployment record (TASK-873)

- Relay domain: `relay-production-7911.up.railway.app` (project `Agent
  Swarm`, service `relay`, workspace **My Projects**).
- Tunnel URL: `wss://relay-production-7911.up.railway.app/_tunnel`
  (the value `scripts/relay-bootstrap.sh --tunnel-url` prints).
- Railway's client-IP header: **`X-Real-IP`**. Verified live: with the
  per-client login budget exhausted (5×401 then 429), a bad login
  carrying a spoofed `X-Real-IP: 1.2.3.4` still got 429, so the edge
  overwrote it with the real client IP; Railway's docs likewise list
  `X-Real-IP` (and not `X-Forwarded-For`) as the client-remote-IP
  header. The relay's first branch handles it.
- Icons: the `relay` service carries the Agent Swarm icon
  (`apps/menubar/Resources/AppIconLayers/Composed-1024.png`); Railway's
  GraphQL has no project icon field, so the project has none. The
  project description is "Agent Swarm remote access relay".

## Prereqs

- A personal Railway account with a workspace named **My Projects**. The
  deploy never touches any other workspace (in particular never Endurio).
- A workspace token at `~/.config/railway-personal/token` (used as
  `RAILWAY_API_TOKEN`). Only this token is ever used; a stored
  `railway login` session belonging to another account is ignored.
- The relay secret at `~/.swarm/run/tunnel.secret` (0600, `openssl rand
  -hex 32`). Created for you by the bootstrap when missing.
- Railway CLI and Docker (checked in pre-flight).

## Deploy the relay

```sh
scripts/relay-bootstrap.sh   # idempotent: project, service, secret, config, domain
make relay-deploy            # runs the bootstrap, then uploads and deploys
```

The project is named `Agent Swarm` (workspace **My Projects**) with a
service named `relay` carrying the Agent Swarm icon. The bootstrap finds
the project by name, so re-runs stay idempotent after the rename.

The bootstrap prints the `wss://…/_tunnel` URL at the end. Record the
domain and the client-IP header (see "Which client-IP header") in the
Deployment record above.

## Day-to-day use

One-time setup on the Mac (local only):

```sh
swarm auth set        # email + password (12+ chars); enables remote access
swarm tunnel enable --url 'wss://<domain>'   # base URL: no /_tunnel path
swarm lan enable      # optional: serve the LAN IP, advertise swarm.local
```

Status and doctor:

```sh
swarm auth status      # credential email + session count
swarm tunnel status    # state, URL, connected-since, reconnects
swarm lan status
swarm doctor           # includes tunnel and LAN rows
```

From the phone, open `https://<domain>`, log in with the email and
password, and use the board. Sign out from the header menu when done.
Remote sessions use an HttpOnly `swarm_session` cookie (Secure on
tunnel); cookie writes need a matching `Origin` header (CSRF guard).
After 5 bad logins per client per 15 minutes (20 per hour globally),
login answers 429 with `Retry-After`.

LAN: open `http://swarm.local:7777` (plain HTTP) from a device on the
same network while `swarm lan enable` is on.

Teardown:

```sh
swarm tunnel disable
swarm lan disable
swarm auth logout-all   # revoke every remote session
```

## Which client-IP header Railway sets

The relay picks the caller IP as `X-Real-IP`, else the right-most
`X-Forwarded-For` hop, else the peer. Live against this deployment the
winner is **`X-Real-IP`** (see the Deployment record above for the
experiment). If Railway ever stops sending it, remote access still works
(peer fallback) but per-client rate limiting degrades to the global cap;
file a task for a coder, since relay header handling is Go code.

## Troubleshooting

- `Swarm is offline` page on `/`: the Mac daemon is not connected.
  Check `swarm tunnel status` on the Mac and reconnect.
- `401` on `/_tunnel`: the daemon's `~/.swarm/run/tunnel.secret`
  differs from the service's `RELAY_SECRET`. Re-run the bootstrap
  (it re-syncs the variable) and `swarm tunnel disable && swarm
  tunnel enable`.
- `curl https://<domain>/healthz` must return 200 even with no tunnel;
  otherwise check `railway logs --service relay` and the service build
  settings (Dockerfile `relay/Dockerfile`, `/healthz` check, as the
  bootstrap applies from `relay/railway.json`).
- Local-only routes (`/api/bootstrap`, `/mcp`, `/hook/*`, `/api/tunnel`,
  `/api/lan`, …) answer 404 off the Mac by design, as does the daemon
  Bearer [REDACTED] presented remotely (401).
- Until credentials exist (`swarm auth set`), remote callers get 503
  telling them to run `swarm auth set` on the Mac.
