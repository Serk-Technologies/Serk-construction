# ADR 0022: Fleet fuel is stock; a dispense is a stock issue

Status: accepted, 2026-11-15. Extends ADR 0020 (fleet register).

## Context

Fleet part 1 records hours and a "fuel litres" figure in daily logs but knows nothing about where fuel comes from or what it costs. The directive for fleet fuel (FLT-02) asks for purchases, tanks, receipts, dispensing, consumption against standards, reconciliation and project cost. The system already has a stock ledger with valuation (FIFO or weighted average), no-negative-stock control, counts, transfers and a journal for issues that charges a project and cost code.

## Decision

1. **No second inventory.** Fuel is a stock item. A tank or bowser is a warehouse. Buying and receiving fuel is the existing purchase, goods-receipt and put-away chain. A tank reading that differs from the book is the existing stock count.
2. **A dispense is a stock issue plus one fact.** `DispenseFuel` writes, in one transaction, a DRAFT stock issue by the same code path as any other issue, and a `fuel_dispenses` row saying which machine took it, at what meter reading, for which operator. The database checks that the item is designated as fuel, the issue is the preparer's own draft with the same project, litres and date, the machine is at that project and not unregistered, retired or disposed, and the reading is not behind the last approved one.
3. **Effect only on approval by a second person.** The existing stock-issue approval values the fuel, removes it from the tank and generates the STK_ISSUE journal that charges the project. Until then the dispense is "awaiting approval" and nothing has moved. More than the tank holds cannot be approved.
4. **Fuel items and standards are attested decisions of a person with `fleet.approve`.** A fuel item is permanent. A standard is litres per metered unit (hour or km) from a date, append-only.
5. **Reconciliation is a report that asks questions.** Per machine and period: litres dispensed (approved issues), awaiting approval, value from the stock ledger, litres in approved logs, metered units, the standard and the expected litres. A difference of more than a threshold (10% of the larger figure, and more than one litre) is listed with both figures and possible explanations. It accuses no one and decides nothing.
6. **No new document type.** The governed document is the stock issue; attestations name existing subjects (`stock_item`, `equipment`, `stock_issue`). The shared subject functions are not redefined.

## Consequences and limits

- The project charged is the project the machine is at; fuel for a machine in the yard is an ordinary stock issue, not a dispense.
- Quantities are in the item's base unit (litres); no unit conversion on a dispense.
- The threshold is an engineering default shown in the report, not a policy; it should be confirmed or replaced by the plant manager and the financial controller.
- The standard applied to a period is the one in force at its end; a period spanning a change of standard is not split.
- Not built: a recorded, approved investigation of a variance; tank dip readings as their own record; fuel cards and external filling stations; telematics fuel sensors; fuel for machines without a project.
- The reconciliation aggregates on request. It is bounded by period (366 days) and project scope and will need the same roll-up as the fleet summary before tens of millions of rows.

## Evidence

`TestFuel_DispenseIsAStockIssue`, `TestFuel_StandardsAndReconciliation` (internal/kernel, PostgreSQL), `TestFuelFlags` (pkg/fleet), `TestAPIFuel` (internal/httpapi), `TestConsoleBrowserFleet` (real browser).
