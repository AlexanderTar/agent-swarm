# Remote access to Swarm

Reach the Swarm board from a phone anywhere through a personal Railway
relay, from the LAN through `swarm.local`, or locally as today.

## Deployment record (TASK-873)

- Relay domain: filled on first deploy (see "Deploy the relay" below).
- Railway's client-IP header: filled on first deploy (`X-Real-IP` or the
  right-most `X-Forwarded-For` hop; the relay already handles both).

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

The bootstrap prints the `wss://…/_tunnel` URL at the end. Record the
domain and the client-IP header (see "Which client-IP header") in the
Deployment record above.

## Day-to-day use

One-time setup on the Mac (local only):

```sh
swarm auth set        # email + password (12+ chars); enables remote access
swarm tunnel enable --url 'wss://<domain>/_tunnel'
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
`X-Forwarded-For` hop, else the peer. To confirm which one Railway's
edge actually sets, tunnel a scratch daemon through the live relay and
compare the login rate-limit client (or a test echo of the received
`X-Forwarded-For`) against the phone's public IP: when only `X-Real-IP`
is present it wins; when only `X-Forwarded-For` arrives the right-most
hop wins. Record the winner in the Deployment record above. If neither
header arrives, remote access still works (peer fallback) but per-client
rate limiting degrades to the global cap; file a task for a coder, since
relay header handling is Go code.

## Troubleshooting

- `Swarm is offline` page on `/`: the Mac daemon is not connected.
  Check `swarm tunnel status` on the Mac and reconnect.
- `401` on `/_tunnel`: the daemon's `~/.swarm/run/tunnel.secret`
  differs from the service's `RELAY_SECRET`. Re-run the bootstrap
  (it re-syncs the variable) and `swarm tunnel disable && swarm
  tunnel enable`.
- `curl https://<domain>/healthz` must return 200 even with no tunnel;
  otherwise check `railway logs --service relay` and the service config
  path (`relay/railway.json`: Dockerfile builder, `/healthz` check).
- Local-only routes (`/api/bootstrap`, `/mcp`, `/hook/*`, `/api/tunnel`,
  `/api/lan`, …) answer 404 off the Mac by design, as does the daemon
  Bearer [REDACTED] presented remotely (401).
- Until credentials exist (`swarm auth set`), remote callers get 503
  telling them to run `swarm auth set` on the Mac.
