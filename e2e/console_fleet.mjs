// Browser end-to-end test of SERK FLEET part 1 (migration 0054) in the SERK
// console, started by internal/httpapi/console_browser_fleet_test.go (which
// verifies the outcome through the API afterwards). The plant officer
// registers an excavator, the plant manager commissions it, the site engineer
// reserves it, the manager approves, the officer mobilises it, the engineer
// receives it, places it on a safety hold and finds no way to release it, the
// inspector certifies it, the engineer records a log and the project manager
// approves it; the command centre then shows the hours. Accessibility is
// audited on every new page; phone width and dark theme too.
import { as, nav, act, selectByText, snap, audit, idFromHash, log, fail, failures, browser, pages } from './console_lib.mjs';

async function move(p, label, reason, project) {
  await selectByText(p.getByLabel('Move', { exact: true }), label);
  await p.getByLabel('Reason').fill(reason);
  if (project) await selectByText(p.getByLabel('Project', { exact: true }), project);
  await p.getByRole('button', { name: 'Record the move' }).click();
}
const state = async (p, text) => p.locator('.kpis').filter({ hasText: text }).first().waitFor();

try {
  // ---- 1. Officer registers; manager commissions ---------------------------------------
  const officer = await as('officer');
  await nav(officer, '#/fleet', 'Fleet command centre');
  await audit(officer, 'fleet command centre (empty)');
  await officer.getByRole('link', { name: 'Register equipment' }).click();
  await officer.getByRole('heading', { level: 1, name: 'Register equipment' }).waitFor();
  await officer.getByLabel('Name').fill('Excavator 07');
  await selectByText(officer.getByLabel('Class'), 'Excavator');
  await officer.getByLabel('Manufacturer').fill('Komatsu');
  await officer.getByLabel('Model').fill('PC200-8');
  await officer.getByLabel('Serial number').fill('KMT-PC200-55120');
  await officer.getByLabel('Opening meter reading').fill('2000');
  await audit(officer, 'equipment form');
  await officer.getByRole('button', { name: 'Register equipment' }).click();
  await officer.getByRole('heading', { level: 1, name: /^EQ-.* — Excavator 07$/ }).waitFor();
  const eq = await idFromHash(officer);
  await state(officer, 'Registered');
  if (await officer.getByRole('button', { name: 'Record the move' }).count()) fail('the registrar is offered a move on unregistered equipment');
  await audit(officer, 'equipment registered');
  const manager = await as('manager');
  await nav(manager, `#/fleet/${eq}`, /Excavator 07$/);
  await move(manager, 'Put into service', 'Commissioned after pre-delivery inspection');
  await state(manager, 'Available');
  log('registered and commissioned');

  // ---- 2. Engineer reserves; manager approves ------------------------------------------
  const eng = await as('eng');
  await nav(eng, `#/fleet/${eq}`, /Excavator 07$/);
  if (await eng.getByRole('button', { name: 'Record the move' }).count()) fail('site staff are offered a move on equipment in the yard');
  await eng.getByRole('link', { name: 'Reserve this equipment' }).click();
  await eng.getByRole('heading', { level: 1, name: 'Reserve equipment' }).waitFor();
  await selectByText(eng.getByLabel('Project'), 'P-API');
  await eng.getByLabel('Purpose').fill('Bulk excavation, cut 3');
  await eng.locator('main').filter({ hasText: 'No other reservation overlaps that period.' }).waitFor();
  await audit(eng, 'reservation form');
  await eng.getByRole('button', { name: 'Request reservation' }).click();
  await eng.getByRole('heading', { level: 1, name: /^Equipment reservation EQR-/ }).waitFor();
  const res = await idFromHash(eng);
  await act(eng, 'Submit for approval', 'Submit for approval: done');
  await nav(manager, '#/home', 'My work');
  await manager.getByRole('link', { name: /Equipment reservations to approve/ }).waitFor();
  await nav(manager, `#/doc/equipment_reservation/${res}`, /^Equipment reservation EQR-/);
  await act(manager, 'Approve', 'Approve: done');
  await audit(manager, 'reservation approved');
  log('reserved and approved');

  // ---- 3. Mobilise, receive, hold, certify ------------------------------------------------
  await nav(officer, `#/fleet/${eq}`, /Excavator 07$/);
  await state(officer, 'Reserved');
  await move(officer, 'Mobilise to a project', 'Low-bed dispatched', 'P-API');
  await state(officer, 'Mobilising');
  await nav(eng, `#/fleet/${eq}`, /Excavator 07$/);
  await move(eng, 'Confirm at the project', 'Arrived on site, checked');
  await state(eng, 'Assigned');
  await move(eng, 'Place on safety hold', 'Hydraulic hose leaking near the cab');
  await state(eng, 'Safety hold');
  const opts = await eng.getByLabel('Move', { exact: true }).locator('option').allTextContents();
  if (opts.some((o) => /return to service|operating|Confirm|Demobilise/i.test(o))) fail(`site staff are offered a way out of a safety hold: ${opts.join(' | ')}`);
  if (!(await eng.locator('main').textContent()).includes('cannot be put to work until a certifier')) fail('the hold is not explained on the equipment page');
  await audit(eng, 'equipment on safety hold');
  await snap(eng, 'safety-hold');
  const inspector = await as('inspector');
  await nav(inspector, `#/fleet/${eq}`, /Excavator 07$/);
  await move(inspector, 'Certify fit and return to service', 'Hose replaced, pressure test passed');
  await state(inspector, 'Assigned');
  log('mobilised, held and certified');

  // ---- 4. Operator authorised; log recorded and approved --------------------------------
  await nav(officer, '#/new/equipment_operator', 'Authorise an operator');
  await officer.getByLabel('Operator').fill('op-abebe');
  await selectByText(officer.getByLabel('Equipment class'), 'Excavator');
  await officer.getByLabel('Certificate').fill('TVET/EXC/2291');
  await audit(officer, 'operator form');
  await officer.getByRole('button', { name: 'Prepare authorisation' }).click();
  await officer.getByRole('heading', { level: 1, name: /^Operator authorisation EQO-/ }).waitFor();
  const op = await idFromHash(officer);
  await act(officer, 'Submit for approval', 'Submit for approval: done');
  await nav(inspector, `#/doc/equipment_operator_auth/${op}`, /^Operator authorisation EQO-/);
  await act(inspector, 'Approve', 'Approve: done');
  await nav(eng, `#/fleet/${eq}`, /Excavator 07$/);
  await eng.getByRole('link', { name: 'Record a log' }).click();
  await eng.getByRole('heading', { level: 1, name: 'Record an equipment log' }).waitFor();
  if ((await eng.getByLabel('Opening meter').inputValue()) !== '2000') fail('the opening meter is not taken from the machine');
  await eng.getByLabel('Operator').fill('op-abebe');
  await eng.getByLabel('Closing meter').fill('2008');
  await eng.getByLabel('Productive hours').fill('6');
  await eng.getByLabel('Idle hours').fill('2');
  await eng.getByLabel('Fuel (litres)').fill('96');
  await audit(eng, 'log form');
  await eng.getByRole('button', { name: 'Save log' }).click();
  await eng.getByRole('heading', { level: 1, name: /^Equipment log EQL-/ }).waitFor();
  const lg = await idFromHash(eng);
  await act(eng, 'Submit for approval', 'Submit for approval: done');
  const pm = await as('pm');
  await nav(pm, '#/home', 'My work');
  await pm.getByRole('link', { name: /Equipment logs to approve/ }).waitFor();
  await nav(pm, `#/doc/equipment_log/${lg}`, /^Equipment log EQL-/);
  await act(pm, 'Approve', 'Approve: done');
  log('log recorded and approved');

  // ---- 5. The command centre shows what the approved log says -------------------------
  await nav(pm, '#/fleet', 'Fleet command centre');
  const usage = await pm.getByRole('table', { name: /Usage from approved logs/ }).locator('tbody tr').allTextContents();
  if (usage.length !== 1 || !/Excavator 07.*6\.00.*2\.00.*0\.00.*75\.0%.*100\.0%.*12\.00/.test(usage[0])) fail(`usage row: ${usage.join(' || ')}`);
  const reg = await pm.getByRole('table', { name: 'Equipment register' }).locator('tbody tr').allTextContents();
  if (reg.length !== 1 || !/Excavator 07.*Assigned.*P-API.*2,008\.00 h/.test(reg[0])) fail(`register row: ${reg.join(' || ')}`);
  // Fuel: the log says 96 L and no stock issue put fuel into the machine, so the difference is listed for explanation.
  const fuelRows = await pm.getByRole('table', { name: /Fuel: dispensed, logged and expected/ }).locator('tbody tr').allTextContents();
  if (fuelRows.length !== 1 || !/Excavator 07.*0\.00.*96\.00.*8\.00/.test(fuelRows[0]) || !/Approved logs record 96\.00 L; stock issues to this machine total 0\.00 L/.test(fuelRows[0]) || !/No fuel standard is set/.test(fuelRows[0])) fail(`fuel row: ${fuelRows.join(' || ')}`);
  await audit(pm, 'fleet command centre');
  await snap(pm, 'fleet-command-centre');
  await nav(pm, `#/fleet/${eq}`, /Excavator 07$/);
  const hist = await pm.getByRole('table', { name: /Lifecycle history/ }).locator('tbody tr').count();
  if (hist !== 7) fail(`history rows: ${hist}`);
  await audit(pm, 'equipment detail');
  await snap(pm, 'equipment-detail');

  // ---- 6. Phone width, dark theme ---------------------------------------------------------
  const phone = await as('engPhone', { viewport: { width: 390, height: 844 }, colorScheme: 'dark' });
  await nav(phone, `#/fleet/${eq}`, /Excavator 07$/);
  await audit(phone, 'equipment at 390px (dark)');
  await nav(phone, '#/fleet', 'Fleet command centre');
  await audit(phone, 'fleet at 390px (dark)');
  await snap(phone, 'mobile-fleet-dark');
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
console.log('\nCONSOLE FLEET: PASS');
