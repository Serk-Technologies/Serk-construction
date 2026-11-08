# User acceptance test plan, release 1

Purpose: let the people who will use the system decide whether it does their job. Not a substitute for the automated
suite or the independent security work; those are separate gates.

## Who signs

| Area | Signs | Must be |
|---|---|---|
| Finance (ledger, close, reconciliations, payments) | Finance lead | A qualified accountant of the company |
| Procurement and stores | Procurement lead | Someone who runs purchasing |
| Commercial (contracts, certificates, invoicing) | Commercial lead | A quantity surveyor or commercial manager |
| Project controls (budget, forecast, dashboard) | Project controls lead | A project manager or planner |
| Security and governance | Security lead | Not the developer of the code |

## Environment

A staging environment seeded with synthetic data (`runlocal.md` for the demo stack; a real staging host once it exists).
Real customer data is **not** used until the production decision is made.

## Scenarios

Each scenario is a case in `testmanual.md`; the tester follows its steps, records Pass, Fail or Blocked, and notes defects.

| Scenario | Cases | Roles to switch between |
|---|---|---|
| Sign in, roles and separation of duties | AUTH-01 to AUTH-13 | any |
| Budget, revisions and transfers | BUD-01 to BUD-07 | project manager, commercial manager, CFO |
| Authority, delegation, duty conflicts | AUTH-10 to AUTH-13 | process owner, CFO, security admin |
| Buy to pay | Journey A cases | site engineer to CFO |
| Contract to cash | Journey B cases | QS, commercial, AR |
| Stock, reservations, conversions | Journey C, STK-10 to STK-12 | storekeeper, site engineer, finance manager |
| Reports, exports, dashboard | REP-10, REP-11, DASH-01, DASH-02 | CFO, project manager |
| Projects, stage gates, organisation | ORG-01, ORG-02 | commercial manager, project manager, CFO |
| Setup and imports | SET-01, IMP-01, IMP-02 | buyer, second buyer, procurement manager |
| Every page explains itself | GUIDE-01 | any |

## Entry criteria

- Full automated suite green on the build under test; evidence files current.
- Release notes read by the testers; known limits understood.
- A freshly seeded database (`testmanual.md` section 11.4).

## Exit criteria (all required)

- Every case run by a person other than the one who wrote the code; no case left Blocked without a recorded reason.
- No open Critical or High defect; every Medium has an owner and a date.
- Each area lead has signed the table below.

## Sign-off (intentionally blank)

| Area | Name | Decision | Date | Signature |
|---|---|---|---|---|
| Finance | | | | |
| Procurement and stores | | | | |
| Commercial | | | | |
| Project controls | | | | |
| Security and governance | | | | |
