# ADR 0024: Fleet corrections after independent review

Status: accepted, 2026-11-15. Amends ADR 0022 (fuel) and ADR 0023 (maintenance). Migration 0057.

## What the review found, and what was true

| Finding | What the system actually did | Correction |
|---|---|---|
| Is a work order four people or two pairs? | Two independent pairs: authoriser ≠ preparer, certifier ≠ reporter. ADR 0023 and the completion report said "four separate hands". That wording was wrong: one person could prepare and certify, and the authoriser could certify | Wording corrected (below). Enforcement strengthened: the certifier is now none of the preparer, authoriser or reporter, in the kernel and in the database |
| Fresh second factor at both boundaries | Certification: both. Authorisation of a repair: application only; the database accepted an attestation without it | The database now requires it for authorisation too |
| Hardcoded thresholds | 10% fuel tolerance, one litre, a tenth of an interval and seven days were constants | An approved, versioned, effective-dated fleet policy; defaults remain only as a labelled fallback |
| Plans cannot be changed | True | Revision and retirement |
| Periods spanning several fuel standards | The standard at the end of the period was applied to the whole period | Expected fuel is computed log by log at the standard of each day |
| FLT-04 means two things | The register's FLT-04 is Fixed assets; handovers used FLT-04 for costing and an FLT-05 that does not exist | Mapping, no renumbering (below) |

## Decisions

1. **Separation on a work order, stated exactly.** The authoriser is not the preparer. The certifier (closing or sending back) is none of the preparer, the authoriser or the reporter of completion. The preparer may report the work complete: plant staff raise and carry out their own work orders. So a work order carries **at least three distinct people, not four**. Whether the company wants four (reporter ≠ preparer as well) is a policy decision for the owner; it would be a one-line rule in the same guard and is not assumed.
2. **Second factor at the database** for both authorising and certifying. Rejecting and sending back with a reason: sending back is a certifier's act and needs it; rejecting an unauthorised request does not.
3. **Fleet policy** (`fleet_policy`, a governed document): fuel variance tolerance and minimum litres; due-soon share and days. Prepared with `fleet.manage`, approved by another person with `fleet.approve` and a fresh second factor (database-enforced). The policy in force on a date is the latest approved one effective on or before it; one approved policy per effective date; a decided policy never changes. Fuel reconciliation applies the policy in force at the end of its period; the due list applies today's. Every report names the policy it used. **With no approved policy the engineering defaults are used and labelled as such**, which is also how a synthetic policy is used in a test environment: approve one there.
4. **Plans.** A revision is a new plan that names the one it supersedes; inserting it retires the old plan in the same transaction and carries the last-done meter and date forward. A plan can also be retired with a reason. Neither is allowed while a work order of the plan is open. Retired plans are kept. The link between a revision and the retirement it causes is checked against the revision row written in the same transaction, not a session setting.
5. **Fuel across standards.** `equipment_fuel_expected` sums each approved log's metered units at the standard in force on that log's date. Units logged on days without a standard predict nothing and are reported.
6. **Requirement mapping.** FLT-04 is and remains Fixed assets. Equipment costing and charge-out is registered as FLT-01.14 (the original register has no costing domain; it belongs to equipment utilisation). There is no FLT-05. Earlier documents that said "FLT-04 costing" or "FLT-05" are corrected to this mapping; evidence already recorded is unaffected because no identifier was reused.

## Not decided here (needs the owner or the financial controller)

Charge-out rates, workshop cost allocation and utilisation rules: they are equipment costing (FLT-01.14) and will be policy documents of the same kind. Whether four distinct people are required on a work order.

## Evidence

`TestMaintenance_SeparationAsEnforced` (kernel bypassed on purpose: attestations for the wrong person, without a fresh second factor, for another record or outcome, reused, and forged session settings, all refused by the database), `TestFleetPolicy_ApprovedThresholdsApply`, `TestMaintenance_PlanRevisionAndRetirement`, `TestFuelFlags`, `TestDueState`, `TestAPIMaintenance`, `TestConsoleBrowserFleet`.
