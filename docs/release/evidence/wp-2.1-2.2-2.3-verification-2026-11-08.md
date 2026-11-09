# WP-2.1 completion, WP-2.2 completion and WP-2.3: verification, 2026-11-08

SRS: PCC-04, PCC-05, PCC-10 (forecasting, CPI method, S-curves, variance), PCC-06 (WIP over and under billing and
its month-end journal), FIN-15 and CON-07 (revenue over time, contract assets and liabilities).

## Delivered

| Package | What |
|---|---|
| WP-2.1 | `0052_forecast_cpi.sql`: method **CPI** (ETC = (budget − budget × EV/BAC) ÷ CPI, from `schedule_evm` at the forecast date; refused without a baseline with approved progress and posted cost); `prev_eac` freezes each code's EAC in the last approved forecast; a line that moved by more than 10% of its budget needs an explanation before submission (database guard). `GET /v1/projects/{id}/s-curve`; an **S-curve** chart on every forecast (planned, earned and actual cost by month, scheduled budget, latest approved forecast, CPI and SPI); *Last approved EAC* and *Change* columns. |
| WP-2.2 / 2.3 | `0053_revenue_recognition.sql`, `internal/kernel/revenue.go`: revenue policy per legal entity (cost-to-cost or measured output; contract asset and liability accounts; controller proposes, CFO approves with fresh MFA, attested; approving retires the previous version). Revenue run per project and month end: earned, billed (approved invoices less approved credit notes), cumulative adjustment, movement since the last posted run, all computed by the database (a forged line is refused). The run generates a **REV_RUN** draft journal under the subledger protections (lines written only by the generating transaction, re-checked at approval and posting, never reversed, reserved idempotency key); finance (journal approve/post) may cancel a draft run with its journal; one unposted run per project. Console **Accounting → Revenue & WIP**; permission `revenue.run` (project accountant). Contract asset (1160) and liability (2260) accounts added to the seed. |

## Tests

| Test | Proves |
|---|---|
| TestForecast_CPIChangeAndSCurve | CPI refused without a baseline; CPI ETC equals (budget − budget × EV/BAC) ÷ CPI (within one cent of an independent computation); the change rule refuses an unexplained move; S-curve figures (BAC 6,500,000; PV and EV of October 2026); outsider refused |
| TestForecast_MethodsAndGuards (updated) | the change rule applies to a second forecast of the same month; both moved lines must be explained |
| TestRevenue_RunAndWIPJournal | policy accounts typed; maker/checker and fresh MFA; preview figures (progress = cost ÷ EAC, earned, billed 5,310,000); journal lines equal the run's accounting (Cr contract liability / Dr revenue for over-billing); a line edit and a forged revenue line refused; the preparer cannot cancel, finance can, and the journal is cancelled with the run; the rerun is approved and posted by finance; a posted run cannot be cancelled; nothing left to adjust is refused; outsider refused |
| TestPrivileges_CatalogInvariants | every redefined subledger function pins `pg_temp` last |

## Browser check (demo on :8080)

- **Forecast** FC-2026-11 (P-001): S-curve with planned value from August to December 2026, earned value 2,210,000
  at the November progress, SPI 0.6083, no CPI yet (no posted cost), scheduled budget and the whole-project forecast as
  dashed lines; *Last approved EAC* and *Change* columns; day and night themes. Fixed during the check: the last month
  label was clipped, and the dashed lines now say what they cover (scheduled budget vs. whole project).
- **Revenue & WIP**: `u-controller` proposed the policy (1160 / 2260, cost to cost), `u-cfo` approved it.
  `u-project-accountant` saw 0% progress (no posted cost), 1,830,000 billed → contract liability 1,830,000 and
  prepared REV-2026-000001 with a draft journal (Dr revenue / Cr contract liability 1,830,000). An earlier attempt
  with no billing was refused in plain words ("nothing to adjust"). Fixed during the check: the policy list named a
  non-existent column (caught by the browser, then added to the kernel test); the project accountant (no `p2p.read`)
  had no project list, so `GET /v1/revenue/projects` was added; the preview is now open to ledger readers.

## Demo data added

- Approved BOQ for P-001 (earthworks, structures, pavement, 11,500,000 on the three cost codes) and a baseline
  schedule *Construction programme rev A* (4,000,000 over activities A–E, 3 Aug – 23 Dec 2026) with approved progress
  at 7 Nov 2026. Two refused draft schedules (rev B, rev C) remain as drafts.
- Customer invoice for IPC-2026-000001 (approved; its AR journal is a draft for finance).
- Accounts 1160 Contract assets and 2260 Contract liabilities; revenue policy v1 (retired) and v2 (approved);
  revenue run REV-2026-000001 with its draft journal.

## Full suite

`go vet ./...`, the role matrix regenerated (`UPDATE_ROLE_DOC=1 go test ./pkg/auth`), and `go test ./... -count=1` with
PostgreSQL 16 and the Chrome browser journeys: every package `ok`, 0 FAIL, exit 0.
