# Runbook

Operational procedures are maintained in `docs/operations/`: `ADMIN_RUNBOOK.md`, `INCIDENT_RESPONSE.md`, `KEY_CUSTODY_RUNBOOK.md`, `BACKUP_PITR_RESTORE_PROOF.md`, `OBSERVABILITY_SLOS.md`. Local development: `runlocal.md`. This page holds only what an engineer needs to work on the code.

## Run the checks

```bash
# A disposable PostgreSQL 16 whose login can CREATE ROLE (never a shared database):
initdb -D /var/lib/postgresql/serk-test -U postgres --auth=trust -E UTF8 --locale=C.UTF-8      # as the postgres OS user
pg_ctl -D /var/lib/postgresql/serk-test -o '-k /tmp -c max_connections=300' -w start
psql -h /tmp -U postgres -c 'create database serk_test'
psql -h /tmp -U postgres -d serk_test -v ON_ERROR_STOP=1 -f deploy/00-roles.sql

export SERK_TEST_ADMIN_DSN='postgres://postgres@/serk_test?host=/tmp&port=5432'
test -z "$(gofmt -l .)" && go vet ./...
# Optional real services for the document security tests (they SKIP without these, and a skip is not a pass):
#   export SERK_TEST_CLAMD=127.0.0.1:3310            # a ClamAV daemon
#   export SERK_TEST_S3_ENDPOINT=http://127.0.0.1:9000 SERK_TEST_S3_ACCESS_KEY=... SERK_TEST_S3_SECRET_KEY=... SERK_TEST_S3_BUCKET=serk-test
go test -race -count=1 -p 1 ./...            # about 4 minutes on 4 vCPU
```

The browser journeys run when `node` and the `playwright` module are found (`SERK_PLAYWRIGHT=<path>`); otherwise they report SKIP, which is not a pass.

## Run it locally

`make up && make seed` (Docker; see `runlocal.md`), or the commands in `README.md`. The API refuses development secrets outside `SERK_ENV=development`.

## After changing routes, permissions, KPIs or reports

`make gen-docs`, then replace any `x-generated` OpenAPI stub with a written description.

## Record evidence after a slice

```bash
go test -race -count=1 -p 1 -json ./... > run.json
python3 tools/test_results.py run.json docs/engineering/evidence/<date>-<name>.tests.txt
python3 tools/requirements_register.py --results docs/engineering/evidence/<file> --commit $(git rev-parse --short HEAD)
```

Then update `execution-state.json` and `HANDOVER.md`.

## Recovery drill

`DRILL_DIR=<scratch> LOAD_BIN=<kernel.test> deploy/drills/pitr_drill.sh` builds disposable clusters and measures restore time and data loss. Never point it at a real cluster.

## Do not

Edit a committed migration; deploy, merge to `main` or force-push without the owner's authorisation; load real customer data; put a secret in the repository; mark a requirement VERIFIED by hand.
