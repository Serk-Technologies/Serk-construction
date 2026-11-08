# Administrator runbook (release 1)

For the people who install, run and look after SERK Construction OS. Pre-production: production on-call
ownership, vault, identity provider and monitoring are **not yet in place** (see `docs/release/GO_NO_GO.md`).
Incident procedures are in `INCIDENT_RESPONSE.md`; key handling in `KEY_CUSTODY_RUNBOOK.md`; backup and
restore in `BACKUP_PITR_RESTORE_PROOF.md`; alerts in `OBSERVABILITY_SLOS.md`.

## 1. The pieces

| Process | What it does | Needs |
|---|---|---|
| `serk-api` | REST API, the web console, workflow timers and automation | the database (per-tenant logins), the signer socket, token verification settings |
| `serk-signer` | Holds the approval-signing key and signs human decisions; never serves users | its own key; a Unix socket shared with the API |
| `serk-migrate` | Applies the migrations (checksum-locked); provisions tenants | the schema-owner database login |
| `serk-relay` | Delivers outbox events as signed webhooks | the publisher database login |
| PostgreSQL 16 | The system of record and the last line of defence (row-level security, triggers) | PITR backups |

Never give the API both `SERK_SIGNER_SOCKET` and `SERK_APPROVAL_SIGNING_KEY`: it refuses to start.

## 2. Start, stop, health

Local or demo (Docker): see `runlocal.md`. Health endpoints: `GET /healthz` (process up) and `GET /readyz`
(database reachable). Metrics are served only on `SERK_METRICS_LISTEN`, a separate internal listener.

```bash
curl -s http://127.0.0.1:8080/readyz          # {"status":"ready"}
docker compose -p serk -f deploy/docker-compose.dev.yml logs -f api signer
```

## 3. Configuration (environment variables)

Validated at start; production refuses every test-only setting. The ones an administrator meets:

| Variable | Meaning |
|---|---|
| `SERK_ENV` | `production` or `development` (development enables published dev secrets and test sign-in) |
| `SERK_LISTEN`, `SERK_METRICS_LISTEN` | API and metrics addresses (must differ) |
| `SERK_DB_BASE_DSN`, `SERK_TENANT_DB_SECRET` | database address and the secret each tenant login is derived from |
| `SERK_SIGNER_SOCKET`, `SERK_SIGNER_KEY_ID`, `SERK_APPROVAL_SIGNING_KEY` | approval signing (the key only in the signer) |
| `SERK_OIDC_ISSUER`, `SERK_OIDC_AUDIENCE`, `SERK_OIDC_JWKS_URL`, `SERK_OIDC_*_CLAIM` | identity-provider token verification and claim names |
| `SERK_TOKEN_SECRET` | HS256 tokens (development and staging only) |
| `SERK_BANK_DATA_KEY`(`_ID`, `_PREVIOUS`), `SERK_BANK_DIGEST_KEY` | encryption of bank account numbers at rest, with key rotation |
| `SERK_RATE_PER_MIN`, `SERK_PREAUTH_PER_MIN`, `SERK_AUTH_FAILURES_PER_MIN`, `SERK_TRUSTED_PROXIES`, `SERK_ABUSE_DSN` | abuse limits (shared across replicas through the abuse database) |
| `SERK_AI_ENDPOINT`, `SERK_AI_KEY`, `SERK_AI_MODEL` | optional model phrasing for the assistant (facts are always computed by the server) |

Check a production file without starting the server: `make check-prod-config`.

## 4. Migrations

Additive and checksum-locked: **a migration that has been applied is never edited**; corrections are a new
migration. If the migrator reports `already applied with a different checksum; refusing`, someone changed a
committed file. In a disposable database, recreate it; in any other, restore the file from version control.

```bash
SERK_ADMIN_DSN=... go run ./cmd/serk-migrate                    # apply
SERK_ADMIN_DSN=... SERK_TENANT_DB_SECRET=... go run ./cmd/serk-migrate provision-tenant --tenant <uuid> --name "Company PLC"
```

Release 1 added migrations 0039 (budget revisions), 0040 (authority matrix, delegation, duty exceptions),
0041 (units, reservations, reorder rules), 0042 (project master, stage gates, calendar), 0043 (staged imports).

## 5. Onboarding a company

1. Provision the tenant (`provision-tenant`) and its legal entity, chart of accounts and first project (operator tooling:
   `cmd/serk-seed` shows the pattern with synthetic data).
2. In the console, **Overview → Company setup** lists what remains, who does it and links to each page.
3. Load suppliers, customers and stock items with **Governance → Data imports** (a second person commits).
4. Assign people to roles and projects **in the identity provider**; the console shows only what a person's token allows.

## 6. People, roles and controls

- Roles and permissions: `docs/security/ROLE_PERMISSION_MATRIX.md` (generated from the code).
- Approvals are human decisions signed by the isolated signer and re-checked by the database; nobody approves what
  they prepared; AI and service principals only prepare drafts.
- Limits: the **authority matrix** (Governance → Authority & duties) sets who may approve how much; **delegations**
  lend authority for at most 90 days and are revoked from the same page.
- Revoke a person's sessions: security administrator, `POST /v1/identity/sessions/revoke` (see the OpenAPI spec).

## 6a. Routine checks

| When | Check |
|---|---|
| Daily | `readyz`; the control review page; outbox backlog and dead letters; failed workflow effects |
| Weekly | Dashboard KPI "Ledger balance" is zero; reconciliations (Reports → Reconciliation) show no difference |
| Monthly close | Subledgers reconciled to the GL, then close the period (controller); never reopen without the CFO |
| Quarterly | Restore drill from the encrypted off-site backup; rotate the bank-data key per the key runbook |

## 7. Reset the demo (development only)

Wipes the synthetic database. The seed is idempotent and writes the persona sign-in settings.

```bash
export SERK_DB_PORT=55432 POSTGRES_IMAGE=postgres:16-alpine GO_IMAGE=golang:1.27.1-bookworm   # if Docker Hub is unreachable
docker compose -p serk -f deploy/docker-compose.dev.yml --profile tools down -v
docker compose -p serk -f deploy/docker-compose.dev.yml up -d --build && scripts/dev-seed.sh
```

## 8. What is not covered by this runbook yet

Production identity provider and MFA assertions, the vault and HSM-held signing key, the observability stack, the
external penetration test, load and soak tests, and the named human sign-offs. Each is an open item of release gate A
(`docs/release/READINESS.md`).
