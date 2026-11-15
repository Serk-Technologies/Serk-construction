# ADR 0025 — Integrating the unmerged new-version line

Status: accepted by engineering on 2026-11-15; awaiting the repository owner's review (see "Owner questions").

## Context

Discovery for the final-completion directive found two commits on `new-version` (6cbcdac, 7ac2a6c, 2026-11-09,
by the repository owner) that were never merged into `main` or into this branch. They add about 21,000 lines:
migrations 0052–0067 with kernel, API, console pages, browser journeys and evidence for cost forecasting with
CPI, IFRS 15 revenue over time, change/variation links, subcontracts and subcontract payments, fixed assets,
period end, cash planning, role adjustments, payroll, consolidation, tax returns, workforce/equipment/HSE,
quality and site records, document control, and rules of credit.

This branch (the verified candidate, run 37) independently added migrations 0052–0057 (controlled documents,
document security, fleet, fuel, maintenance, corrections). The migration runner refuses duplicate versions,
and both lines define `documents`, `document_revisions`, `rfis` and `equipment` differently:

| Object | This branch | new-version |
|---|---|---|
| Document files | S3-compatible object storage, malware scan, envelope encryption with key versions (0052–0053, ADR 0018–0021) | `bytea` in PostgreSQL, 10 MiB |
| RFIs | maker/answerer/closer separation, revision link | raise/respond/close, change-event link |
| Equipment | register, lifecycle, reservations, operator authorisations, logs, meter resets, fuel on the stock ledger, maintenance with database-enforced separation (0054–0057) | register, usage, hire runs and hire journals, simple work orders |

new-version on its own (`go test -race -p 1 ./...`, fresh database, this environment): 438 passed, 10 failed.
The failures (posture and relay tests) are the ones this branch corrected for password-authenticated TCP
connections after the fork point; the integration carries this branch's versions.

## Decision

Integrate on this branch with a merge commit, so the owner's commits and authorship stay in history.

1. **Non-colliding migrations are carried byte-for-byte and renumbered** after 0057:

   | new-version | here |
   |---|---|
   | 0052_forecast_cpi | 0058 |
   | 0053_revenue_recognition | 0059 |
   | 0054_change_variation_links | 0060 |
   | 0055_subcontracts | 0061 |
   | 0056_subcontract_payments | 0062 |
   | 0057_fixed_assets | 0063 |
   | 0058_period_end | 0064 |
   | 0059_cash_plan | 0065 |
   | 0060_role_adjustments | 0066 |
   | 0061_payroll | 0067 |
   | 0062_consolidation | 0068 |
   | 0063_tax | 0069 |
   | 0065_quality_site | 0070 |
   | 0067_credit_rules | 0071 |

   Comments inside those files keep their original numbers. Byte identity is checked (sha256) against
   `origin/new-version`.

2. **0072_integration_subjects.sql** redefines the two shared subject functions (`p2p_subject_text`,
   `serk_subject_project`) as the union of both lines. Each carried migration redefines them from
   new-version's lineage, which lacks this branch's document and fleet types.

3. **Colliding parts are not carried as-is**: new-version 0064 (timesheets, equipment usage and hire,
   equipment work orders, HSE) and 0066 (documents, transmittals, RFIs, submittals), with their kernel files,
   handlers, console pages and browser journeys. This branch's document and equipment designs are kept
   because they carry the stronger controls (object storage, scanning, encryption; database-enforced
   maintenance separation). The new-version features that this branch lacks — timesheets, equipment hire
   charge-out, HSE, transmittals and submittals — are rebuilt on this branch's tables as follow-up slices,
   using new-version's code as the reference. They remain readable at `origin/new-version`.

4. Roles: permissions are the union of both lines, except permissions that only the excluded parts use
   (`doc.*`, `timesheet.*`, `equipment.record/approve`, `hse.report`); new-version's own `plant_manager` and
   `hse_officer` role entries are dropped for now (this branch's `plant_manager` stays).

5. The 15 MB `serk-seed` binary committed at the repository root is not carried; the seeder builds from
   `cmd/serk-seed`.

## Consequences

- Any database that applied new-version's migrations under their original numbers cannot be upgraded by
  this branch as-is (version 0052 means something else here). No such environment is known to the agent.
- Requirements the carried work may satisfy are verified only by tests that pass on this branch; no
  new-version evidence file is taken as verification.
- Until the follow-up slices land, timesheets, HSE, internal hire and transmittals/submittals are absent
  from this branch even though new-version had them.

## Owner questions

1. Has any environment (local, staging, Railway or other) applied new-version's migrations 0052–0067?
   If so, a mapping migration is needed before that database can follow this branch.
2. Is this branch's document and equipment design the one to keep (the engineering recommendation), with
   new-version's timesheets, hire, HSE, transmittals and submittals rebuilt on it?
