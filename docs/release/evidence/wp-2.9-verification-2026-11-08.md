# WP-2.9 Fixed assets: verification, 2026-11-08

SRS: FIN-06, EQP-01 (IAS 16, cost model). Migration `0057_fixed_assets.sql`; `internal/kernel/fixedassets.go`;
`internal/httpapi/asset_handlers.go`; console **Accounting → Fixed assets** (Register, Depreciation, Classes) and a
disposal panel on each active asset. Permissions: `asset.manage` (accountant), `asset.approve` (CFO; not the finance
manager, who approves the generated journals). New duty-conflict pair "Fixed assets".

## Fixture (TestFixedAssets_CapitaliseDepreciateDispose)

| Step | Expected and proven |
|---|---|
| Class with an expense account as accumulated depreciation | refused by the database |
| Excavator 1,200,000, residual 120,000, 60 months, straight line; activation | FA_CAP Dr 1500 / Cr 1550 1,200,000 (accountant cannot activate own asset; CFO activates) |
| Survey kit 600,000, 48 months, double declining | October 25,000 (600,000 × 2/48); November 23,958.33 (575,000 × 2/48) |
| October run | 43,000: journal Dr 6100 / Cr 1590 43,000; forged depreciation line refused; same month twice refused; next month refused while the journal is a draft |
| November run | the accountant cannot cancel; finance cancels (journal cancelled with it); rerun posted |
| Disposal of the excavator for 1,000,000 | accumulated 36,000, net book value 1,164,000, loss 164,000: Dr 1100 1,000,000, Dr 1590 36,000, Dr 4900 164,000 / Cr 1500 1,200,000 |
| After disposal | the excavator is no longer depreciated; direct status change to DISPOSED refused; a project manager cannot read the register |

## Browser check (demo on :8080)

`u-accountant` added class PLANT (1500, 1590, 6100, 4900), registered *Excavator CAT 320* (1,200,000, residual
120,000, in service 5 Oct 2026, from 1550) and submitted it; `u-cfo` activated it from *Assets to activate*. The
October preview showed 18,000; the run DEP-2026-000001 was prepared with its draft journal; the register shows cost
1,200,000, accumulated 18,000, net book value 1,182,000. No console errors.

## Demo data added

Accounts 1500, 1590, 1550, 6100, 4900 (also in the seed); class PLANT; asset FA-2026-000001; run DEP-2026-000001
(journal draft) and the asset's capitalisation journal (draft).

## Not built

Revaluation, impairment, transfers between entities or projects, the units-of-production method, capitalising
directly from a goods-receipt line.

## Full suite

`go vet ./...`, the role matrix regenerated, and `go test ./... -count=1` with PostgreSQL 16 and the Chrome browser
journeys: all 18 packages with tests `ok`, 0 FAIL, exit 0.
