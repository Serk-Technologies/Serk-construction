# Differential migration testing

`tests/Serk.Differential.Tests` runs the Go reference (`differential/go-oracle`, which calls the real Go packages) and the
C# implementation on the same generated cases, and compares canonical results.

A mismatch passes only if a documented deviation below explains it. The explanation is checked by recomputing Go's
mechanism and getting Go's exact answer, not by allowing a category of differences. Every documented deviation must
also occur in the run, so the list cannot go stale. Missing Go fails the test; nothing is skipped.

Cases that never terminate are run in a child process and killed after 3 seconds (`nontermination`).

## Run 2026-11-18: pkg/money and pkg/ethcal

Seed 20261118, 6,519 cases; 6,373 agree exactly and 146 differ, all explained. Per operation:

| Operation | Agree | Differ (explained) |
|---|---|---|
| money.parse_strict | 518 | 103 (DIFF-004) |
| money.round | 1,548 | 0 |
| money.mul | 601 | 0 |
| money.div | 772 | 30 (DIFF-001) |
| money.allocate | 807 | 9 (DIFF-002) |
| eth.from_jdn | 600 | 0 |
| eth.from_gregorian | 608 | 0 |
| eth.to_gregorian | 610 | 1 (DIFF-005) |
| eth.parse | 309 | 3 (DIFF-003) |

The report of the run is `differential/reports/2026-11-18-money-ethcal.json`.

## Documented deviations

Each one needs the owner's approval of the corrected behaviour before pkg/money or pkg/ethcal counts as MIGRATED. Until
then their status is DIFFERENTIAL_PASS.

| Id | Go behaviour (observed) | C# behaviour | Severity in Go | Recommendation |
|---|---|---|---|---|
| DIFF-001 | `Money.Div` rounds the quotient to 16 places (half away from zero), then to the minor unit. Example: 0.01 ETB ÷ 2.0000000000000000001, HALF_UP → Go 0.01; exact 0.0049999… → 0.00 | One rounding of the exact quotient | Low: `Money.Div` has no production caller. The same 16-digit intermediate exists wherever kernel code calls `decimal.Div` | Approve the exact behaviour. Review `decimal.Div` call sites when Fleet, Plan and Intelligence migrate |
| DIFF-002 | `Money.Allocate` uses int64. Past 2^63 minor units (amount × ratio) it silently returns wrong amounts (999,999,999,999,999,999.99 ETB ÷ 3 → 25,887,598,771,507,473.07 + …) or loops about 2^63 times, hanging a CPU | Exact BigInteger allocation, conserving every minor unit | Medium for the library (data corruption or denial of service). Both outcomes were observed in the run, including at a plausible size: 1,000,000,000,000.00 ETB split 100000 : 33333 : 66667 never terminates. `Money.Allocate` has no production caller today | Approve the exact behaviour. Fix pkg/money in Go too (separate change, owner's call) |
| DIFF-003 | `ethcal.Parse` (fmt.Sscanf) accepts a signed year: "+016-01-01" → year 16 | Exactly YYYY-MM-DD digits | Low | Approve the strict parser |
| DIFF-004 | `money.ParseStrict` accepts leading zeros and a signed zero ("007.50", "-0.00") | One spelling per value | Low | Approve the strict grammar. Note that an import or API client sending such spellings would be refused after cutover |
| DIFF-005 | Ethiopian years 9992–9999 convert to Gregorian dates after 9999-12-31 (Go `time.Time`) | `DateOnly` ends at 9999-12-31 (out of range) | None in practice | Approve. Restrict valid Ethiopian years to ≤ 9991 at the API in both systems |

## Next differential targets (pure Go packages, in order)

`pkg/ledger` (journal validation, reversal), `pkg/fx` (rate selection, conversion), `pkg/tax`, `pkg/schedule` (CPM),
`pkg/evm`, `pkg/fleet`, `pkg/workflow`, `pkg/auth` (role catalogue and duty conflicts). After those come database-level
differential tests on isolated clones: the same command through the Go API and the .NET API, comparing rows written,
attestations and refusals.
