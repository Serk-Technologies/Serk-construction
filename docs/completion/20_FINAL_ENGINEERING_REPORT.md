# Engineering report for the completion directive (status at 2026-11-15)

This is a status report, not a completion report. The directive's scope is 80 business domains; this cycle reproduced the baseline, did what governance work the agent's permissions allow, and delivered two fleet slices. Release decision: **NO-GO**.

The six states the directive distinguishes, as they stand:

| State | Where the system is |
|---|---|
| Implemented | See section 3 |
| Automated verified | 97 of 191 registered requirements (named tests passed in executed runs) |
| Independently reviewed | Nothing in this cycle was reviewed by anyone other than the engineering agent |
| Externally integrated | Object storage and ClamAV against real servers in CI. No identity provider, bank, messaging, KMS/HSM, AI provider or government interface |
| Professionally accepted | Nothing |
| Authorised | Nothing (`19_HUMAN_AUTHORIZATION.md` is unsigned) |

## 1. Starting condition

Branch `claude/new-session-xkd5g3` at `711a174`; `main` at `1096bf2` (CI failing, unprotected). 684 tracked files, 246 Go files (127 tests), 54 migrations: as the directive stated. Baseline reproduced on a fresh database over password-authenticated TCP with real ClamAV, MinIO and Playwright: 460 passed, 0 failed, 0 skipped (`../engineering/evidence/2026-11-15-baseline-711a174-fresh-db.json`).

## 2. Final revision of this cycle

The commit that contains this file, following `5c3c2a8` (maintenance). GitHub Actions: runs 29 (`7fdaaf9`) and 30 (`aa3c44f`, fuel) passed all six jobs. The run for the maintenance commit is recorded in `../engineering/execution-state.json` when read.

## 3. Implemented in this cycle

| Slice | Migration | Decision | Evidence |
|---|---|---|---|
| Fleet fuel on the stock ledger (FLT-02) | 0055 | ADR 0022 | `TestFuel_*`, `TestFuelFlags`, `TestAPIFuel`, browser journey; full run 464/0/0 |
| Fleet maintenance work orders (FLT-03) | 0056 | ADR 0023 | `TestMaintenance_*`, `TestDueState`, `TestAPIMaintenance`, browser journey; full run 468/0/0 |
| Phase 0 and governance tooling | — | — | Generated inventory, 80-domain plan in 13 waves, traceability; `scripts/prepush.sh` with CI's Gitleaks; unsigned authorisation record |

Both slices post nothing of their own: fuel and parts are stock issues, valued and charged by the existing ledger.

## 4. Outstanding domains

By the register: 38 domains partly built, 40 not started, 2 blocked. No domain is complete: none has had its requirement list reviewed by an owner. The order of work and what each wave needs from a person are in `02_DOMAIN_MAP.md`. Not started at all: estimating and tendering beyond BOQ, design/BIM/GIS, people and payroll, quality/laboratory/HSE, estate, most of the common data environment beyond documents and RFIs, the AI gateway, the field client.

## 5. Requirement-level status

191 requirements: VERIFIED 97, IMPLEMENTING 9, IMPLEMENTED_UNVERIFIED 1, BLOCKED 9, NOT_STARTED 75. The total grew from 180 because fuel and maintenance were itemised into testable requirements; nothing was removed or downgraded. No percentage is given: requirements differ greatly in size, and the 75 not started include whole product families.

## 6. Architecture

Unchanged in shape (modular monolith; kernel, store, transport). One consolidation: ordinary stock issues, fuel dispenses and maintenance parts share a single insert path. Frontend modularisation, TypeScript evaluation and registry-driven subject functions were **not** done.

## 7 to 9. Migrations, API, console

56 migrations (two added, additive). Routes and OpenAPI paths are counted in `00_ENGINEERING_INVENTORY.md`; a test keeps the specification equal to the served routes. Console: fuel dispensing form and reconciliation table, work orders as a document type with actions, forms for work orders, parts and plans, maintenance-due table.

## 10. Tests

468 top-level tests, 0 failed, 0 skipped, 34 mandatory by name, on a fresh database with the race detector and random order (`../engineering/evidence/2026-11-15-fleet-maintenance.summary.json`). Seven real-browser journeys with accessibility audits. Not present: property-based and fuzz tests for the new code, load tests, offline client tests, AI governance tests beyond the draft-only boundary.

## 11. Security

- No new finding in this cycle. Both slices were tested against a tenant database login writing around the kernel, another tenant, and staff of another project.
- Two places in the first maintenance draft relied on a session setting a tenant login could have forged; they were replaced before commit by checks against attested rows in the same transaction.
- Open, unchanged: single-custody keys in environment secrets; no re-encryption of existing documents; no real identity provider; no penetration test; `main` unprotected.

## 12. Performance

No new measurement. The only figures remain those of `../engineering/CAPACITY_PLAN.md` (one session, 100,000 machines, 1,000,000 logs). The fuel reconciliation and maintenance history aggregate on request and are bounded by period and scope; neither has been measured at volume. No load or soak test exists.

## 13. Backup and recovery

Not rerun in this cycle. Last drill: database point-in-time recovery with 54 migrations (PASS). Documents and their keys are not yet part of a drill, so a complete-system restore is unproven.

## 14. Integration (golden journeys)

Of the directive's fifteen journeys: procure-to-pay, material-to-job-cost and contract-to-cash were already verified end to end; "fleet to project cost" now reaches fuel cost on the project through the stock issue (charge-out is not built); "breakdown to restoration" is verified from report to certified release with parts. The other ten are not built or only partly built.

## 15. In-house deployment

Not performed. Deployment profiles were not updated for object storage, the scanner and key versions; no synthetic UAT environment was provisioned in this cycle.

## 16. Known defects and limits of what was built

Fuel: the 10% threshold and the choice of standard at period end are engineering defaults; no recorded investigation of a variance. Maintenance: no labour cost; a part for a machine in the yard must name a project; plans cannot be edited or retired; DUE_SOON threshold is a default. See ADR 0022 and 0023.

## 17. External blockers

Repository administration (branch protection, reviewed merge to `main`; no authorisation to open a pull request has been given). Identity provider, KMS/HSM, bank, messaging and AI provider accounts. Statutory payroll, tax and labour rules. Accounting policies for charge-out, depreciation and revenue.

## 18. Human review needed next

Plant manager and financial controller: fuel threshold, charge-out and depreciation policy (blocks FLT-04 and FLT-05). Security reviewer: this cycle's migrations 0055 and 0056. Repository administrator: section 4 of `../engineering/AUDIT_REMEDIATION_2026-11-15.md`.

## 19. Commands

```
# services and database: docs/engineering/RUNBOOK.md
go mod verify && go vet ./...
go test -race -count=1 -p 1 -shuffle=on -json ./... > run.json
python3 tools/ci/check_test_results.py run.json --mandatory tools/ci/mandatory_tests.txt --min-pass 466
GITLEAKS=/path/to/gitleaks scripts/prepush.sh
python3 tools/requirements_register.py --results <tests.txt> --commit <hash> && python3 tools/completion_map.py
```

## 20. Recommendation

**Not ready to be proposed as an internal acceptance candidate for SERK Construction OS 3.0 as the directive defines it.** The definition of done requires in-scope modules implemented end to end, a deployed test environment, performance evidence and a human acceptance package; most of the 80 domains are not built. What exists is a verified foundation (finance, procurement, stock, contracts, documents, fleet register, fuel and maintenance) that could be offered for controlled technical testing of **those modules only**, after a human takes decision 1 in `19_HUMAN_AUTHORIZATION.md`. Production remains NO-GO.
