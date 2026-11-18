# SERK Construction OS 4.0: the .NET workspace

The isolated C#/.NET 10 migration workspace for SERK Construction OS (ADR 0026,
`../docs/adr/0026-dotnet-transformation-workspace.md`).
- **Status:** INTEGRATION_BLOCKED, PRODUCTION_NO_GO.
- **Go system:** the Go system in the repository root is authoritative and unchanged.

## Build and test

```bash
# .NET SDK 10.0.1xx (global.json); Go for the differential oracle; a disposable PostgreSQL 16 for persistence tests
dotnet build SerkOS.slnx
SERK_DOTNET_TEST_ADMIN_DSN='Host=127.0.0.1;Port=5432;Username=postgres;Password=…;Database=postgres' \
  dotnet test SerkOS.slnx            # all suites; the differential suite runs `go run ./dotnet/differential/go-oracle`
python3 tools/traceability.py --check   # the migration register is current
```

The persistence tests need `SERK_DOTNET_TEST_ADMIN_DSN`, and the differential tests need Go. Without them those tests
fail; nothing is skipped.

## Where things are

- `docs/ARCHITECTURE.md`: modules, ownership, rules, database plan, dependency order.
- `docs/MIGRATION_TRACEABILITY.md` and `.json`: every Go requirement, package, migration, table and route, with its C#
  destination and migration status. Generated; edit `docs/migration-status.json`.
- `docs/DIFFERENTIAL.md`: the Go-to-C# comparison and the documented deviations awaiting owner approval.
- `docs/DEPENDENCIES.md`: the package review.
- `docs/CHECKPOINT-*.md`: evidence-backed checkpoints.
