# Changelog (engineering)

Changes made under the SERK Construction OS 3.0 directive (`MASTER_PROMPT.md`). Earlier history: `docs/release/CHECKPOINT_*.md` and `git log`.

## 2026-11-15 (second session) — document security, CI, SERK FLEET part 1

### Added
- **Document security** (migration `0053`, ADR 0019):
  - `pkg/blob`: S3-compatible object store client (Signature V4, multipart, standard library only), a development directory store, and an envelope that encrypts every object per tenant and detects tampering, truncation and swaps.
  - `pkg/docscan`: inspection before storage (type signatures, archive bombs, path traversal, macros, active PDF content; SVG and HTML refused) and a ClamAV daemon client.
  - Fail-closed quarantine in the kernel and the database; a per-tenant document policy changed only by an attested decision with fresh MFA; scan verdicts carry a keyed proof.
  - Streamed upload (`PUT /v1/documents/{id}/revisions/{revision}/file`) and download; short-lived signed download links; storage quota; rescan and discard.
  - Two-step migration from PostgreSQL to object storage (`POST /v1/document-storage/migrate`, then `serk-migrate release-migrated-documents` by the schema owner).
  - Configuration: `SERK_DOCUMENT_KEY`, `SERK_S3_*`, `SERK_CLAMD_ADDR`, `SERK_DOCUMENT_MAX_MB`, `SERK_BLOB_DIR` (development only).
- **SERK FLEET part 1** (migration `0054`, ADR 0020): equipment register, fourteen-state lifecycle, reservations that cannot overlap, operator authorisations, daily logs, meter control and resets, Fleet Command Centre. Roles `plant_officer`, `plant_manager`, `plant_inspector`; six `fleet.*` permissions with five duty conflicts. API under `/v1/equipment*` and `/v1/fleet/summary`; register paged by cursor.
- `tools/capacity/fleet_measure.sh` and its first result; `tools/test_results.py`; `ROLE_PERMISSION_PROPOSAL.md`.

### Changed
- CI: the image job built a stage that no longer exists and the vulnerability scan failed on Go 1.26.10. Now Go 1.26.11, both Dockerfiles built, image vulnerability scan, secret scan of the history (with one documented allow-listed test fixture file), console script check, and MinIO and ClamAV running for the integration tests, where a skipped integration fails the job. No credential is written in the workflow.
- `size_bytes` of a revision is `bigint` with no schema ceiling; the limit is a deployment setting.
- Requirement CNX-01.4 (malware scanning) moved from BLOCKED to VERIFIED; CNX-01 gained requirements 7–13 and FLT-01 requirements 2–13. Existing IDs are unchanged.

### Not changed
- No committed migration was edited. No deployment, merge or data load. The release decision remains NO-GO.

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
