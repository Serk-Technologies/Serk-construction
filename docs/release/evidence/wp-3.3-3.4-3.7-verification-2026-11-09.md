# WP-3.3 Timesheets, WP-3.4 Equipment, WP-3.7 HSE: verification, 2026-11-09

Migration `0064_workforce_field.sql`; `internal/kernel/field.go`; `internal/httpapi/field_handlers.go`; console **Site workforce & safety**
(Timesheets, Equipment and fleet, Health and safety). Permissions: `timesheet.record` / `timesheet.approve`, `equipment.manage` / `.record` / `.approve` /
`.charge`, `hse.report` / `hse.manage`; roles `plant_manager` and `hse_officer`; duty-conflict pairs "Timesheets" and "Equipment usage".
Personas `u-plant`, `u-hse`.

## Controls (database-enforced)
Timesheets: one DRAFT per day and project with at most 16 hours a person; submit and approve by different people (project scope);
approved hours per person and day never above 16 across projects and one approved line per person, project and day; employees must be
employed by the project's entity that day; optional induction gate. Approved hours import into a draft pay run (daily people their days,
hourly people their hours, overtime for all; the project with most hours is charged).
Equipment: usage recorded and approved by different people; the charge is computed at approval (working hours x rate, or a day's rate;
idle and breakdown free); at most 24 hours a day across projects; one record per equipment, project and day; charged once by a hire run that
creates one draft journal per project (Dr cost / Cr recovery, accountant chooses the accounts, finance approves and posts); work orders
completed once; fuel anomaly when litres per working hour over 30 days exceed the norm by 20%; service due from hours since the last service.
HSE: a fatality is critical, a lost-time injury has lost days; an incident moves to investigation only with an investigation; it is closed by
someone other than the reporter, with every corrective action done and verified by a different person and at least one action for a major or
critical incident; a major or critical report notifies HSE officers and project managers at once; permits last at most 31 days and need three
different people (requester, issuer, acceptor); inductions, toolbox talks and inspections are insert-only; LTIFR = lost-time injuries x 1,000,000 /
approved hours of the last 12 months.

## Tests
`TestField_TimesheetsEquipmentHSE` (kernel): roster shows no pay data; PM cannot record; 17 hours refused; induction gate blocks then allows
submission; recorder cannot approve; duplicate approved line refused; payroll import gives E-001 8 h + 2 h overtime and E-002 1 day (salaried
person not imported); excavator 8 h x 800 = 6,400 and fuel 25 l/h flagged against a norm of 18; hire journal Dr 5000 / Cr 5100 6,400; second
charge refused; work order completes once; incident flow with notifications (2), investigation, action, verification by a second officer,
closing by a third; permit flow; LTIFR 55,555.56 (1 injury / 18 hours). Browser journey `TestConsoleBrowserField` (engineer, project manager,
HSE officer, accessibility incl. 390 px dark).

## Not built
Leave and biometric attendance, equipment telematics and transfers between sites, fuel issue from stock issues, environmental monitoring.
