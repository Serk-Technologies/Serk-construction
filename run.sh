#!/usr/bin/env bash
# SERK Construction OS: one command to build, start and seed the system and the demo.
#
#   ./run.sh               start everything (builds what changed), load the demo company
#                          and its demo data, then print where to open it and how to sign in
#   ./run.sh reset         ERASE the demo database, then start from scratch (asks first; --yes skips the question)
#   ./run.sh stop          stop the containers (the data is kept)
#   ./run.sh status        what is running, and on which ports
#   ./run.sh logs          follow the API log
#   ./run.sh data          (re)load the demo data only (safe to repeat; it skips what is loaded)
#   ./run.sh pin <position>   show the one-time e-mail sign-in PIN of a demo position, e.g.  ./run.sh pin cfo
#   ./run.sh help
#
# Needs: Docker with the Compose plugin and curl. Node 18+ loads the demo data (optional); Go shows PINs (optional).
# Development only: published well-known secrets, synthetic data. Nothing here is for production.
set -euo pipefail
cd "$(dirname "$0")"

PROJECT=${SERK_PROJECT:-serk}
export SERK_PROJECT=$PROJECT
COMPOSE="docker compose -p $PROJECT -f deploy/docker-compose.dev.yml"
TENANT=11111111-1111-4111-8111-111111111111
PASSPHRASE=dev-only-staging-passphrase-0123456789

say()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
warn() { printf '\033[33m%s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31m%s\033[0m\n' "$*" >&2; exit 1; }

need_docker() {
  command -v docker >/dev/null || die "Docker is not installed."
  docker compose version >/dev/null 2>&1 || die "The Docker Compose plugin is not installed."
  command -v curl >/dev/null || die "curl is not installed."
}

# A port is free when nothing accepts a connection on it.
port_free() { ! (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }
# The host port a running container of this project already publishes (so a restart keeps its ports).
published() { docker port "${PROJECT}-$1-1" "$2" 2>/dev/null | head -n1 | sed 's/.*://'; }
pick_port() { local p; for p in "$@"; do if port_free "$p"; then echo "$p"; return 0; fi; done; die "No free port among: $*"; }

choose_ports() {
  if [ -z "${SERK_DB_PORT:-}" ]; then
    SERK_DB_PORT=$(published db 5432 || true)
    [ -n "$SERK_DB_PORT" ] || SERK_DB_PORT=$(pick_port 5432 5434 55432 55433 55434)
  fi
  if [ -z "${SERK_API_PORT:-}" ]; then
    SERK_API_PORT=$(published api 8080 || true)
    [ -n "$SERK_API_PORT" ] || SERK_API_PORT=$(pick_port 8080 8081 8082 8090 8091)
  fi
  export SERK_DB_PORT SERK_API_PORT
  BASE="http://127.0.0.1:${SERK_API_PORT}"
}

wait_ready() {
  local i
  for i in $(seq 1 90); do
    if curl -fsS "$BASE/readyz" >/dev/null 2>&1; then return 0; fi
    sleep 2
  done
  warn "The API did not become ready in 3 minutes. Look at:  ./run.sh logs"
  return 1
}

# The build containers sometimes cannot reach the Go module proxy (no network, or only IPv6 routes).
# With Go on the host, the modules are vendored here and the build uses them (see deploy/Dockerfile).
vendor_modules() {
  command -v go >/dev/null || return 0
  if [ ! -f vendor/modules.txt ] || [ go.sum -nt vendor/modules.txt ] || [ go.mod -nt vendor/modules.txt ]; then
    say "Preparing the Go modules for the build (vendoring)"
    go mod vendor || warn "go mod vendor failed; the build will try to download the modules itself."
  fi
}

start() {
  need_docker
  choose_ports
  vendor_modules
  say "Building and starting the system (first build takes a few minutes)"
  if ! $COMPOSE up -d --build; then
    warn ""
    warn "The system did not start. If the message mentions the migration, the database was made by an older"
    warn "version of the code (for example before the migrations were renumbered). Start from scratch with:"
    warn "    ./run.sh reset"
    exit 1
  fi
  wait_ready || exit 1
  say "Loading the demo company"
  scripts/dev-seed.sh
  wait_ready || exit 1
  demo_data || warn "The demo data was not loaded completely (the system itself is running). Retry with:  ./run.sh data"
  summary
}

demo_data() {
  if ! command -v node >/dev/null; then
    warn "Node.js is not installed, so the extra demo data (payroll run, timesheets, incidents, documents...) is skipped."
    return 0
  fi
  if [ ! -f scripts/demo-data.mjs ]; then
    warn "scripts/demo-data.mjs is not there yet: only the base demo company (projects, suppliers, customer, policies, people) is loaded."
    return 0
  fi
  say "Loading demo data"
  SERK_URL="$BASE" SERK_STAGING_PASSPHRASE="$PASSPHRASE" node scripts/demo-data.mjs
}

summary() {
  cat <<EOF

$(printf '\033[1m')SERK Construction is running$(printf '\033[0m')

  Open        $BASE/console/
  Database    127.0.0.1:${SERK_DB_PORT}   (project "$PROJECT")

  Sign in, either way:
   1. Test persona   (best for demonstrations, one person per duty)
        choose "Demonstration: sign in as a ready-made test persona", pick the person,
        passphrase:   $PASSPHRASE
   2. E-mail + PIN   (a real-looking sign-in; every position has one)
        e-mail  <position>@serk-demo.serk.example   for example  cfo@serk-demo.serk.example
        PIN     ./run.sh pin cfo        (a one-time PIN; the system asks for a new one at the first sign-in)

  Users and roles:  sign in as the security admin (persona "Security admin"), then account menu -> People & access.

  Other commands:   ./run.sh stop | status | logs | data | reset | help
EOF
}

reset() {
  need_docker
  say "This ERASES the demo database (Docker volume ${PROJECT}_pgdata) and everything in it."
  if [ "${1:-}" != "--yes" ]; then
    printf "Type  erase  to continue: "
    read -r answer
    [ "$answer" = "erase" ] || die "Not erased."
  fi
  $COMPOSE --profile tools down -v
  rm -f deploy/.env
  start
}

show_pin() {
  local pos=${1:-}
  [ -n "$pos" ] || die "Usage: ./run.sh pin <position>   for example  ./run.sh pin cfo"
  pos=${pos#u-}
  command -v go >/dev/null || die "Go is needed to show a PIN (it recomputes the one-time PIN)."
  printf 'e-mail  %s@serk-demo.serk.example\nPIN     %s\n' "$pos" "$(go run ./cmd/serk-pin -position "u-$pos")"
  echo "(only valid until that person has chosen their own PIN at the first sign-in)"
}

cmd=${1:-up}
case "$cmd" in
  up|start|"")   start ;;
  reset)         reset "${2:-}" ;;
  stop)          need_docker; $COMPOSE stop ;;
  status)        need_docker; choose_ports; $COMPOSE ps -a; echo; curl -fsS "$BASE/readyz" 2>/dev/null && echo "  <- $BASE" || echo "API not reachable on $BASE" ;;
  logs)          need_docker; $COMPOSE logs -f --tail 100 api ;;
  data)          need_docker; choose_ports; demo_data ;;
  pin)           show_pin "${2:-}" ;;
  help|-h|--help) sed -n '2,15p' "$0" ;;
  *)             die "Unknown command '$cmd'. Try ./run.sh help" ;;
esac
