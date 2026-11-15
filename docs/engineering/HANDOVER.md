# Handover — 2026-11-15, second session (updated after the independent audit)

For the next engineer or session. Machine-readable state: `execution-state.json`. Earlier history: `CHANGELOG.md`.

## Where things are

| | |
|---|---|
| Repository / branch | `Serk-Technologies/Serk-construction`, `claude/new-session-xkd5g3` |
| Commit at the start of this session | `12c3cec` |
| Commits of this session | `1ae8f5b` (document security, CI), then the fleet commit that contains this file |
| Release status | **NO-GO** (unchanged). Nothing deployed, merged or loaded with real data |

## What this session did

1. **Reproduced the baseline** at `12c3cec`: 433 passed, 0 failed, 0 skipped, as reported.
2. **Priority A.1, document security** (migration 0053, ADR 0019): encrypted S3-compatible object storage with database-fixed tenant-scoped keys, inspection before storage, ClamAV scanning, fail-closed quarantine in kernel and database, streamed transfer, short-lived download links, quota, and a two-step verified migration from PostgreSQL.
3. **Priority A.2, CI**: read the real GitHub Actions results for this branch, fixed the two jobs that were failing before this work, and added image scan, secret scan, console check and real scanner and object-store services to the test job.
4. **Priority A.3, authorisation review**: tests for every item of the directive's list (`TEST_MATRIX.md`, last section) and `ROLE_PERMISSION_PROPOSAL.md` for professional review.
5. **Priority B, SERK FLEET part 1** (migration 0054, ADR 0020): register, lifecycle, reservations, operator authorisations, daily logs, meter control, command centre. Sections B.1–B.4 of the directive in part; B.5–B.9 not started.
6. A first **capacity measurement** and a **recovery drill** with all 54 migrations.

## Verified results (executed in this session)

| Check | Result | Evidence |
|---|---|---|
| Baseline at `12c3cec` | 433 passed, 0 failed, 0 skipped | reproduced; same count as the previous handover |
| After document security (`1ae8f5b`) | 445 passed, 0 failed, 0 skipped | `evidence/2026-11-15-document-security.tests.txt` |
| After fleet part 1 | **454 passed, 0 failed, 0 skipped** | `evidence/2026-11-15-fleet-part1.tests.txt` |
| `gofmt -l .`, `go vet ./...` | clean | — |
| Real services in those runs | A ClamAV 1.5.4 daemon and a MinIO server built from source ran locally; `TestClamdAgainstARealDaemon`, `TestS3AgainstARealServer` and `TestDocSecurity_RealScannerAndObjectStore` passed, not skipped | same files |
| Browser journeys (documents, fleet and the five earlier ones) | pass, with accessibility audits | `TestConsoleBrowser*` |
| Recovery drill | PASS: 16 table fingerprints identical, 54 migrations replayed, restore verified in 1.15 s, 5 s of writes lost with a 10 s archive timeout | `evidence/2026-11-15-pitr-drill-0054.txt` |
| Fleet query measurement | 100,000 machines, 1,000,000 logs: register page 0.09 ms at any depth; one project's 30-day summary 37 ms; company-wide summary over all logs 695 ms | `evidence/2026-11-15-fleet-query-measurement.txt`, `CAPACITY_PLAN.md` |

Command for the suite: `go test -race -count=1 -p 1 -json ./...` with `SERK_TEST_ADMIN_DSN`, `SERK_TEST_CLAMD`, `SERK_TEST_S3_*` set (see `RUNBOOK.md`). Environment: 4 vCPU, 16 GiB RAM, Go 1.26, PostgreSQL 16.15.

### GitHub Actions, as actually run

