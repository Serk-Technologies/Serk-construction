// Browser end-to-end test of payroll (migration 0061) in the SERK console,
// started by internal/httpapi/console_browser_payroll_test.go (which verifies
// the outcome through the API afterwards). The payroll officer opens a run,
// enters inputs, calculates and submits; the HR manager approves; treasury
// releases the payment. Accessibility is audited; phone width and dark theme too.
import { as, nav, act, selectByText, snap, audit, log, fail, failures, browser, pages } from './console_lib.mjs';

try {
  // ---- 1. Officer opens the run, enters inputs, calculates, submits ---------------------------
  const off = await as('officer');
  await nav(off, '#/payroll/runs', 'Payroll');
  await audit(off, 'payroll runs (empty)');
  await act(off, 'Open a pay run', 'Pay run opened');
  const grid = off.getByRole('group', { name: /^Inputs/ });
  await grid.getByLabel('Months / days / hours, line 2').fill('22');
  await grid.getByLabel('Overtime hours, line 1').fill('10');
  await selectByText(grid.getByLabel('Charged to, line 1'), 'P-API');
  await selectByText(grid.getByLabel('Charged to, line 2'), 'P-API');
  await audit(off, 'pay inputs');
  await act(off, 'Save inputs', 'Inputs saved');
  await act(off, 'Calculate', 'Run calculated');
  await off.getByRole('link', { name: /^PAY-/ }).click();
  await off.getByRole('table').filter({ hasText: 'Net pay' }).waitFor();
  const text = await off.locator('body').textContent();
  for (const v of ['21,442.31', '8,800.00', 'gross 30,242.31']) if (!text.includes(v)) fail(`the calculated run does not show gross ${v}`);
  await snap(off, 'run-calculated');
  await act(off, 'Submit for approval', 'Submitted to the HR manager');
  log('run calculated and submitted');

  // ---- 2. HR manager approves ---------------------------------------------------------------
  const hr = await as('hr');
  await nav(hr, '#/payroll/runs', 'Payroll');
  if (await hr.getByRole('button', { name: 'Open a pay run' }).count()) fail('the HR manager is offered "Open a pay run"');
  await hr.getByRole('link', { name: /^PAY-/ }).click();
  await audit(hr, 'run to approve');
  await act(hr, 'Approve', 'Approved');
  log('run approved by the HR manager');

  // ---- 3. Treasury pays ---------------------------------------------------------------------
  const tr = await as('treasury');
  await nav(tr, '#/payroll/runs', 'Payroll');
  await tr.getByRole('link', { name: /^PAY-/ }).click();
  await act(tr, 'Release payment', 'Payroll paid');
  await snap(tr, 'run-paid');
  log('payment released by treasury');

  // ---- 4. Phone width, dark theme -----------------------------------------------------------
  const phone = await as('hrPhone', { viewport: { width: 390, height: 844 }, colorScheme: 'dark' });
  await nav(phone, '#/payroll/employees', 'Payroll');
  await audit(phone, 'employees at 390px (dark)');
  const emp = await phone.locator('main').textContent();
  if (emp.includes('1000111122223')) fail('an employee account number is shown unmasked');
  await snap(phone, 'mobile-employees-dark');
} catch (e) {
  fail(`journey aborted: ${e.message.split('\n')[0]}`);
  for (const [role, p] of Object.entries(pages)) {
    try { await snap(p, `failure-${role}`); } catch { /* page may be gone */ }
  }
} finally {
  await browser.close();
}

if (failures.length) {
  console.log(`\n${failures.length} failure(s):\n- ` + failures.join('\n- '));
  process.exit(1);
}
console.log('\nCONSOLE PAYROLL: PASS');
