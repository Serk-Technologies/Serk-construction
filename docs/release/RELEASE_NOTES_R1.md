# Release 1 notes (financial core and procure-to-pay) - DRAFT, 2026-11-07

Status: **not released.** The release decision recorded in `docs/release/GO_NO_GO.md` is **NO-GO** until the
external items below exist and named people sign. These notes say what the build contains, what changed for people
who already use it, and what is known to be missing.

## What release 1 contains

Everything in the earlier baseline (ledger, periods, FX, procure to pay, contract to cash, stock valuation, planning
and earned value, site diaries with offline sync, company portals, notifications, automation rules, intelligence,
exports) plus, in this release:

| Area | What is new | Where to look |
|---|---|---|
| Console | Scrollable, collapsible sidebar by category with a page finder; a "How this page works" card on every page | any page |
| Budget control | Budget revisions, transfers between cost codes, contingency drawdowns; current budget = original + approved changes; checked at every requisition and order | Projects and planning |
| Authority | Authority matrix (who may approve how much), delegation of authority, duty-conflict exceptions | Governance |
| Dashboard | 10 KPIs with formula, source and drill-down; role views; region and business-unit roll-ups | Overview |
| Reports | 15 more reports (25 in all), grouped, exportable to PDF, Word and Excel | Overview, Reports |
| Stores | Unit conversions, stock reservations, reorder rules and a low-stock report | Stores |
| Projects | Project master data and controlled stage gates; regions, business units, company calendar | Projects and planning, Governance |
| Setup | Company setup checklist; staged imports of suppliers, customers and stock items | Overview, Governance |
| Contract | Every route is described in `api/openapi.yaml` and a test keeps it so; the role matrix and the KPI and report catalogues are generated from the code | `docs/` |

## Changes that affect existing installations

1. **Migrations 0039 to 0043** apply on start. They are additive. Migration 0039 was rebuilt before release; a database
   that applied an earlier draft of it must be recreated (the migrator refuses a changed checksum).
2. **Projects created by people** now start PLANNED in phase BID and move through stage gates. Before, a company
   login could insert an active project directly with raw SQL; that gap is closed. Projects created by the operator's tooling
   and the seed are unchanged (ACTIVE, phase EXECUTION).
3. **The production image no longer builds the development tools.** Seed and dev-token tools live in
   `deploy/Dockerfile.devtools`.
4. **New permissions** (see `docs/security/ROLE_PERMISSION_MATRIX.md`): `budget.revise|approve|transfer`,
   `authority.manage|approve`, `delegation.create|approve`, `stock.reserve`, `org.manage`, `project.create|update`,
   `project.gate.submit|decide`. Custom roles must be reviewed; the seed roles already include them.
5. **Test sign-in** has two more personas (`u-buyer-2`, `u-ar-clerk-2`) so two-person flows can be exercised.

## Verification

The full suite passes against PostgreSQL 16 with `-race` (kernel, API, five real-browser journeys, database
forgery probes). Each work package has an evidence file in `docs/release/evidence/`. The test manual
(`testmanual.md`) has cases for every new area, and a UAT plan is in `UAT_PLAN_R1.md`.

## Known limits (not hidden)

- No real identity provider, so step-up MFA and session revocation are verified against test tokens only.
- No staging environment with TLS, vault and an HSM-held signing key; no observability stack; no external
  penetration test; no load or soak test. The decision stays NO-GO.
- The authority matrix does not yet cover journals, stock or contract approvals.
- Opening balances, open payables and receivables and opening stock cannot be imported yet.
- Dashboard and reports read live tables (no cached read model); large tenants need that work.
- The calendar is stored but not yet used by the scheduler; no Ethiopian holidays are seeded.
- New projects reach staff only when the identity provider assigns them; until then their creator and company-wide roles see them.
- Not built: subcontracts, payroll, equipment, document control, quality, HSE, mobile app, portals, claims, fixed assets,
  consolidation, tax returns (plan in `docs/IMPLEMENTATION_PLAN.md`).
