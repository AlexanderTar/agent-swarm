#!/usr/bin/env bash
#
# Bootstraps the swarm-relay project on personal Railway.
#
# Idempotent: every step finds the existing resource first and only creates
# what is missing. Safe to re-run at any time.
#
#   - Reads RAILWAY_API_TOKEN from ~/.config/railway-personal/token.
#   - Uses workspace "My Projects" only; refuses to touch any other
#     workspace (in particular never Endurio).
#   - Finds or creates project `Agent Swarm` (CLI first, GraphQL fallback).
#   - Finds or creates service `relay` in the production environment.
#   - Generates ~/.swarm/run/tunnel.secret (0600) when missing and sets it
#     as the service's RELAY_SECRET (via stdin, never on argv).
#   - Applies the relay/railway.json build settings (Dockerfile path,
#     /healthz check, restart policy) as service instance settings
#     (Railway no longer accepts a config-file pointer).
#   - Creates the generated Railway domain and prints the wss:// tunnel URL.
#
# Query modes (read-only, for Make and docs tooling):
#   scripts/relay-bootstrap.sh --project-id   print the project id
#   scripts/relay-bootstrap.sh --domain        print the service domain
#   scripts/relay-bootstrap.sh --tunnel-url    print wss://<domain>/_tunnel
#
# Overrides (defaults match the paths above):
#   RAILWAY_TOKEN_FILE, TUNNEL_SECRET_FILE, RELAY_PROJECT_NAME,
#   RELAY_SERVICE_NAME, RAILWAY_WORKSPACE, RAILWAY_ENVIRONMENT.
#
# Never prints secrets: logs (stderr) carry names and ids only.
set -euo pipefail

PROJECT_NAME="${RELAY_PROJECT_NAME:-Agent Swarm}"
PROJECT_DESCRIPTION="${RELAY_PROJECT_DESCRIPTION:-Agent Swarm remote access relay}"
SERVICE_NAME="${RELAY_SERVICE_NAME:-relay}"
ICON_URL="${RELAY_ICON_URL:-https://raw.githubusercontent.com/AlexanderTar/swarmrun/main/apps/menubar/Resources/AppIconLayers/Composed-1024.png}"
WORKSPACE_NAME="${RAILWAY_WORKSPACE:-My Projects}"
ENVIRONMENT_NAME="${RAILWAY_ENVIRONMENT:-production}"
TOKEN_FILE="${RAILWAY_TOKEN_FILE:-$HOME/.config/railway-personal/token}"
SECRET_FILE="${TUNNEL_SECRET_FILE:-$HOME/.swarm/run/tunnel.secret}"
CONFIG_PATH="relay/railway.json"

log() { printf 'relay-bootstrap: %s\n' "$*" >&2; }
die() { printf 'relay-bootstrap: error: %s\n' "$*" >&2; exit 1; }

MODE="apply"
for arg in "$@"; do
  case "$arg" in
    --project-id|--domain|--tunnel-url) MODE="${arg#--}" ;;
    -h|--help)
      sed -n '2,/^set /p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) die "unknown argument: $arg" ;;
  esac
done

[ -f "$TOKEN_FILE" ] || die "token file not found: $TOKEN_FILE (personal Railway token)"
RAILWAY_API_TOKEN="$(cat "$TOKEN_FILE")"
[ -n "$RAILWAY_API_TOKEN" ] || die "token file is empty: $TOKEN_FILE"
export RAILWAY_API_TOKEN

# gql DOC: run a GraphQL document through the Railway CLI, die on .errors,
# print .data. Response bodies on this path carry ids and names, no secrets.
gql() {
  local doc="$1" out err
  err="$(mktemp)"
  if ! out="$(railway api "$doc" 2>"$err")"; then
    if ! printf '%s' "$out" | jq -e . >/dev/null 2>&1; then
      die "railway api failed: $(head -n 2 "$err" | tr '\n' ' ')"
    fi
  fi
  rm -f "$err"
  if printf '%s' "$out" | jq -e '.errors and (.errors | length > 0)' >/dev/null; then
    die "Railway error: $(printf '%s' "$out" | jq -r '[.errors[].message] | join("; ")')"
  fi
  printf '%s' "$out" | jq '.data'
}

