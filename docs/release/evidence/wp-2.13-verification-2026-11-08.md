# WP-2.13 Fiscal calendar, recurring journals, accruals, overhead: verification, 2026-11-08

SRS: FIN-10, FIN-11, FIN-12, FIN-13. Migration `0058_period_end.sql`; `internal/kernel/periodend.go`; routes
`GET /v1/fiscal-periods`, `POST /v1/fiscal-years`, `GET /v1/period-end`, `POST /v1/recurring-journals` (+ `/run`),
`POST /v1/accruals` (+ `/reverse-due`), `GET /v1/overhead/preview`, `POST /v1/overhead-allocations`; console
**Accounting → Period end** (Fiscal calendar, Recurring journals, Accruals, Overhead allocation).

## Tests

| Test | Proves |
|---|---|
| TestEthiopianFiscalYear | EFY 2019 = 8 July 2026 to 7 July 2027; Hamle 8 Jul–6 Aug; Nehase (with Pagume) 7 Aug–10 Sep; Meskerem from 11 Sep; no gaps; 1 Meskerem 2016 = 12 Sep 2023 (leap rule); Gregorian 2028 February ends on the 29th |
| TestPeriodEnd_FiscalYearRecurringAccrualOverhead | a project manager cannot generate periods; a year overlapping FY2026-Q4 refused in plain words; EFY 2021 generated (12 periods); an unbalanced template refused; rent template from 5 Oct to 30 Nov makes exactly two drafts, and a second run makes none; a template cannot move back; a draft accrual is not reversed; once posted it is reversed once, dated 1 Nov; overhead 12,000 allocated entirely to the one project with posted direct cost (50,000); a second allocation of the same pool and range and a forged share refused |

## Browser check (demo on :8080)

`u-controller`: EFY 2020 refused ("overlaps existing periods: FY2027-Q3, FY2027-Q4"); the hint read "EFY 2021 runs
from 1 Hamle 2020 to 30 Sene 2021; Pagume is part of Nehase"; EFY 2021 generated (17 periods listed). `u-accountant`:
recurring *Yard rent* (30,000 on the 5th) saved; *Create the journals due today* made the November draft and moved the
next date to 5 December. Console: only the intended refusal (422).

## Demo data added

Periods EFY2021-01 to EFY2021-12 (July 2028 to July 2029); recurring journal REC-2026-000001 and its November draft.

## Pending

Corporate overhead budgets; a labour-hours driver (needs timesheets, WP-3.3).

## Full suite

`go vet ./...` and `go test ./... -count=1` with PostgreSQL 16 and the Chrome browser journeys: all 18 packages with tests
`ok`, 0 FAIL, exit 0.
