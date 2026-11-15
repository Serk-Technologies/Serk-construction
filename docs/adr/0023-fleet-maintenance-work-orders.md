# ADR 0023: Maintenance work orders gate the return to service

Status: accepted, 2026-11-15. Extends ADR 0020 (fleet lifecycle) and ADR 0022 (fuel as stock).

## Context

The fleet lifecycle (ADR 0020) lets anyone who operates a machine stop it, and only a certifier (`fleet.release`, fresh second factor) return it to service. Nothing recorded what was done in between, what it cost in parts, or whether anyone other than the certifier had looked at the work. The directive asks for preventive maintenance, breakdown reporting, work orders, parts, labour, downtime, repair approval and history.

## Decision

1. **A work order is a governed document** (`maintenance_work_order`), with four separate hands: preparer, authoriser (`fleet.approve`, fresh second factor, not the preparer), reporter of completion (`fleet.manage`) and certifier (`fleet.release`, fresh second factor, not the reporter). The certifier closes it or sends it back with a reason, which clears the completion report. Site staff who operate equipment may prepare only a BREAKDOWN, only for a machine at one of their projects. A closed, rejected or cancelled work order never changes. Every step is attested; none can be taken by an AI or service principal.
2. **The machine itself is gated.** A second trigger on `equipment` (the ADR 0020 guard is untouched) applies to every move back into service from inspection, maintenance or safety hold: refused while a work order of the machine is authorised or awaiting its certifier; and, if the machine has been UNDER_MAINTENANCE at any point since it was last in service, refused unless a work order for it was closed after it entered maintenance. Passing through inspection does not avoid it, and a work order from an earlier spell does not count. This changes existing behaviour deliberately.
3. **Parts are stock issues.** `RequestWorkOrderPart` writes a draft stock issue by the shared code path and a `maintenance_parts` row linking it to an authorised work order. The existing second-person approval values the part and charges the project. A work order cannot be reported complete while one of its parts is still awaiting approval. A machine at a project charges that project.
4. **Preventive plans** recur by metered units, by days, or both. A plan counts from the machine's meter and the date when it is made. It is raised as at most one open PREVENTIVE work order; only the certified close of that work order moves the plan on, checked in the database against the closing in the same transaction (not a session setting a tenant login could forge).
5. **Reporting**: what is due (OVERDUE, DUE_SOON, OK), a machine's work orders with labour hours and parts cost from the stock ledger, and its hours out of service by state from the lifecycle history.
6. No new permission. The roles of ADR 0020 already separate plant officer, plant manager and inspector.

## Consequences and limits

- Labour hours are recorded without a rate; maintenance labour cost is not computed or posted.
- A part for a machine that is not at a project must name a project to charge. A workshop or plant cost centre belongs to equipment costing (FLT-04).
- DUE_SOON (a tenth of the interval, or seven days) is an engineering default.
- A plan cannot be edited or retired yet; a wrong plan stays listed.
- A safety hold with no maintenance in between still returns on the certifier's attested decision alone, as before; inspection checklists are not built.
- The gate reads the machine's lifecycle history on each return to service; histories are short and indexed by machine.

## Evidence

`TestMaintenance_WorkOrderAndReturnToService`, `TestMaintenance_PlansAndDue`, `TestFleet_RegisterAndLifecycle` (internal/kernel, PostgreSQL), `TestDueState` (pkg/fleet), `TestAPIMaintenance` (internal/httpapi), `TestConsoleBrowserFleet` (real browser, with accessibility audits).
