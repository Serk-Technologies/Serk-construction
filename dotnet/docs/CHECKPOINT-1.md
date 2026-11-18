# Checkpoint 1: discovery, workspace, exact kernel, first differential module (2026-11-18)

**Status: INTEGRATION_BLOCKED, PRODUCTION_NO_GO.** Nothing was deployed, no shared or production database was
touched, and no Go file outside `dotnet/` changed.

## Source

- Go reference: `Serk-Technologies/Serk-construction`, branch `claude/new-session-xkd5g3`, commit `59718e9`.
  Its last full fresh-database run, at `9cfe480` (the same code), had 531 passed, 0 failed, 0 skipped; that run is
  historical evidence, not re-run here.
- Go baseline from the live register:
  - 80 domains plus 7 cross-cutting areas, 218 listed requirements: VERIFIED 123, NOT_STARTED 62, IMPLEMENTING 23,
    BLOCKED 9, IMPLEMENTED_UNVERIFIED 1;
  - 37 Go packages, 97 SQL migrations, 236 tables, 603 HTTP routes;
  - open defects: DEF-002 (High, owner: branch protection), DEF-005, DEF-007, DEF-020.
- Not reachable or not supplied: `SERK-5D-Engineering-Lab` (Titan), the BIMCraft source, and the text of
  AUD-001..AUD-028. Recorded in `docs/unavailable-inputs.json`.

## What was done

1. **Discovery and traceability.** `tools/traceability.py` reads the repository and maps every requirement, package,
   migration, table and route to a C# destination module with a migration status. All rows are assigned (no
   UNASSIGNED); 2 packages are DIFFERENTIAL_PASS and everything else is NOT_STARTED.
2. **Architecture.** ADR 0026 (`../docs/adr/0026-dotnet-transformation-workspace.md`) and `docs/ARCHITECTURE.md`:
   - a modular monolith of 14 modules plus Estate (proposed, pending owner decision);
   - the ownership matrix and dependency order;
   - the database compatibility plan.
3. **Workspace.** `SerkOS.slnx` on .NET 10 (SDK 10.0.115):
   - 3 platform projects, 14 module shells and 4 test projects;
   - central, locked package versions and nuget.org source mapping;
   - warnings as errors and deterministic builds.
4. **Exact kernel** (`Serk.Platform.Numerics`, base class library only):
   - BigInteger `Rational` with six rounding modes and a strict grammar for untrusted input (400 characters);
   - `ParseCanonical` for stored values, consistent with the 65,536-bit `ComputeBudget`;
   - `Dimension`/`Unit`/`Quantity` with exact and approximate (error-bounded) quantities;
   - `Money`/`Currency` with one-step rounding and exact allocation;
   - versioned `RoundingPolicy`, `Canonical` serialization, `CalculationRecord` provenance with SHA-256, and exact
     plane geometry with explicit tolerances.
5. **Rational persistence.** `db/0001_serk_rational.sql` is a proposed domain in which the database itself refuses
   non-canonical spellings and compares exactly. It is tested on a real PostgreSQL 16 in a scratch database per run.
6. **Differential harness.**
   - The Go oracle runs the real `pkg/money` and `pkg/ethcal`, isolating non-terminating cases.
   - The C# side evaluates the same 6,519 seeded cases.
   - Every mismatch must be explained by recomputing Go's mechanism.

## Tests executed (this checkpoint)

From `docs/evidence/2026-11-18-checkpoint-1-dotnet-test.txt` (clean build: 0 warnings, 0 errors):

| Suite | Passed | Failed | Skipped |
|---|---|---|---|
| Serk.Architecture.Tests | 5 | 0 | 0 |
| Serk.Platform.Numerics.Tests (unit and FsCheck property tests) | 84 | 0 | 0 |
| Serk.Platform.Persistence.Tests (PostgreSQL 16.15) | 12 | 0 | 0 |
| Serk.Differential.Tests (Go 1.26.11 vs C#; 6,519 cases) | 1 | 0 | 0 |

The architecture rule was shown to fail when broken: a temporary Supply → Capital reference failed
`Modules_meet_only_through_published_Api_projects`, and it passed again once removed.

## Findings

- **Go `pkg/money` Allocate (DIFF-002, Medium).**
  - Past 2^63 minor units it returns wrong amounts or never terminates. Both were observed, including
    1,000,000,000,000.00 ETB split 100000 : 33333 : 66667.
  - No production caller today. The fix in Go is the owner's call; the C# kernel is exact.
- **Go `pkg/money` Div (DIFF-001, Low).** Double rounding through a 16-place intermediate. The same pattern exists at
  kernel `decimal.Div` call sites, to review per module.
- **Strict parsing (DIFF-003, DIFF-004, DIFF-005, Low).**
  - Go accepts a signed Ethiopian year, leading zeros and a signed zero.
  - Go converts Ethiopian years 9992–9999 to Gregorian years beyond 9999.
- **Found and fixed in the kernel during this checkpoint:**
  - canonical text longer than the input limit would not have read back from the database (now `ParseCanonical`);
  - a PostgreSQL regular-expression repetition limit broke the first domain draft.

## Known limitations

- No business module is migrated: all 218 requirements are NOT_STARTED in .NET. Module projects are shells
  carrying their owner and status.
- Deviations DIFF-001..005 need owner approval before pkg/money and pkg/ethcal count as MIGRATED.
- The persistence tests use a disposable local PostgreSQL named by an environment variable, not Testcontainers (no
  container runtime here).
- Titan, BIMCraft and AUD-001..028 are blocked on access. No Blazor, Avalonia, MAUI, SignalR, gRPC, OpenTelemetry or
  AI work has started.
- The workspace sits inside the Go repository's working branch because this session cannot create a repository.

## Exact next action

Differential migration of `pkg/ledger` (journal validation, balancing, state machine, reversal), using the same
harness, into `Serk.Modules.Capital` with a `Serk.Modules.Capital.Api`. Then `pkg/fx` and `pkg/tax`, followed by
the first database-level differential test: the same journal posted through the Go API and the .NET path on
isolated clones, comparing rows, attestations and refusals.
