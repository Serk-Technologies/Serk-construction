# Cycle report: fleet review corrections and the AURA foundation (2026-11-15)

Release decision: **NO-GO**. This cycle did Workstream A of the AURA directive and the first step of Workstream B. It did not attempt the rest of the directive.

States used below: *designed* (specified, not in the product), *implemented* (in the product), *verified* (named automated tests passed in an executed run). Nothing in this cycle was independently reviewed, externally integrated, professionally accepted or authorised.

## 1–2. Revisions

Start: `45a3dc1` (baseline reproduced on a fresh database: 468 passed, 0 failed, 0 skipped). End: the commit containing this file, after `12ca58e` (corrections) and `01d727e` (tokens).

## 3. Implemented and verified

| Item | State | Evidence |
|---|---|---|
| Independent certification of work orders (certifier ≠ preparer, authoriser, reporter), kernel and database | verified | `TestMaintenance_SeparationAsEnforced` |
| Fresh second factor for authorising a repair, at the database | verified | same test, with the kernel bypassed on purpose |
| Approved, effective-dated fleet policy for fuel and due-soon thresholds | verified | `TestFleetPolicy_ApprovedThresholdsApply`, `TestAPIMaintenance` |
| Plan revision and retirement | verified | `TestMaintenance_PlanRevisionAndRetirement` |
| Expected fuel across several standards in one period | verified | `TestFleetPolicy_ApprovedThresholdsApply` |
| AURA light and dark themes in the console (colour, type family, elevation, focus, motion) | implemented; journeys and audits verified; one page reviewed by eye | `docs/design/aura/`, seven `TestConsoleBrowser*` |
| FLT-04 naming resolved by mapping | done | ADR 0024, register |

Migration 0057, ADR 0024. Full run after the corrections: 471 passed, 0 failed, 0 skipped, 37 mandatory (`../engineering/evidence/2026-11-15-fleet-corrections.summary.json`). The run with the theme applied is recorded in `execution-state.json`.

## 4. Corrections to earlier statements

- "Four separate hands" on a work order was wrong. It was two pairs; it is now at least three distinct people. Whether four are required is the owner's decision (ADR 0024).
- "Fresh second factor to authorise" was enforced only in the application until migration 0057.
- Earlier documents called costing "FLT-04" and fixed assets "FLT-05". The register's FLT-04 is Fixed assets; costing is FLT-01.14; FLT-05 does not exist.

## 5. Defects found and fixed in this cycle

| Defect | Found by | Fix |
|---|---|---|
| A plan revision keeping its title collided with the plan it replaces | new test | active-title rule checked at the end of the transaction |
| With a standard set but no logs, the reconciliation reported "no standard" | existing API test | only logged days without a standard count as unpredicted |
| The page's focus ring was 2.0:1 on the forest top band | contrast computation, before any styling | a focus colour for the band |
| The fuel table's last column was clipped and stretched its row | looking at the rendered page | explanations moved under the table; journey asserts the table fits |
| **A late answer to an earlier request could silently reset a choice in a form** (the RFI's document link; found because GitHub run 35 failed while the same tree passed locally). Predates this session | CI on GitHub's timing | forms apply only the newest answer and keep the current choice; the documents journey now holds the first answer back on purpose and fails without the fix |
| A workflow step name broke the workflow file's YAML | local check before push | fixed; `scripts/prepush.sh` now parses workflow files |

In the first draft of the corrective migration I again linked two writes through a session setting a tenant login could forge; it was replaced before it reached the tree by a check against the row written in the same transaction.

### Known remaining instance of that defect class

The fix was applied to the RFI form, the stock-issue form, the fuel form and the work-order part form. Seven other forms reload their cost-code list on a project change with the older pattern (a late answer can replace the list; the user's choice is then the first entry rather than what they picked). They are not yet converted and have no reproduction test. This is a real, user-visible defect on slow connections and is the first item of the next cycle.

## 6. AURA: what exists and what does not

Exists: token file with light and dark sets; 72 colour pairs computed against their targets and enforced in CI together with the stylesheet; the console's two themes on those tokens.

Does not exist: Figma library or any Figma file; sign-in redesign; personal workspace; navigation shell and command palette; component catalogue; warm and high-contrast themes; dashboard customisation; frontend modularisation; TypeScript evaluation; localisation beyond what was there; eye review of screens other than the Fleet page.

## 7. Not started from this directive

Equipment costing and charge-out (FLT-01.14), fixed assets (FLT-04), tank readings and variance investigation records, labour costing, inspection checklists; executive command centre; work packages, quality, HSE; every other business family; the field application; the AI gateway; identity, KMS/HSM, deployment profiles, monitoring; load, soak and fault-injection tests; complete-system recovery drill; synthetic UAT environment.

## 8. Blockers that need a person

- Repository administrator: `GOVERNANCE_REVIEW_PACKAGE.md`.
- Owner: whether a work order needs four distinct people; whether to authorise a Figma file and which workspace; identity, KMS/HSM and AI provider accounts.
- Financial controller and plant manager: charge-out, allocation and depreciation policy; the first real fleet policy.
- Professional reviewers: translations, payroll and tax rules.

## 9. Next steps, exactly

1. Convert the seven remaining forms to the newest-answer-wins loader, each with a reproduction in a journey.
1. Record the CI result of this cycle's final commit; if it is to be a candidate, run it three times.
2. AURA: review each screen family by screenshot at the listed breakpoints; warm and high-contrast token sets; then the sign-in page and personal workspace as real, working screens.
3. FLT-01.14 costing as approved policy documents with synthetic policies in test; then FLT-04 fixed assets through the existing posting interfaces.
