# WP-2.8 Workforce and payroll: verification, 2026-11-09

SRS: HCM-01, HCM-04, HCM-05, HCM-06. Migration `0061_payroll.sql`; `internal/kernel/payroll.go`;
`internal/httpapi/payroll_handlers.go`; console **Workforce → Payroll** (Pay runs, Employees, Tax and pension rules,
Accounts). New permissions `payroll.prepare` (role `payroll_officer`) and `payroll.approve` (role `hr_manager`);
release uses the existing `payment.release` (treasury). New duty-conflict pairs "Payroll" and "Payroll and payment
release". Staging personas `u-payroll` and `u-hr-manager`.

## Controls

| Control | Where it is enforced |
|---|---|
| Payroll accounts (salaries, employer pension, net pay, income tax, pension payable) proposed by the controller, approved by the CFO with fresh MFA; all five differ and have the right types | `payroll_policies_guard` |
| Tax bands ascending, rates in [0, 1), last band open; effective-dated; prepared by the payroll officer, approved by the HR manager (not the preparer, fresh MFA) | `payroll_bands_valid`, `payroll_rule_sets_guard`, `approval_use` |
| A rule set never takes effect inside a period whose pay run is approved or paid | `payroll_rule_sets_guard` (on insert and on approval) |
| Each pay line equals the database's computation (`payroll_compute`); a forged line is refused | `pay_lines_guard` |
| Inputs only on a draft run, only for people employed by the entity during the period; a salaried person is paid at most one month | `pay_inputs_guard` |
| Preparer ≠ approver ≠ payer; approval and release need fresh MFA | kernel checks and `pay_runs_guard` (`approval_use`) |
| One PAY_RUN journal per project section, one PAY_PAY journal for the payment; each re-checked against its document at approval and posting, never reversed | `pay_expected_lines`, `journals_pay_binding`, `journals_pay_rebind`, `journals_subledger_guard` |
| Bank account numbers encrypted at rest and shown as `****1234`; the decrypted numbers leave only in the treasury bank file, whose download is audited | `sealAccountNumber`, `PayrollBankFile`, API audit log |

## Golden fixture (TestPayroll_RunApprovePay)

The bands are tenant test data, not a statement of current law: monthly 0–600 0%; to 1,650 10% less 60; to 3,200
15% less 142.50; to 5,250 20% less 302.50; to 7,800 25% less 565; to 10,900 30% less 955; above 35% less 1,500.
Pension 7% (employee) and 11% (employer) on basic; overtime 1.5×; 208 hours a month, 8 a day.

| Employee | Inputs | Gross | Tax | Pension EE / ER | Net |
|---|---|---|---|---|---|
| E-001 salaried 20,000 | 1 month, 10 h overtime, allowance 1,000, project | 22,442.31 | 6,354.81 | 1,400 / 2,200 | 14,687.50 |
| E-002 daily 400 | 22 days, project | 8,800.00 | 1,685.00 | 616 / 968 | 6,499.00 |
| E-003 hourly 50 | 160 h + 8 h overtime, head office | 8,600.00 | 1,625.00 | 560 / 880 | 6,415.00 |

| Journal | Lines (proven) |
|---|---|
| PAY_RUN, project section | Dr 6200 31,242.31, Dr 6210 3,168.00 / Cr 2250 21,186.50, Cr 2260 8,039.81, Cr 2310 5,184.00 |
| PAY_RUN, head office section | Dr 6200 8,600.00, Dr 6210 880.00 / Cr 2250 6,415.00, Cr 2260 1,625.00, Cr 2310 1,440.00 |
| PAY_PAY | Dr 2250 27,601.50 / Cr 1050 27,601.50 |

(Account codes in the test fixture; the demo seed uses 6300, 6310, 2500, 2510, 2520.)

Also proven: descending bands refused; the preparer cannot approve the rule set or the run; a project manager
cannot register employees or read the payroll; a second (two-month) input for a salaried person refused; a forged
pay line refused; an unsubmitted run cannot be approved; approval without fresh MFA refused; a rule set dated
15 October (inside the approved October run) refused, one from 1 November accepted; the HR manager cannot release
the payment or download the bank file; the bank file carries the decrypted account number for E-001 and an empty
one for E-003 (no bank details); a leaver cannot be put on a later run; cancelling needs a reason.

## Browser journey (TestConsoleBrowserPayroll, e2e/console_payroll.mjs)

Real Chromium against a test server: the payroll officer opens the October run, enters the inputs on the grid,
saves, calculates (gross 21,442.31 and 8,800.00; totals gross 30,242.31) and submits; the HR manager (not offered
"Open a pay run") approves; treasury releases from the company bank. The API then shows the run PAID. Accessibility
audited on each page; the employee list at 390 px in dark theme shows no account number in clear.

## Not built

ISO 20022 payment file (CSV only); hours from timesheets (WP-3.3; units are entered on the run); D-04 adviser-signed
rule values (rule sets are tenant data with a source reference); payslips; loans and other deductions; leave.

## Demo

Not yet applied to the :8080 demo (kept running unchanged during client demonstrations). When it is restarted:
migration 0061 applies, the seed adds accounts 6300, 6310, 2500, 2510, 2520, and the personas `u-payroll` and
`u-hr-manager` appear on the staging sign-in.