Full table and root causes: `AUDIT_REMEDIATION_2026-11-15.md`. In short: runs 14 to 20 failed (run 20 is `main` after PR #3 was merged while failing); run 21 passed but with the browser journeys skipped; **run 22 (`be030f7`) is the first run with all six jobs passing and no skipped test** (457 tests). An earlier version of this file said the `test` job passed in run 14: that was wrong, the step could not fail.

## Security findings

- **Fixed**: uploads were not scanned (ADR 0018 residual). Now fail closed.
- **Found in CI, fixed by upgrade**: a reachable Go standard-library vulnerability on 1.26.10. Confirmed gone on 1.26.11 by run 15. The vulnerability database is unreachable from the build environment, so this could not be checked locally.
- **Found, no action needed**: 7 secret-scan hits are validator test fixtures, not credentials.
- **Open, important**:
  - Scanner detection coverage is unmeasured: the public ClamAV signature feed is unreachable here, so the evidence runs used a database containing only the EICAR test hash.
  - Document keys rotate (ADR 0021) but existing files are not re-encrypted and custody is an environment secret.
  - Download links are reusable for two minutes and appear in request paths.
  - The image vulnerability scan and the ClamAV container have not yet completed on GitHub.
  - Everything listed as open in `docs/release/GO_NO_GO.md` (penetration test, real identity provider, signer key custody) is unchanged.

## Requirement status

178 requirements: 158 across the 80 domains and 20 cross-cutting (159 at the start of this session; the additions are the itemised document-security and fleet requirements). VERIFIED 86, IMPLEMENTING 7, IMPLEMENTED_UNVERIFIED 1, BLOCKED 9, NOT_STARTED 75. By domain: 38 partly built, 40 not started, 2 blocked; no domain is VERIFIED as a whole because no owner has reviewed any domain's requirement list as complete. Newly verified this session (14): CNX-01.10, CNX-01.11, CNX-01.4, CNX-01.7, CNX-01.8, CNX-01.9, FLT-01.1, FLT-01.2, FLT-01.3, FLT-01.4, FLT-01.5, FLT-01.6, FLT-01.7, FLT-01.8. Nothing was downgraded. VERIFIED means the named tests passed here; it is not an acceptance.

## Financial integration

None was added. Fleet part 1 records hours and fuel quantities but posts nothing and allocates no cost; no journal is touched by any fleet code. The ledger, stock and job-cost tests are unchanged and pass. Charge-out, depreciation and fuel costing are the next part and must use the existing posting interfaces.

## Known limitations of what was built

- Fleet: no fuel, maintenance work orders, costing, rental agreements, attachments, fixed-asset link, telematics or optimisation; reservations are whole machines and whole days; logs are not yet a `/v1/sync` command; the operator named in a log does not sign it.
- Documents: no preview generation, no client-side resumable upload, no retention or legal hold, no key rotation, quota can be overshot by one concurrent file, PDF active-content checks are heuristic.
- The console is one JavaScript file and lists other than the fleet register are still bounded by LIMIT rather than paged by cursor.
- A company-wide fleet summary aggregates logs on request; it needs a roll-up before tens of millions of logs.

## Blocked on things outside this environment

ClamAV public signatures and the Go vulnerability database (network); a Docker daemon (image build and scan run only on GitHub); a real identity provider, bank, messaging provider and model endpoint; a field mobile client; professional review of roles, accounting policy, tax and payroll rules.

## Next checkpoint, exactly

Feature work is suspended by the audit directive. In order:

1. **People:** enable branch protection on `main` and bring the fixes to `main` through a reviewed pull request (`AUDIT_REMEDIATION_2026-11-15.md`, section 4).
2. Confirm three consecutive passing runs of the release-candidate revision (`execution-state.json`, `ci.release_candidate`).
3. Document keys: versions, rotation and recovery are built (ADR 0021). Still open: re-encryption of existing files, KMS/HSM custody.
4. Stale-signature alerting for the scanner; load, soak and fault-injection runs; penetration-test scope and environment.
5. Only then: fleet part 2 (fuel), as described in `CHANGELOG.md`.

## Resume

```text
Read CLAUDE.md and docs/engineering/execution-state.json, check the latest GitHub Actions run for this branch, reproduce the baseline (457 tests, none skipped: `tools/ci/check_test_results.py`) per docs/engineering/RUNBOOK.md, then take the first open item of "Next checkpoint" in docs/engineering/HANDOVER.md. Do not start feature work while the stabilisation gate is open.
```
