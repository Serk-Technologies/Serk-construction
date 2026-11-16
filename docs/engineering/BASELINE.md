# Baseline (M0) — 2026-11-15

What the repository was, and what was measured, before any change of this session. Later results are in `HANDOVER.md` and `evidence/`.

## Repository

| Item | Value |
|---|---|
| Git root / remote | `Serk-Technologies/Serk-construction` |
| Baseline commit | `fad2cd4` (merge of PR #2, "building R2 fully") |
| Working branch | `claude/new-session-xkd5g3` |
| Kind of repository | An existing SERK Construction OS, not a new or unrelated project |
| Tracked files / Go lines | 584 files, about 60,000 lines of Go |
| Language and store | Go 1.26 (module `github.com/serk-group/serk-construction`), PostgreSQL 16 |
| Migrations | 51 (`0001`–`0051`), additive, checksum-locked |
| API | 270 paths in `api/openapi.yaml`, contract-tested against the served routes |
| Front end | Static single-page console in `internal/webui` (no framework), served at `/console/` |
| Mobile app | None. The offline sync API exists (`/v1/sync`) |
| CI | `.github/workflows/ci.yml` (lint, supply chain, tests on PostgreSQL, image build). Not executed in this session |
| Existing instructions | No `CLAUDE.md` or `AGENTS.md` before this session |
| Secrets | None committed that I found; development compose uses published dev-only secrets the API refuses outside development |

## What already exists

The repository's own records are detailed and were found consistent with the code: `docs/architecture/MODULES.md` (module map), `docs/BACKLOG.md` (gap register), `docs/release/CHECKPOINT_0.7.0.md` and `docs/release/GO_NO_GO.md` (decision: **NO-GO**, not production ready).

Built and tested before this session, in the order of `MASTER_PROMPT.md` milestones:

- **M1 foundation**: tenant isolation by database login and row-level security, roles and separation of duties, attested approvals with an isolated signer, hash-chained audit, outbox and relay, workflow engine, sessions and abuse limits.
- **M2 project/estimate**: projects with stage gates, WBS and cost codes, BOQ versions with import, budgets with revisions and transfers. No rate build-up, tender or CRM.
- **M3 supply/stock/GL**: requisition to payment with three-way match, stock valuation (FIFO/WAC), transfers, reservations, ledger with FX, bank reconciliation.
- **M4 field/IPC**: contracts, measurements, payment certificates, invoices, receipts, retention, credit notes, variations, change events, claims, guarantees, price adjustment, dashboards and 19 catalogued reports.
- **M5 offline**: server side only (devices, idempotent command log, conflicts).
- **M6**: CPM scheduling, baselines, progress and earned value only.
- **M7–M12**: document control, quality, HSE, fleet, payroll, real estate, BIM/GIS, AI gateway, cells, archive and localisation packs were absent.

## Baseline checks (executed)

Environment: Linux container, 4 vCPU, 15 GiB RAM, Go 1.26.0, PostgreSQL 16.15 (disposable local cluster, `fsync=off`), Node 22 with Playwright and Chromium.

| Check | Command | Result |
|---|---|---|
| Format | `gofmt -l .` | clean |
| Vet | `go vet ./...` | exit 0 |
| Full suite | `SERK_TEST_ADMIN_DSN=… go test -race -count=1 -p 1 -json ./...` | **427 passed, 0 failed, 0 skipped**; 18 packages with tests |
| Browser journeys | included above (`TestConsoleBrowser*`, real Chromium) | passed |

Per-test results: `evidence/2026-11-15-baseline.tests.txt`. Longest packages: `internal/kernel` 90 s, `internal/httpapi` 45 s.

Not run at baseline: the GitHub CI workflow, Docker image build, `govulncheck`, the offsite backup drill, any load test, any deployment.

## Risks found at baseline

1. The product scope in the Bible (80 domains) is several times what exists; see `REQUIREMENTS.md` for the count.
2. `docs/STATUS.md` and `README.md` still describe release 0.2.0; the current state is in `docs/BACKLOG.md` and the checkpoints.
3. Everything external is unproven: no real identity provider, bank, object storage, message provider or model endpoint has been connected.
4. No load or scale measurement exists beyond an abuse-limit test.
