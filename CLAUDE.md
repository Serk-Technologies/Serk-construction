# SERK Construction OS — repository execution instructions

Read `MASTER_PROMPT.md` in full and `spec/BIBLE.md` before implementation. The original Word Bible and all embedded figures are under `spec/`. `spec/module-register.csv` is the original grouped domain register; expand it into testable individual requirements. Follow repository-local higher-priority instructions and preserve existing implementation where valid.

Mandatory: repository discovery first; evidence-driven vertical slices; Go/PostgreSQL baseline unless ADR establishes otherwise; transactional accounting invariants, tenant isolation, offline-first field workflows, expandable cells and policy-compliant historical retention; no direct AI writes; no fake features. No destructive change, production deployment, real data, protected branch merge or force push without separate authorization. Record actual executed test evidence and blockers. Maintain `docs/engineering/execution-state.json` and requirement traceability, resume from the checkpoint, never mark untested work verified.

Start with M0/M1 in `MASTER_PROMPT.md`. Continue in coherent milestones. Do not replace a valid repo with a parallel scaffold. If this file would overwrite existing `CLAUDE.md`, merge instructions deliberately rather than replacing it blindly.

## Repository facts (added at M0, 2026-11-15)

This repository already holds the SERK Construction OS: Go 1.26, PostgreSQL 16, a static web console. Extend it; do not scaffold beside it.

- Start with `docs/engineering/HANDOVER.md` and `docs/engineering/execution-state.json`. Module map and "how to add a module": `docs/architecture/MODULES.md`. Decisions: `docs/adr/`.
- Tests need a disposable PostgreSQL 16 whose login can CREATE ROLE, with `deploy/00-roles.sql` applied:
  `SERK_TEST_ADMIN_DSN='postgres://postgres@/serk_test?host=/tmp&port=5432' go test -race -count=1 -p 1 ./...`
- Migrations are additive and never edited once committed. A new document type must be added to `p2p_subject_text` and `serk_subject_project` (copied verbatim from the latest migration), `p2pTables` and `listSQL` in `internal/store/postgres/p2prepo.go`, and `docProject` in `internal/kernel/p2p.go`.
- After adding routes or permissions run `make gen-docs` (OpenAPI stubs, role matrix, KPI and report catalogues); tests fail if these are stale.
- After a verified slice: save the test results under `docs/engineering/evidence/`, run `python3 tools/requirements_register.py --results <file>`, update `execution-state.json` and `HANDOVER.md`.
- `tools/gate_status.py` rewrites `docs/release/GATE_STATUS.json`; run it only when cutting a checkpoint.
