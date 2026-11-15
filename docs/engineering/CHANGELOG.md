# Changelog (engineering)

Changes made under the SERK Construction OS 3.0 directive (`MASTER_PROMPT.md`). Earlier history: `docs/release/CHECKPOINT_*.md` and `git log`.

## 2026-11-15 — M0 baseline and first M7 slice

### Added
- The implementation pack: `CLAUDE.md`, `MASTER_PROMPT.md`, `spec/` (Bible in Markdown and Word, 24 figures, module register).
- `docs/engineering/`: baseline, requirement register (80 domains, generated, machine-readable), architecture and decision index, threat-model additions, test matrix for the 22 mandatory scenarios, capacity plan, release gates, runbook, handover and `execution-state.json`.
- `tools/requirements_register.py`: generates the register and downgrades any VERIFIED claim whose tests did not pass in the cited run.
- **Controlled documents, revisions and RFIs** (migration `0052`, ADR 0018): register entries, immutable revisions with database-hashed files, maker/checker publication with fresh MFA, supersession with exactly one current revision, scoped and recorded downloads, RFIs answered by a second person.
  - Permissions `document.manage` (site engineer, quantity surveyor, planner) and `document.approve` (project manager), as a separation-of-duties pair.
  - API: `/v1/documents`, `/v1/document-revisions/{id}/…`, `/v1/rfis`; both types also served by the generic `/v1/p2p/{type}` routes.
  - Console: Documents navigation group, register, document page, upload and RFI forms, publication from the approvals worklist.
  - Tests: four kernel tests, one API test, one browser journey.

### Changed
- The request body limit is per route: 1 MiB everywhere, 14.3 MiB on `POST /v1/documents/{id}/revisions`.
- `docs/security/ROLE_PERMISSION_MATRIX.md` and `api/openapi.yaml` regenerated for the new permissions and routes.

### Not changed
- No committed migration was edited. No deployment, merge or data load was performed. The release decision remains NO-GO.
