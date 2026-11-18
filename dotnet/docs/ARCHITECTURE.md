# SERK Construction OS 4.0 (.NET): migration architecture and ownership

Decision record: `docs/adr/0026-dotnet-transformation-workspace.md`. Status: **INTEGRATION_BLOCKED, PRODUCTION_NO_GO.**
The Go system in this repository stays authoritative. Nothing here runs against a shared or production database.

## Shape: a modular monolith

```
dotnet/
  src/Platform/Serk.Platform.Numerics     exact kernel: Rational, Quantity/Dimension/Unit, Money/Currency,
                                           strict grammar, ComputeBudget, RoundingPolicy, Canonical, provenance, Geometry
  src/Platform/Serk.Platform.Primitives   shared pure values (Ethiopian calendar)
  src/Platform/Serk.Platform.Persistence  Npgsql access with explicit SQL (no ORM-generated schema)
  src/Modules/Serk.Modules.<Module>       one per module: Domain, Application and Infrastructure namespaces
  src/Modules/Serk.Modules.<Module>.Api   (when a module publishes something) the only thing other modules may reference
  db/                                      proposed additive SQL, applied to isolated test databases only
  differential/go-oracle                   the Go reference, run on generated cases
  tests/                                   architecture, kernel, persistence and differential tests
```

Rules, enforced by `tests/Serk.Architecture.Tests`:
- Platform never references a module.
- A module references platform projects and other modules' `.Api` projects only.
- An `.Api` project references platform types only.
- The project graph has no cycle.
- The numerical kernel has no package, no project reference, no `System.Decimal` and no floating rounding.
- Every directive module exists and names its owner.

Rules not yet automated (they need code to bite on):
- `Domain` never references `Infrastructure`.
- Only `Infrastructure` touches Npgsql.
- No AI principal reaches an approve, post, pay, release or certify command.

These become reflection-based architecture tests with the first module that has such code.

## Modules and ownership

Counts are rows of `docs/MIGRATION_TRACEABILITY.md` at commit `59718e9`, where every row has a destination module.
Owners are roles (people are assigned by the owner). The "Go status" column counts that module's 218 listed
requirements (VERIFIED / not yet verified) in the Go register.

| Module | Owner (role) | Go requirements | Tables | Routes | First .NET work | Status |
|---|---|---|---|---|---|---|
| Platform (numerics, primitives, persistence, host, outbox, import) | Chief architect | 37 | 5 | 6 | kernel, calendar, rational persistence | kernel and calendar: DIFFERENTIAL_PASS |
| Governance (identity, roles, attestation, signer, settings, audit) | Security lead | 12 | 28 | 63 | authorization model; approval attestation | NOT_STARTED |
| Capital (GL, AP, AR, treasury, tax, FX, consolidation, period end) | Finance lead | 20 | 51 | 126 | ledger and journals (double entry), FX, tax | NOT_STARTED |
| Supply (procurement, suppliers, three-way match, subcontracts) | Procurement lead | 11 | 23 | 45 | requisition → PO → GRN → invoice match | NOT_STARTED |
| Stock (inventory, warehouses, valuation, reservations) | Stores lead | 8 | 15 | 41 | stock ledger and valuation | NOT_STARTED |
| Plan (budget, WBS, schedule, EVM, progress, site) | Project controls lead | 16 | 30 | 65 | CPM schedule (pkg/schedule), EVM (pkg/evm) | NOT_STARTED |
| Estimate (BOQ, rates, build-ups, tender) | Estimating lead | 10 | 5 | 17 | rate build-up on the exact kernel | NOT_STARTED |
| Contracts (contracts, variations, claims, IPC, bonds, escalation) | Commercial lead | 8 | 19 | 59 | IPC and retention | NOT_STARTED |
| Fleet (equipment, fuel, maintenance, charge-out) | Plant lead | 32 | 16 | 51 | pkg/fleet differential | NOT_STARTED |
| People (HR, timesheets, payroll) | HR and payroll lead | 5 | 14 | 51 | payroll controls | NOT_STARTED |
| Assure (quality, HSE, lab) | Quality and HSE lead | 4 | 14 | 32 | ITP and NCR | NOT_STARTED |
| Connect (documents, RFI, submittals, correspondence, reports, portals) | Document control lead | 32 | 14 | 36 | document register; sandboxed parsers | NOT_STARTED |
| Intelligence (automation, assistant, KPIs) | Data and AI lead | 5 | 2 | 11 | governed proposals only | NOT_STARTED |
| Command (enterprise, portfolio, executive) | Executive office | 11 | 0 | 0 | read models over the modules | NOT_STARTED |
| Design (BIM, digital twin, GIS) | BIM / engineering lead | 3 | 0 | 0 | xBIM evaluation; BIMCraft adapter | BLOCKED (BIMCraft not supplied) |
| Estate (proposed: sales, facilities, commissioning, handover) | owner decision | 4 | 0 | 0 | none until the owner decides | owner decision |

## Dependency order (next modules)

1. **Platform first**: kernel and calendar are done, then persistence and the transaction pattern.
2. **Governance**:
   - authorization and project scope;
   - attested approvals, keeping the PostgreSQL `approval_use` check;
   - the separation of AI principals from approval commands.
3. **Capital ledger**: journals, periods and double entry. `pkg/ledger`, `pkg/fx` and `pkg/tax` are pure and get the
   same differential treatment as `pkg/money`.
4. **Plan**: `pkg/schedule` (CPM) and `pkg/evm` are pure and come next.
5. **Supply and Stock** (three-way match, valuation), then **Contracts** and **People**.
6. **Fleet, Assure, Connect, Intelligence, Command**, then **Design** once BIMCraft is supplied.

## Database compatibility plan

- The 97 Go migrations are the schema of record, preserved verbatim and applied by the existing runner. EF Core is not
  used to create or change that schema.
- Npgsql and EF Core map onto it only when a module is migrated. Critical transactional paths use explicit SQL inside
  explicit transactions, with optimistic concurrency (the existing `version` columns) and idempotency keys (the
  existing command log).
- Row-level security, guard triggers, `approval_use` attestation checks and append-only tables stay in PostgreSQL. A
  .NET writer connects as the same least-privileged tenant roles, so an ORM cannot bypass them.
- New objects are additive SQL in `dotnet/db/`, reviewed like migrations. The first is `serk_rational`: exact
  rationals as canonical text; the database refuses non-canonical spellings and compares exactly.
- Go and .NET are never authoritative writers to the same database. Differential database tests run on isolated clones.
  Cutover (one module at a time, or all at once) is a separate plan, rehearsed with backup restore and rollback, and
  needs written owner approval.

## Engineering, BIM and intelligence (not started; blocked inputs noted)

- **Titan**: the numerical kernel of `SERK-5D-Engineering-Lab` and audit findings AUD-001..AUD-028 are not reachable from
  this session. The C# kernel follows the directive's stated requirements; each AUD finding gets a regression test
  once its text is available.
- **BIM**:
  - xBIM is evaluated for IFC reading and validation behind a sandboxed parser.
  - BIMCraft (Rust) stays a native dependency behind a versioned subprocess or gRPC contract, after its licence and
    feasibility are reviewed.
  - Model imports that fail validation never feed authoritative quantities.
  - Geometry-derived quantities are `Quantity.Approximate`, carrying an error bound; authoritative workflows require
    `RequireExact`.
- **Intelligence**:
  - AI produces proposals only.
  - Arithmetic is done by the kernel, never by a language model.
  - Approvals stay human and attested; no AI principal holds an approve, post, pay, release or certify permission.
