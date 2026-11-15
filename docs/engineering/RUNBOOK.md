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
# Bucket for the document tests (creates it, proves it private):
#   S3_ENDPOINT=... S3_BUCKET=serk-test S3_ACCESS_KEY=... S3_SECRET_KEY=... go run ./tools/ci/s3provision
# Browser journeys: export SERK_PLAYWRIGHT=<path to node_modules/playwright>   (1.63.0)
go test -race -count=1 -p 1 -json ./... > run.json   # about 4 minutes on 4 vCPU
python3 tools/ci/check_test_results.py run.json --mandatory tools/ci/mandatory_tests.txt --min-pass 455   # the same gate CI applies
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

## Document key rotation (ADR 0021)

Keys: `SERK_DOCUMENT_KEY` (current) with `SERK_DOCUMENT_KEY_ID`, and after a rotation `SERK_DOCUMENT_KEY_PREVIOUS` with `SERK_DOCUMENT_KEY_PREVIOUS_ID`. Each is at least 32 random bytes from the secret store, never from a repository or a ticket.

Before a key is first used: two named people place a sealed offline copy in escrow, with its id and date. Without that copy, losing the secret store loses every file under the key.

Rotate:
1. Generate the new key in the secret store. Escrow it (above).
2. Set `SERK_DOCUMENT_KEY_PREVIOUS` and `_PREVIOUS_ID` to the present key and id; set `SERK_DOCUMENT_KEY` and `_ID` to the new key and a new id (for example `d2`). The service refuses to start if the ids are equal or the keys are the same.
3. Restart every API instance together. At start-up the log says "document key rotation in effect" with both ids.
4. Check: download one file from before the rotation and upload and download a new one.

Recover ("file storage is temporarily unavailable", log line `encryption key version not available: object needs key "dN"`): the service was started without key `dN`. Supply it as the previous key (from the secret store or escrow) and restart. Nothing has been changed or lost.

Not available yet: re-encrypting existing files under a new key, more than one previous key through configuration, KMS/HSM custody.
