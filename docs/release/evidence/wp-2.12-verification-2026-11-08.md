# WP-2.12 Cash forecasting: verification, 2026-11-08

SRS: TRS-02. Migration `0059_cash_plan.sql`; `internal/kernel/cashforecast.go`; routes `GET /v1/cash-forecast`,
`POST /v1/cash-plan`, `POST /v1/cash-plan/{id}/cancel`; console **Treasury & banking → Cash forecast** (tiles,
13-week chart, week table, item list with links to the documents, planned items).

## Test (TestCashForecast_ThirteenWeeks)

- Sources present: customer invoice (confirmed in), supplier invoice (confirmed out), client certificate not yet invoiced (forecast in), purchase order not yet invoiced (forecast out), planned loan.
- Thirteen weeks; the last closing balance equals opening cash plus every item inside the horizon; the net after the horizon equals the items beyond it (−1,000,000, a planned repayment at 200 days).
- Confirmed inflows by week equal the confirmed inflow items.
- A QS cannot add planned items; cancelling needs a reason; a cancelled loan leaves the plan history (two items) and lowers the 13-week balance by exactly 5,000,000; a cancelled item cannot be reactivated by a direct update; a project-scoped QS cannot read the forecast.

## Browser check (demo on :8080)

`u-treasury`: cash now 0 (the demo's bank accounts have no posted balance), 1,890,600 in 13 weeks, lowest −122,400 in
week 1 (the released subcontract payment). Adding a planned loan drawdown of 2,500,000 raised the 13-week balance to
4,390,600. The night theme was checked as `u-cfo`; no console errors. Fixed during the check: the balance line had no
scale, so the left axis is now labelled and the legend explains bars and line.

## Demo data added

A planned item: a 2,500,000 working-capital loan drawdown (synthetic), due in 14 days.

## Pending

The project-horizon (monthly) view; payroll flows arrive with WP-2.8 (until then, as planned items).

## Full suite

`go vet ./...` and `go test ./... -count=1` with PostgreSQL 16 and the Chrome browser journeys: all 18 packages with tests
`ok`, 0 FAIL, exit 0.
