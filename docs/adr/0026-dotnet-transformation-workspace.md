# ADR 0026: The .NET transformation workspace (SERK Construction OS 4.0)

- Status: accepted for the isolated workspace only; cutover NOT approved
- Date: 2026-11-18
- Decision owner: SERK Technologies Group (the "SERK Construction OS 4.0 — .NET transformation directive")
- Supersedes: nothing. ADR 0001 (Go and PostgreSQL) stays in force for everything that runs.

## Context

The owner directed a transformation of SERK Construction OS (Go, this repository) and Project Titan
(`SERK-5D-Engineering-Lab`) into one C#/.NET 10 platform. The directive is explicit:
- the Go repositories are not deleted, overwritten, merged into or retired; they are the behavioural reference;
- there is no production deployment, integration or cutover without separate written owner approval;
- a module counts as migrated only when its differential and acceptance tests pass.

`CLAUDE.md` says the baseline is Go/PostgreSQL "unless ADR establishes otherwise" and "do not replace a valid repo with a
parallel scaffold". This ADR reconciles the two.

## Decision

1. **The Go system stays authoritative.** Every route, table, migration and test of this repository keeps working
   unchanged. The .NET code is not deployed and is never a second writer to any shared database
   (`INTEGRATION_BLOCKED`, `PRODUCTION_NO_GO`).
2. **The .NET workspace lives in `dotnet/`.** It is self-contained (its own solution `SerkOS.slnx`, central package versions,
   lock files and tests) and changes no Go file. Its only Go file is the differential oracle
   (`dotnet/differential/go-oracle`), which calls the Go packages read-only. The workspace can move to its own
   repository without changes once the owner creates one. This session can push only to this repository's working
   branch.
3. **Architecture: a modular monolith.**
   - Modules: Command, Estimate, Plan, Design, Supply, Stock, Fleet, People, Contracts, Capital, Assure, Connect,
     Intelligence and Governance, on platform projects (exact numerics, primitives, persistence).
   - A module reaches another only through that module's published `Serk.Modules.X.Api` project.
   - Platform never depends on a module, and the project graph is acyclic. `tests/Serk.Architecture.Tests` enforces
     all of this.
   - ESTATE (sales, facilities, commissioning, handover) is in the Go requirement register but not in the directive's
     module list. It is kept as "Estate (proposed)" in the traceability register until the owner decides; no requirement
     is dropped.
4. **The database stays PostgreSQL, and its history is preserved.**
   - The 97 Go migrations are the schema of record. EF Core never generates or alters that schema.
   - New .NET schema objects are proposed as reviewed, additive SQL in `dotnet/db/`. They are applied only to
     isolated test databases until a cutover plan is approved.
   - Financial and authorization invariants stay in PostgreSQL: forced row-level security, attested approvals, guard
     triggers, append-only tables.
5. **Exact numerics.** The kernel (`Serk.Platform.Numerics`) uses only the base class library:
   - BigInteger rationals; no `System.Decimal` and no floating rounding (enforced by an architecture test);
   - checked dimensions, explicit compute budgets and canonical serialization;
   - versioned rounding policies and calculation provenance.
6. **Differential migration.** For each module, the Go code is run on generated cases, the C# code on the same
   cases, and the canonical results are compared. A mismatch passes only if a documented deviation explains it by
   recomputing Go's mechanism and getting Go's exact answer. Known Go defects are documented, not copied, and each
   needs owner approval.

## Consequences

- Two code bases coexist on one branch until the owner moves the workspace. The Go CI checks the oracle with `go vet`
  and gofmt; the .NET suite runs separately (`dotnet/README.md`).
- Titan and BIMCraft are not reachable from this session. Their parts of the directive are BLOCKED until they are attached.
- Nothing in this ADR authorizes integration, deployment, production data or a cutover.