# 1. Resolve the workspace id. The token must see "My Projects"; anything
# else (including a token that only sees Endurio) is a hard refusal.
WS_ID="$(gql 'query { apiToken { workspaces { id name } } }' \
  | jq -r --arg n "$WORKSPACE_NAME" '.apiToken.workspaces[] | select(.name == $n) | .id' \
  | head -n 1)"
[ -n "$WS_ID" ] || die "workspace '$WORKSPACE_NAME' is not visible to this token; refusing to use any other workspace"

# 2. Find or create the project: CLI first, GraphQL fallback.
find_project_cli() {
  railway list --json 2>/dev/null \
    | jq -r --arg n "$PROJECT_NAME" '.. | objects | select(.name == $n and (.id | type == "string")) | .id' 2>/dev/null \
    | head -n 1
}
PROJECT_ID="$(find_project_cli || true)"
if [ -z "$PROJECT_ID" ]; then
  PROJECT_ID="$(gql 'query { projects(first: 100) { edges { node { id name } } } }' \
    | jq -r --arg n "$PROJECT_NAME" '.projects.edges[].node | select(.name == $n) | .id' \
    | head -n 1)"
fi
if [ -z "$PROJECT_ID" ] && [ "$MODE" = "apply" ]; then
  log "creating project '$PROJECT_NAME' in workspace '$WORKSPACE_NAME'"
  doc="$(jq -n --arg ws "$WS_ID" --arg n "$PROJECT_NAME" \
    -r '"mutation { projectCreate(input: {workspaceId: \($ws|@json), name: \($n|@json), defaultEnvironmentName: \"production\"}) { id name } }"')"
  PROJECT_ID="$(gql "$doc" | jq -r '.projectCreate.id')"
fi
[ -n "$PROJECT_ID" ] || die "project '$PROJECT_NAME' not found; run without --${MODE} to create it"
log "project '$PROJECT_NAME': $PROJECT_ID"

if [ "$MODE" = "project-id" ]; then
  printf '%s\n' "$PROJECT_ID"
  exit 0
fi

# 2b. Project description (read-compare-set; projects have no icon field).
if [ "$MODE" = "apply" ]; then
  HAVE_DESC="$(gql "$(jq -n --arg pid "$PROJECT_ID" -r \
      '"query { project(id: \($pid|@json)) { description } }"')" | jq -r '.project.description // empty')"
  if [ "$HAVE_DESC" != "$PROJECT_DESCRIPTION" ]; then
    doc="$(jq -n --arg pid "$PROJECT_ID" --arg desc "$PROJECT_DESCRIPTION" -r \
      '"mutation { projectUpdate(id: \($pid|@json), input: {description: \($desc|@json)}) { id } }"')"
    gql "$doc" | jq -e '.projectUpdate.id' >/dev/null || die "projectUpdate description failed"
    log "project description set"
  fi
fi

# 3. Resolve the environment id (defaultEnvironmentName "production").
ENV_ID="$(gql "$(jq -n --arg pid "$PROJECT_ID" -r \
    '"query { project(id: \($pid|@json)) { environments(first: 20) { edges { node { id name } } } } }"')" \
  | jq -r --arg n "$ENVIRONMENT_NAME" '.project.environments.edges[].node | select(.name == $n) | .id' \
  | head -n 1)"
[ -n "$ENV_ID" ] || die "environment '$ENVIRONMENT_NAME' not found in project '$PROJECT_NAME'"
log "environment '$ENVIRONMENT_NAME': $ENV_ID"

# 4. Find or create the service: CLI first, GraphQL fallback.
find_service_cli() {
  railway service list --json --project "$PROJECT_ID" --environment "$ENV_ID" 2>/dev/null \
    | jq -r --arg n "$SERVICE_NAME" '.. | objects | select(.name == $n and (.id | type == "string")) | .id' 2>/dev/null \
    | head -n 1
}
SERVICE_ID="$(find_service_cli || true)"
if [ -z "$SERVICE_ID" ]; then
  SERVICE_ID="$(gql "$(jq -n --arg pid "$PROJECT_ID" -r \
      '"query { project(id: \($pid|@json)) { services(first: 50) { edges { node { id name } } } } }"')" \
    | jq -r --arg n "$SERVICE_NAME" '.project.services.edges[].node | select(.name == $n) | .id' \
    | head -n 1)"
fi
if [ -z "$SERVICE_ID" ] && [ "$MODE" = "apply" ]; then
  log "creating service '$SERVICE_NAME'"
  doc="$(jq -n --arg pid "$PROJECT_ID" --arg eid "$ENV_ID" --arg n "$SERVICE_NAME" -r \
    '"mutation { serviceCreate(input: {projectId: \($pid|@json), environmentId: \($eid|@json), name: \($n|@json)}) { id name } }"')"
  SERVICE_ID="$(gql "$doc" | jq -r '.serviceCreate.id')"
fi
[ -n "$SERVICE_ID" ] || die "service '$SERVICE_NAME' not found; run without --${MODE} to create it"
log "service '$SERVICE_NAME': $SERVICE_ID"

# 5. Relay secret: generate (0600) when missing, validate the format the
# relay requires (openssl rand -hex 32: 64 hex chars). Value never leaves
# this shell except into the file and the Railway variable.
if [ ! -f "$SECRET_FILE" ]; then
  [ "$MODE" = "apply" ] || die "secret file missing: $SECRET_FILE"
  log "generating relay secret"
  mkdir -p "$(dirname "$SECRET_FILE")"
  openssl rand -hex 32 >"$SECRET_FILE"
  chmod 0600 "$SECRET_FILE"
fi
RELAY_SECRET="$(cat "$SECRET_FILE")"
case "$RELAY_SECRET" in
  *[!0-9a-fA-F]*|"") die "secret in $SECRET_FILE is not hex" ;;
esac
[ "${#RELAY_SECRET}" -ge 64 ] || die "secret in $SECRET_FILE is shorter than 64 hex chars"
chmod 0600 "$SECRET_FILE"

# 6. Set RELAY_SECRET on the service (skipDeploys: the deploy itself runs
# via `make relay-deploy`). CLI first with the value on stdin, GraphQL
# variableUpsert fallback.
set_variable_cli() {
  printf '%s' "$RELAY_SECRET" | railway variable set RELAY_SECRET --stdin \
    --service "$SERVICE_NAME" --project "$PROJECT_ID" --environment "$ENV_ID" \
    --skip-deploys --json 2>/dev/null | jq -e . >/dev/null 2>&1
}
if ! set_variable_cli; then
  # The CLI needs a linked project or a user session; the workspace token
  # path is GraphQL. Value travels inside a JSON string built by jq.
  doc="$(jq -n --arg pid "$PROJECT_ID" --arg eid "$ENV_ID" --arg sid "$SERVICE_ID" \
    --arg val "$RELAY_SECRET" -r \
    '"mutation { variableUpsert(input: {projectId: \($pid|@json), environmentId: \($eid|@json), serviceId: \($sid|@json), name: \"RELAY_SECRET\", value: \($val|@json), skipDeploys: true}) }")')"
  gql "$doc" | jq -e '.variableUpsert == true' >/dev/null \
    || die "variableUpsert RELAY_SECRET failed"
fi
log "RELAY_SECRET set on service '$SERVICE_NAME'"

# 7. Build settings mirroring relay/railway.json (DOCKERFILE builder,
# relay/Dockerfile, /healthz check, ALWAYS restart, no overlap).
# Railway deprecated config-as-code files, so railwayConfigFile is refused;
# the same values go in as instance settings instead.
doc="$(jq -n --arg sid "$SERVICE_ID" --arg eid "$ENV_ID" -r \
  '"mutation { serviceInstanceUpdate(serviceId: \($sid|@json), environmentId: \($eid|@json), input: {dockerfilePath: \"relay/Dockerfile\", healthcheckPath: \"/healthz\", restartPolicyType: ALWAYS, overlapSeconds: 0, numReplicas: 1}) }"')"
gql "$doc" | jq -e '.serviceInstanceUpdate == true' >/dev/null \
  || die "serviceInstanceUpdate build settings failed"
HAVE="$(gql "$(jq -n --arg sid "$SERVICE_ID" -r \
    '"query { service(id: \($sid|@json)) { serviceInstances { edges { node { environmentId dockerfilePath healthcheckPath } } } } }"')" \
  | jq -r --arg eid "$ENV_ID" '.service.serviceInstances.edges[].node | select(.environmentId == $eid) | "\(.dockerfilePath) \(.healthcheckPath)"')"
[ "$HAVE" = "relay/Dockerfile /healthz" ] || die "build settings read back as '$HAVE'"
log "service config: $CONFIG_PATH (as instance settings)"

# 7b. Service icon. Projects have no icon field in the API, so only the
# service gets one. Read-compare-set keeps re-runs free of writes.
HAVE_ICON="$(gql "$(jq -n --arg sid "$SERVICE_ID" -r \
    '"query { service(id: \($sid|@json)) { icon } }"')" | jq -r '.service.icon // empty')"
if [ "$HAVE_ICON" != "$ICON_URL" ]; then
  doc="$(jq -n --arg sid "$SERVICE_ID" --arg icon "$ICON_URL" -r \
    '"mutation { serviceUpdate(id: \($sid|@json), input: {icon: \($icon|@json)}) { id } }"')"
  gql "$doc" | jq -e '.serviceUpdate.id' >/dev/null || die "serviceUpdate icon failed"
  log "service icon set"
fi

# 8. Generated domain: CLI first, GraphQL serviceDomainCreate fallback.
find_domain_cli() {
  railway domain list --service "$SERVICE_NAME" --project "$PROJECT_ID" \
    --environment "$ENV_ID" --json 2>/dev/null \
    | jq -r '.. | objects | select(.domain | type == "string") | .domain' 2>/dev/null \
    | grep -E '\.up\.railway\.app$' | head -n 1 || true
}
DOMAIN="$(find_domain_cli || true)"
if [ -z "$DOMAIN" ]; then
  DOMAIN="$(gql "$(jq -n --arg pid "$PROJECT_ID" --arg eid "$ENV_ID" --arg sid "$SERVICE_ID" -r \
      '"query { domains(projectId: \($pid|@json), environmentId: \($eid|@json), serviceId: \($sid|@json)) { serviceDomains { domain } } }"')" \
    | jq -r '.domains.serviceDomains[].domain' 2>/dev/null | head -n 1 || true)"
fi
if [ -z "$DOMAIN" ] && [ "$MODE" = "apply" ]; then
  if ! DOMAIN="$(railway domain --service "$SERVICE_NAME" --project "$PROJECT_ID" \
      --environment "$ENV_ID" --json 2>/dev/null | jq -r '.domain // empty' 2>/dev/null)"; then
    DOMAIN=""
  fi
  if [ -z "$DOMAIN" ]; then
    doc="$(jq -n --arg eid "$ENV_ID" --arg sid "$SERVICE_ID" -r \
      '"mutation { serviceDomainCreate(input: {environmentId: \($eid|@json), serviceId: \($sid|@json)}) { domain } }"')"
    DOMAIN="$(gql "$doc" | jq -r '.serviceDomainCreate.domain')"
  fi
fi
# serviceDomainCreate returns the domain with a scheme prefix; the CLI and
# the domains query return it bare. Normalize to the bare host either way.
DOMAIN="${DOMAIN#https://}"
DOMAIN="${DOMAIN#http://}"
DOMAIN="${DOMAIN%%/*}"
case "$DOMAIN" in
  *.up.railway.app) ;;
  *) die "no *.up.railway.app domain on service '$SERVICE_NAME'" ;;
esac
log "domain: $DOMAIN"

TUNNEL_URL="wss://$DOMAIN/_tunnel"
case "$MODE" in
  domain) printf '%s\n' "$DOMAIN" ;;
  tunnel-url) printf '%s\n' "$TUNNEL_URL" ;;
  *) log "tunnel URL: $TUNNEL_URL" ;;
esac
