# Railway staging deployment: evidence (2026-10-25)

TEST ENVIRONMENT ONLY. Synthetic data. The release decision stays NO-GO
(`docs/release/GO_NO_GO.md`); nothing here is a production deployment or a
human sign-off.

## Deployed

| Item | Value |
|---|---|
| Railway project | `serk-construction-staging`, environment `staging` |
| Services | `serk-staging` (deploy/railway/Dockerfile), `Postgres` (Railway template, **PostgreSQL 18**) |
| Source | GitHub `Egziu/Serk-construction`, branch `main` |
| URL | `https://serk-staging-staging.up.railway.app/console/` |
| First deployment | `1748285c-…` at commit `2f851b3`: status SUCCESS (Railway `/readyz` health check passed) |

Deploy log of that deployment (Railway): migrations 0001–0037 applied as
`serk_owner`, deploy/02 applied, `serk-seed: synthetic tenant seeded`
(tenant `b00ca2e9-…`, projects P-001 and P-002), `signer listening`, API
listening on :8080 with `STAGING persona sign-in enabled`. No database
posture warning was logged.

## Tests executed

- Full race suite, Go 1.26.10 / PostgreSQL 16, fresh database
  `serk_full38b`: **360 PASS, 0 FAIL, 0 SKIP**
  (`evidence/full-suite-railway-staging-fresh-db.txt`). A first run on
  `serk_full38` failed 10 tests that connect as password-less test logins:
  the rebuilt local cluster used scram authentication for 127.0.0.1. With the
  disposable cluster's local `trust` restored, all passed. No code change.
- New tests: `TestStagingLoginConfig` (refused in production, with OIDC,
  without tenant, short or reused passphrase), `TestStagingLoginDisabledByDefault`,
  `TestStagingLoginIssuesPersonaTokens` (wrong passphrase 401, unknown
  persona 422, token verifies with the right tenant, scope and MFA claims,
  passphrase guessing locked out with 429).
- Local simulation of the Railway container (same entrypoint, fresh
  database, PostgreSQL 16): first boot and a restart (seed idempotent);
  `scripts/staging_smoke.py`: BOQ create, self-approval refused 403,
  approval by another persona through the isolated signer, PR create,
  submit, approval through the signer, auditor read: all PASS. Console
  persona sign-in checked in Chromium with no page errors; this found and
  fixed a pre-existing layout fault (signed-out page squeezed into the
  hidden navigation column). Console browser journeys A, B, C, planning
  and site re-run after the fix: PASS.

## Not verified here (open)

- The smoke script was **not** run against the Railway URL: this agent's
  network cannot reach `*.up.railway.app`. Run
  `python3 scripts/staging_smoke.py https://serk-staging-staging.up.railway.app <passphrase>`.
- Railway's database is PostgreSQL 18; the suite ran on 16. Migrations and
  the seed succeeded on 18, but the adversarial suite has not run on 18.
- Everything listed in `deploy/railway/README.md` under "Differences from
  production" remains a production blocker.

## Redeployment 2026-11-16 (commit 942e52a)

TEST ENVIRONMENT ONLY; the release decision stays NO-GO.

| Item | Value |
|---|---|
| Source | GitHub `Serk-Technologies/Serk-construction`, branch `claude/new-session-xkd5g3`, **pinned** to `942e52a` (pushes do not redeploy) |
| Deployment | `4d9e0ecf-…`, status SUCCESS (Railway `/readyz` health check passed), replacing `efeb8cca-…` (commit `5f94bc3`, 2026-10-26) |
| Deploy log | migrations 0039–0094 applied as `serk_owner`; seed found both synthetic companies already seeded; signer listening; API listening on :8080 |
| Warnings logged (expected) | HS256 staging tokens; no malware scanner (`SERK_CLAMD_ADDR` unset), so uploaded documents stay quarantined; metrics disabled; persona sign-in enabled |

Rehearsed before switching, on PostgreSQL 16 in the development container: the staging entrypoint on an empty
database (94 migrations, seed, signer, API ready; `scripts/staging_smoke.py` 10 PASS) and the upgrade path
(entrypoint of `5f94bc3`, then of `942e52a`, on the same database: 56 migrations applied, seed idempotent,
smoke 10 PASS). Not done: the smoke script against the Railway URL (unreachable from the agent's network) and
the adversarial suite on PostgreSQL 18.

## Redeployment 2026-11-16 (commit 9cfe480): e-mail + PIN sign-in for every position

TEST ENVIRONMENT ONLY; the release decision stays NO-GO.

| Item | Value |
|---|---|
| Source | branch `claude/new-session-xkd5g3`, **pinned** to `9cfe480` (full fresh-database run 531 passed, 0 failed, 0 skipped) |
| Deployment | `b0d07b56-…`, status SUCCESS, replacing `4d9e0ecf-…` (`942e52a`) |
| New variables | `SERK_LOGIN_PEPPER` (API and seed) and `SERK_SEED_PIN_SECRET` (seed only; the entrypoint removes it from the signer and the API), each 48 random characters |
| Deploy log | migrations 0095–0097 applied; seed: 37 staging e-mail sign-ins issued for each of the two synthetic companies ("one-time PINs, derived; never printed"); `e-mail + PIN sign-in enabled (HS256 sessions; development and staging only)`; API listening |

Sign-ins are `<position>@serk-demo.serk.example` and `<position>@senselet.serk.example`; their roles and projects come from
the company directory. Each one-time PIN is derived from `SERK_SEED_PIN_SECRET` and the company and person ids
(`pinauth.DerivePIN`). It is never logged or stored readable, and it must be replaced at the first sign-in. The list was
handed to the owner privately and is not in the repository.

Rehearsed before the switch with the staging entrypoint on PostgreSQL 16:
- a fresh database without the variables, with a persona signed in by token;
- a restart with the variables: 37 sign-ins per company; the system administrator, CEO and the previously unmanaged
  project manager signed in with their derived PIN, had to choose a new one, and received their directory roles and projects;
- a further restart: 0 issued, and the chosen PINs were kept.

Not verified here: a sign-in against the Railway URL itself, which this agent's network cannot reach.
