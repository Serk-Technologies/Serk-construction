# Architecture

The architecture is recorded where the repository already keeps it. This page is the index and the comparison with the Bible; it does not restate those documents.

| Topic | Source of truth |
|---|---|
| Layers, modules, governance invariants, how to add a module | `docs/architecture/MODULES.md` |
| Data flow | `docs/architecture/DATA_FLOW.md` |
| Domain dependencies | `docs/baseline/DOMAIN_DEPENDENCY_MAP.md` |
| Decisions | `docs/adr/0001`–`0018`, indexed in `ADR/README.md` |
| Database privileges | `docs/security/DB_PRIVILEGE_MATRIX.md` |
| Roles and permissions (generated) | `docs/security/ROLE_PERMISSION_MATRIX.md` |
| API contract | `api/openapi.yaml` |

## Shape

A modular monolith in Go over PostgreSQL. One direction of dependency: console → HTTP API → kernel (every command and read) → store (SQL only) → pure packages. The database is the last line of defence: triggers re-verify approvals, immutability, period rules and tenant isolation even if the kernel is bypassed.

Processes: `serk-api` (API and workers), `serk-signer` (approval key, isolated), `serk-relay` (outbox to signed webhooks), `serk-migrate`.

## Against the Bible's baseline decisions

| Bible | Repository | Verdict |
|---|---|---|
| Go transactional core, modular | Yes | Keep |
| PostgreSQL authority, transactional outbox | Yes, with database-enforced invariants | Keep |
| RLS with `SET LOCAL app.tenant_id` | Stronger: tenant identity comes from the database login, not a session variable (ADR 0006) | Keep the repository's design |
| React/TypeScript web | Framework-free static console, tested in a browser with accessibility checks | Keep; revisit only if the console's size makes it unmaintainable (it is one 4,400-line file) |
| Flutter field app, encrypted SQLite | Absent; sync API exists | Open (M5) |
| Object storage for documents | Files in PostgreSQL with a size cap (ADR 0018) | Interim, with a migration path |
| Event bus (NATS/Kafka) | Outbox relay to signed webhooks | Sufficient until a consumer needs a bus |
| Tenant cells and control plane | Tenant router with a dedicated-database tier; no control plane, no cell moves | Open (M10) |
| Partitioning, archive tiers | None | Open (M10) |
| AI gateway and agents | Deterministic assistant; AI principals cannot decide | Open (M9) |
| PostGIS, IFC | Absent | Open (M8) |

## Known structural debts

- The shared SQL functions `p2p_subject_text` and `serk_subject_project` are redefined in full by every migration that adds a document type. This is deliberate (additive migrations) but error-prone; a table-driven registry would remove the copy.
- List endpoints are bounded by `LIMIT`, not cursor-paginated.
- The console is a single JavaScript file.
