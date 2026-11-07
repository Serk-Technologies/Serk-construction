# Running SERK Construction locally (fully in Docker)

Everything runs in containers: PostgreSQL, the migrator, the approval signer
and the API (which also serves the web console). Nothing is installed on the
host except Docker. **Development only:** published, well-known secrets,
synthetic data, HS256 dev tokens. The API refuses all of it unless
`SERK_ENV=development`.

| Service | What it is | Host port |
|---|---|---|
| `db` | PostgreSQL 16 | `127.0.0.1:5432` by default (see "Port conflicts") |
| `migrate` | Applies all migrations, then exits | none |
| `signer` | Isolated approval-signing process | none (Unix socket in a shared volume) |
| `api` | REST API and console | `127.0.0.1:8080` |
| `seed`, `devtoken` | Dev tools, started only on request (profile `tools`) | none |

Prerequisites: Docker with the Compose plugin, `curl`. Run everything from the
repo root: `cd ~/serk/Serk-construction`.

## Port conflicts (read this first)

The default database port is 5432. If another Postgres already uses it (on the
machine this was written on, another project's `infra-db-1` does), do **not**
stop that project. Publish SERK's database on another port instead:

```bash
export SERK_DB_PORT=55432
```

Same idea for the API: `export SERK_API_PORT=8081` if 8080 is taken. SERK runs
under its own compose project name (`-p serk` below), so it cannot collide
with other compose projects' containers, networks or volumes.

## Offline or restricted network

By default the build uses `golang:1.26.10-alpine` and the database uses
`postgres:16`. If those cannot be pulled (no route to Docker Hub), point at any
locally cached equivalents. Go 1.26 or newer is required; `postgres:16-*`:

```bash
docker images | grep -E '^(golang|postgres) '
export GO_IMAGE=golang:1.27.1-bookworm     # any cached Go >= 1.26
export POSTGRES_IMAGE=postgres:16-alpine   # any cached Postgres 16
```

Module downloads during the build still need `proxy.golang.org`.

## Shortcut variables

Every command below uses these. Put them in your shell (add the exports above
if you need them):

```bash
export SERK_DB_PORT=55432                 # only if 5432 is taken
SERK="docker compose -p serk -f deploy/docker-compose.dev.yml"
TENANT=11111111-1111-4111-8111-111111111111
```

(`SERK` is a plain shell variable and must be used unquoted, as below. These
variables vanish with the shell; re-set them in every new terminal.)

---

## 1. First time

```bash
$SERK up -d --build        # builds the images, starts db -> migrate -> signer -> api
scripts/dev-seed.sh     # synthetic demo tenant + 2 projects; enables the test-persona sign-in
```

The first build takes a few minutes. When it finishes:

```bash
$SERK ps -a
curl -s http://127.0.0.1:8080/healthz     # {"status":"ok"}
curl -s http://127.0.0.1:8080/readyz      # {"status":"ready"}
```

`ps -a` should show `db` healthy, `signer` and `api` up, and `migrate`
**Exited (0)**. That is correct: it applies migrations 0001 to 0038 and stops.

The seed prints the tenant and two project ids. It goes through the real
maker/checker commands, so the database verifies genuine attestations. It is
idempotent; running it again just reports the existing ids.

## 2. Signing in

**Easiest:** the console's persona sign-in (26 test users, one shared passphrase). All
credentials and a step-by-step test guide are in [testmanual.md](testmanual.md).

Or paste a bearer token: the console holds no credentials of its own. Mint one inside a
container:

```bash
$SERK --profile tools run --rm devtoken --sub alice --tenant $TENANT --roles accountant
```

(`run` prints some `Container ...` progress lines first; the token is the last
line. Add `2>/dev/null | tail -1` to get only the token.)

Open **http://127.0.0.1:8080/** (it redirects to `/console/`) and paste it
into "Access token". It stays in that tab's memory only.

`devtoken` options: `--roles a,b`, `--projects id1,id2` (project scope; empty
means none), `--mfa` (fresh multi-factor, needed for step-up actions), `--ttl 1h`,
`--kind human|service|ai`.

**Maker/checker is enforced**, so one token cannot usually do both halves of a
flow: the maker and the checker must be different people (`--sub`) with
different roles. Mint one token per person:

| To do this | Roles (different `--sub` for maker and checker) |
|---|---|
| Journals | `accountant` -> `finance_manager` -> `controller` |
| Procure to pay | `site_engineer`, `procurement_officer` -> `procurement_manager`, `storekeeper`, `ap_clerk` -> `ap_supervisor` |
| Contract to cash | `quantity_surveyor`, `commercial_manager`, `ar_clerk` -> `ar_supervisor` |
| Stock | `storekeeper` -> `inventory_controller` |
| Planning | `planner` -> `project_manager` |
| Automation rules, settings | `process_owner` -> `workflow_admin`; `security_admin` |
| Read-only audit | `auditor` |

All roles are defined in `pkg/auth/auth.go`. Anything approve/post/pay/release is
reserved to human principals by the database itself, so a `--kind ai` or
`--kind service` token can only draft.

API check from the shell:

```bash
TOK=$($SERK --profile tools run --rm devtoken --sub alice --tenant $TENANT --roles accountant 2>/dev/null | tail -1)
curl -s -H "Authorization: Bearer $TOK" http://127.0.0.1:8080/v1/identity/me
curl -s http://127.0.0.1:8080/v1/identity/me          # 401: bearer token required
```

---

## 3. Every time after that

```bash
cd ~/serk/Serk-construction
export SERK_DB_PORT=55432          # if you needed it the first time
SERK="docker compose -p serk -f deploy/docker-compose.dev.yml"

$SERK up -d                        # start (data is kept; migrate reports "schema up to date")
$SERK stop                         # stop, keep everything
$SERK down                         # remove containers, KEEP the database volume
$SERK logs -f api signer           # follow logs
```

No re-seeding is needed: the database lives in the `serk_pgdata` volume and
survives `stop`, `down` and reboots.

**After `git pull` or any code change**, rebuild:

```bash
$SERK up -d --build
```

This also applies any new migrations (the `migrate` service runs on every `up`).

### Wipe everything and start over

```bash
$SERK --profile tools down -v      # removes containers AND the database volume
```

Then redo section 1. Roles and passwords are re-created because the volume is
empty again (the init scripts only run on an empty volume).

### Poke at the database

```bash
$SERK exec db psql -U postgres -d serk_construction
```

---

## 4. Running the tests

The integration suites need a disposable database whose login can `CREATE
ROLE`. **Do not** use the dev database; the tests create and drop roles and
tenants. Use a throwaway container and the host Go toolchain (Go 1.26+):

Two things the suite needs from that throwaway cluster (CI does the same):

- **trust authentication**: the tests create per-tenant and relay login roles
  and connect as them without passwords;
- **the cluster roles** from `deploy/00-roles.sql` (`serk_health`, `serk_app`, ...).

```bash
docker run -d --name serk-test-pg -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_DB=serk_test \
  -p 127.0.0.1:55433:5432 ${POSTGRES_IMAGE:-postgres:16}
sleep 5
docker exec -i serk-test-pg psql -q -U postgres -d serk_test < deploy/00-roles.sql
export SERK_TEST_ADMIN_DSN='postgres://postgres@127.0.0.1:55433/serk_test?sslmode=disable'
go vet ./...
go test -race -count=1 -p 1 ./...      # recorded run: 378 PASS, 0 FAIL, 0 SKIP
docker rm -f serk-test-pg
```

Browser journeys (Journeys A-C, planning, site) run inside the same suite when
Node and Playwright are available; otherwise they are **skipped** (a skip
still prints `ok`, so check with `-v`). On a machine without Playwright's own
browser download, point them at an installed Chrome:

```bash
export SERK_PLAYWRIGHT=/path/to/node_modules/playwright   # any Playwright install
export SERK_CHROME=/usr/bin/google-chrome                 # optional: use this browser
go test -count=1 -p 1 -run TestConsole -v ./internal/httpapi/
```

`make test-unit` runs only the pure packages and needs no database. Browser
journeys (`e2e/`, `internal/httpapi/console_browser_*_test.go`) need a browser;
see `docs/testing/`.

---

## 5. Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `port is already allocated` / `address already in use` on 5432 | Another Postgres owns it. `export SERK_DB_PORT=55432`. Do not stop the other project. |
| Same on 8080 | `export SERK_API_PORT=8081` and use that port in the URLs. |
| `failed to resolve reference ... network is unreachable` on build/pull | Docker Hub unreachable. Use cached images: see "Offline". |
| `migrate` exits 1 with `connection refused` | Fixed by the TCP healthcheck in the compose file. If you see it, you are on an old copy of `docker-compose.dev.yml`. |
| `signer` exits with `bind: permission denied` | Fixed by `/run/serk` ownership in `deploy/Dockerfile`. A stale `serk_signer_sock` volume from before the fix needs `$SERK down -v`. |
| `readyz` fails, `serk_health` auth error | The volume was created without `01-dev-passwords.sql`. `$SERK down -v`, then redo section 1. |
| `401 bearer token required` | Token missing, expired (`--ttl`), or from a different `SERK_TOKEN_SECRET`. |
| An approve/post is rejected | Maker/checker: use a different `--sub` and the checker role. |
| `configuration refused` | Expected outside development. Dev secrets only work with `SERK_ENV=development`. |
| Warnings about superuser owner / `log_parameter_max_length` / metrics disabled in `api` logs | Expected in dev; production refuses them. The dev database owner is the `postgres` superuser; production uses non-superuser `serk_owner` (`deploy/01-schema-owner.sql`, ADR 0016). |

## 6. What was and was not verified

Verified (Docker 29.1.3, Compose 5.6.0, Postgres 16.x alpine, Go 1.27.1 build image):

- build, `up`, migrations 0001-0038, signer socket, API `healthz`/`readyz` = 200;
- `seed` and `devtoken` through containers; authenticated `/v1/identity/me` = 200,
  unauthenticated = 401; `/console/` = 200 (not opened in a browser);
- `down` then `up` keeps the data; the other project's database was untouched.

Not verified:

- the default images (`golang:1.26.10-alpine`, `postgres:16`): not pullable here,
  cached equivalents were used;
- the approval flows for each role combination in the table (taken from `pkg/auth`);
- the test suite was not re-run for this document;
- production (`deploy/docker-compose.prod.yml`, OIDC, TLS): out of scope,
  `docs/release/GO_NO_GO.md` records it as **NO-GO**. Note the `/run/serk`
  change to `deploy/Dockerfile` also lands in the production image; review it
  there before relying on it.

## 7. Repo changes made to make this work

- `deploy/Dockerfile`: `GO_IMAGE` build arg (same default); a `devtools` target
  with `serk-seed` and `serk-devtoken` (the default/production image still
  excludes them); `/run/serk` owned by `nonroot` for the signer socket.
- `deploy/docker-compose.dev.yml`: overridable `POSTGRES_IMAGE`, `SERK_DB_PORT`,
  `SERK_API_PORT`, `GO_IMAGE` (defaults unchanged); TCP database healthcheck;
  `seed` and `devtoken` services behind the `tools` profile.
- `deploy/docker-compose.dev.yml` also sets the dev-only test-persona passphrase on the API.
- `scripts/dev-seed.sh` (new): seeds the demo tenant, writes its project ids to `deploy/.env`
  (gitignored, added to `.gitignore`) and recreates the API so persona sign-in works.
- `.dockerignore`: keeps `.git`, `bin/` and test evidence out of the build context.
