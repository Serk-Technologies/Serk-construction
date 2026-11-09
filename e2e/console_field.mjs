// Browser end-to-end test of timesheets, equipment and HSE (migration 0064) in the SERK console, started by
// internal/httpapi/console_browser_field_test.go. Accessibility audited on each new page; phone width and dark theme too.
import { as, nav, act, selectByText, snap, audit, log, fail, failures, browser, pages } from './console_lib.mjs';

try {
  // ---- timesheet: engineer records and submits, project manager approves -------------------
  const eng = await as('eng');
  await nav(eng, '#/timesheets/record', 'Timesheets');
  await audit(eng, 'timesheet form');
  const grid = eng.getByRole('group', { name: /^Hours/ });
  await grid.getByLabel('Normal hours, line 1').waitFor();
  await grid.getByLabel('Overtime hours, line 1').fill('2');
  await act(eng, 'Save and submit', 'Timesheet submitted');
  await eng.getByRole('link', { name: /^TS-/ }).first().waitFor();
  log('timesheet submitted');
  const pm = await as('pm');
  await nav(pm, '#/timesheets/list', 'Timesheets');
  await pm.getByRole('link', { name: /^TS-/ }).first().click();
  await pm.getByRole('heading', { level: 1, name: /^Timesheet TS-/ }).waitFor();
  await pm.getByRole('table').filter({ hasText: 'Overtime' }).waitFor();
  await audit(pm, 'timesheet detail');
  if (await pm.getByRole('link', { name: 'Record a day' }).count()) log('(pm sees the form tab: ok)');
  await act(pm, 'Approve', 'Approve: done');
  log('timesheet approved');

  // ---- equipment usage ---------------------------------------------------------------------------
  await nav(eng, '#/equipment/usage', 'Equipment and fleet');
  await audit(eng, 'equipment usage');
  await eng.locator('input[name=litres_fuel]').fill('200');
  await act(eng, 'Save and submit', 'Usage submitted');
  await nav(pm, '#/equipment/usage', 'Equipment and fleet');
  await pm.getByRole('button', { name: 'Approve', exact: true }).first().click();
  await pm.locator('p.notice').filter({ hasText: 'Usage approved' }).first().waitFor();
  await nav(pm, '#/equipment/register', 'Equipment and fleet');
  await pm.getByRole('cell', { name: 'EXC-01' }).first().waitFor();
  const reg = await pm.locator('main').textContent();
  if (!reg.includes('FUEL ANOMALY')) fail('the register does not flag the fuel anomaly (200 l over 8 hours against a norm of 18)');
  await snap(pm, 'equipment-register');
  log('equipment usage approved');

  // ---- HSE: incident and permit ----------------------------------------------------------------------
  await nav(eng, '#/hse/incidents', 'Health, safety and environment');
  await audit(eng, 'hse incidents');
  await eng.getByRole('button', { name: 'Report an incident' }).click();
  const dlg = eng.getByRole('dialog');
  await selectByText(dlg.getByLabel('Kind'), 'Lost time');
  await selectByText(dlg.getByLabel('Severity'), 'Major');
  await dlg.getByLabel('What happened').fill('Fall from scaffold, wrist fracture');
  await dlg.getByLabel('Days lost').fill('3');
  await audit(eng, 'incident sheet');
  await dlg.getByRole('button', { name: 'Report', exact: true }).click();
  await eng.locator('p.notice').filter({ hasText: 'Incident reported' }).first().waitFor();
  const hse = await as('hse');
  await nav(hse, '#/hse/incidents', 'Health, safety and environment');
  await hse.getByRole('link', { name: /^INC-/ }).first().click();
  await hse.getByRole('dialog').getByLabel('Root cause').fill('Missing toe boards');
  await hse.getByRole('dialog').getByLabel('Findings').fill('Scaffold handed over without inspection');
  await hse.getByRole('button', { name: 'Record investigation' }).click();
  await hse.locator('p.notice').filter({ hasText: 'Investigation recorded' }).first().waitFor();
  log('incident reported and investigated');
  await nav(eng, '#/hse/permits', 'Health, safety and environment');
  await audit(eng, 'permits');
  await eng.getByLabel('Where').fill('Block A, level 3');
  await eng.getByLabel('Work to be done').fill('Welding of the balustrade');
  await act(eng, 'Request permit', 'Permit requested');
  await nav(hse, '#/hse/permits', 'Health, safety and environment');
  hse.once('dialog', (d) => d.accept('Fire watch 30 minutes'));
  await hse.getByRole('button', { name: 'Issue…' }).first().click();
  await hse.locator('p.notice').filter({ hasText: 'Permit issued' }).first().waitFor();
  await snap(hse, 'permit-issued');
  log('permit requested and issued');

  const phone = await as('engPhone', { viewport: { width: 390, height: 844 }, colorScheme: 'dark' });
  await nav(phone, '#/timesheets/record', 'Timesheets');
  await audit(phone, 'timesheets at 390px (dark)');
  await nav(phone, '#/hse/incidents', 'Health, safety and environment');
  await audit(phone, 'hse at 390px (dark)');
  await snap(phone, 'mobile-hse-dark');
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
console.log('\nCONSOLE FIELD: PASS');
