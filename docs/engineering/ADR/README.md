# Decision records

Decisions live in `docs/adr/`. This index maps them to the ADR topics `MASTER_PROMPT.md` section 1 asks for and to the Bible's proposed ADR 001–006.

| Topic | Record | State |
|---|---|---|
| Architecture (Bible 001) | `0001-modular-monolith` | Decided |
| PostgreSQL authority and outbox (Bible 002) | `0002-postgres-as-invariant-enforcer`, `0009-subledger-documents-generate-bound-draft-journals` | Decided |
| Accounting | `0003-exact-decimal-money`, `0012-stock-valuation-fifo-wac`, `0011-contract-to-cash-and-human-decisions` | Decided; accounting policy content needs an accountant |
| Tenancy | `0006-database-enforced-tenant-identity`, `0016-non-superuser-schema-owner` | Decided |
| Identity | `0005-hs256-now-oidc-next`, `0015-isolated-approval-signer` | Decided; no real identity provider connected |
| Workflow | `0007-controlled-approval-workflow-engine`, `0008-approval-attestations-at-the-sql-boundary` | Decided |
| Synchronisation (Bible 005) | `0004-no-crdt-for-financial-state`, `0017-site-operations-and-offline-sync` | Server side decided; device leases and offline stock rights undecided |
| Document visibility, storage | `0010-one-document-visibility-policy-and-masked-bank-data`, `0018-controlled-documents-in-postgres` | Decided; object storage deferred |
| Planning | `0013-planning-cpm-and-earned-value` | Decided |
| Abuse limits | `0014-distributed-abuse-limits` | Decided |
| Tenant cells and scaling (Bible 003) | — | **Not decided.** Needs measured load first (`CAPACITY_PLAN.md`) |
| Data lifecycle and archive (Bible 004) | — | **Not decided.** Needs a retention schedule from the owner |
| AI providers (Bible 006) | — | **Not decided.** No provider is authorised; the boundary (AI cannot decide) is enforced |
| BIM | — | Not decided (M8) |
| Localisation | — | Not decided; tax rules are data (`pkg/tax`) with no real rates |
| Deployment | `deploy/`, `docs/operations/`, `docs/testing/STAGING_RAILWAY.md` | A Railway staging run is recorded there; not repeated in this session; no production profile operated |

Each new record states context, decision, alternatives, consequences and a reversal plan, as 0018 does.
