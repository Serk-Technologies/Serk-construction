#!/usr/bin/env bash
# Development only. Seeds the synthetic demo tenant in the dev compose stack,
# records its project ids in deploy/.env (gitignored; compose loads it) and
# recreates the API so the test-persona sign-in offers that company.
# Usage (stack already up):  scripts/dev-seed.sh
# Honours SERK_DB_PORT / SERK_API_PORT / POSTGRES_IMAGE / GO_IMAGE if exported.
set -euo pipefail
cd "$(dirname "$0")/.."
C="docker compose -p serk -f deploy/docker-compose.dev.yml"
OUT=$($C --profile tools run --rm seed 2>/dev/null)
echo "$OUT"
TENANT=$(grep -oE 'tenant [0-9a-f-]{36}' <<<"$OUT" | head -1 | cut -d' ' -f2)
NAME=$(sed -n "s/^serk-seed: \(.*\) \(already seeded.*\|seeded\)\$/\1/p" <<<"$OUT" | head -1)
PROJECTS=$(grep -E '^ +project ' <<<"$OUT" | grep -oE '[0-9a-f]{8}-[0-9a-f-]{27}' | paste -sd, - | sed 's/,/","/g')
[ -n "$TENANT" ] && [ -n "$PROJECTS" ] || { echo "could not parse seed output" >&2; exit 1; }
# keep any other lines (e.g. ports) already in deploy/.env
touch deploy/.env; grep -v '^SERK_STAGING_COMPANIES=' deploy/.env > deploy/.env.tmp || true
printf "SERK_STAGING_COMPANIES='[{\"id\":\"%s\",\"name\":\"%s\",\"projects\":[\"%s\"]}]'\n" "$TENANT" "$NAME" "$PROJECTS" >> deploy/.env.tmp
mv deploy/.env.tmp deploy/.env
$C up -d api
echo "persona sign-in ready: see testmanual.md"
