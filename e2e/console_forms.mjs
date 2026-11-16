// Browser regressions for the console's form state (XCT-FORM). Every dependent
// form that reloads cost codes when its project changes is driven through:
//   1. an older answer released after a newer one never replaces the newer
//      choices or a line the person typed (explicit request barrier, no timers);
//   2. a line's cost code that the new project does not offer is marked
//      unresolved — the typed line stays, the form refuses to send until a new
//      choice is made, and choosing resolves it;
//   3. the submit button is disabled while choices are loading;
//   4. an older request's failure, arriving after a newer success, is not shown.
import { as, nav, selectByText, barrier, stays, fail, failures, log, browser, FX } from './console_lib.mjs';

const CC = /\/v1\/projects\/[^/]+\/cost-codes/;
const FORMS = [
  { name: 'requisition', role: 'siteEng', hash: '#/new/requisition', heading: 'New requisition', cc: 'Cost code, line 1', typed: 'Description, line 1', submit: 'Create requisition' },
  { name: 'budget revision', role: 'pm', hash: '#/new/budget_revision', heading: 'New budget revision', cc: 'Cost code, line 1', typed: 'Amount, line 1', submit: 'Create budget revision' },
  { name: 'change event', role: 'pm', hash: '#/new/change_event', heading: 'Raise a change event', cc: 'Cost code, line 1', typed: 'Note, line 1', submit: 'Create change event' },
  { name: 'BOQ', role: 'estimator', hash: '#/new/boq', heading: /BOQ/, cc: 'Cost code, line 1', typed: 'Description, line 1', submit: /Create/ },
  { name: 'schedule', role: 'planner', hash: '#/new/schedule', heading: 'New schedule', cc: 'Cost code, line 1', typed: 'Name, line 1', submit: 'Compute and save', exact: true },
  { name: 'budget transfer', role: 'pm', hash: '#/new/budget_transfer', heading: 'Request budget transfer', cc: 'From cost code', submit: 'Submit transfer request' },
  // DEF-004: the remaining project-driven forms.
  { name: 'contingency drawdown', role: 'pm', hash: '#/new/contingency_drawdown', heading: 'Request contingency drawdown', cc: 'To cost code', typed: 'Reason', submit: 'Create drawdown request' },
  { name: 'stock issue', role: 'store', hash: '#/new/stockissue', heading: 'Issue stock to a project', cc: 'Cost code', typed: 'Quantity', submit: 'Request issue' },
  // The fuel form follows the machine: Grader A works at P-API, Grader B at P-TWO.
  { name: 'fuel dispense', role: 'store', hash: '#/new/fuel_dispense', heading: 'Dispense fuel to a machine', driver: 'Equipment', a: 'Grader A', b: 'Grader B', cc: 'Cost code', typed: 'Operator', submit: 'Prepare issue' },
  // A machine in the yard: the part's project is chosen on the form.
  { name: 'work-order part', role: 'storeHQ', hash: `#/new/work_order_part/${FX.workOrder}`, heading: /^Request a part for /, driver: 'Project charged', cc: 'Cost code', typed: 'Quantity', submit: 'Request part' },
];

const options = async (sel) => (await sel.locator('option').allTextContents()).join(' | ');
const dialogs = {};

// XCT-LOC.3: the Ethiopian date on My work, and the console's conversion on every sample, equal pkg/ethcal's.
{
  const p = await as('pm');
  await nav(p, '#/home', 'My work');
  const shown = await p.locator('.ethdate').first().textContent();
  if (!shown.startsWith(FX.ethToday + ' ')) fail(`My work shows ${shown}, pkg/ethcal says today is ${FX.ethToday}`);
  const wrong = await p.evaluate((samples) => Object.entries(samples).filter(([g, e]) => {
    const [y, m, d] = g.split('-').map(Number);
    return window.serkCalendar.ethiopianText(new Date(y, m - 1, d)) !== e;
  }).slice(0, 5), FX.ethSamples);
  if (wrong.length) fail(`the console's Ethiopian calendar disagrees with pkg/ethcal: ${JSON.stringify(wrong)}`);
  log(`Ethiopian calendar: ${Object.keys(FX.ethSamples).length} sampled days agree; today ${FX.ethToday}`);
}

for (const F of FORMS) {
  const p = await as(F.role);
  if (!dialogs[F.role]) { dialogs[F.role] = []; p.on('dialog', (d) => { dialogs[F.role].push(d.message()); d.accept(); }); }
  await nav(p, F.hash, F.heading);
  const proj = p.getByLabel(F.driver || 'Project', { exact: true });
  const A = F.a || 'P-API', B = F.b || 'P-TWO';
  const cc = p.getByLabel(F.cc, { exact: true }).first();
  const typed = F.typed ? p.getByLabel(F.typed, { exact: true }).first() : null;
  const submit = p.getByRole('button', { name: F.submit });
  const offers = async (code) => (await options(cc)).includes(code);
  const waitOffers = (code) => p.waitForFunction(([label, c]) => {
    const el = [...document.querySelectorAll('select')].find((s) => s.getAttribute('aria-label') === label || (s.labels && [...s.labels].some((l) => l.textContent.trim() === label)));
    return el && [...el.options].some((o) => o.textContent.includes(c));
  }, [F.cc, code]);

  await selectByText(proj, A);
  await waitOffers('01-MAT');

  // 1. older answer after newer
  let b = await barrier(p, CC);
  await selectByText(proj, B);           // request for P-TWO is held
  await b.arrived();
  await selectByText(proj, A);           // request for P-API is answered
  await waitOffers('01-MAT');
  if (typed) await typed.fill('typed before the late answer');
  await b.release();                            // P-TWO's answer arrives last
  await b.stop();
  const seen = await stays(cc, (el) => [...el.options].map((o) => o.textContent).join('|'));
  if (seen.length !== 1 || seen[0].includes('02-LAB') || !seen[0].includes('01-MAT')) fail(`${F.name}: an older answer replaced the newer choices: ${JSON.stringify(seen)}`);
  if (typed) {
    const kept = await stays(typed, (el) => el.value);
    if (kept.length !== 1 || kept[0] !== 'typed before the late answer') fail(`${F.name}: a late answer touched a typed line: ${JSON.stringify(kept)}`);
  }

  // 2. a choice the new project does not offer is unresolved, never remapped
  await selectByText(cc, '01-MAT');
  await selectByText(proj, B);
  await waitOffers('02-LAB');
  if (await cc.getAttribute('aria-invalid') !== 'true') fail(`${F.name}: a cost code of the previous project was not marked unresolved (options: ${await options(cc)}; value ${await cc.inputValue()})`);
  if (await cc.inputValue() !== '') fail(`${F.name}: the line was silently remapped to ${await cc.inputValue()}`);
  if (typed && await typed.inputValue() !== 'typed before the late answer') fail(`${F.name}: the typed line was lost on a project change`);
  await submit.click();
  const alert = await p.locator('#alert').textContent();
  if (!/chosen again/.test(alert || '')) fail(`${F.name}: the form did not refuse an unresolved choice (alert: ${alert})`);
  await selectByText(cc, '02-LAB');
  if (await cc.getAttribute('aria-invalid') === 'true') fail(`${F.name}: choosing again did not resolve the line`);

  // 3. submission waits for choices
  b = await barrier(p, CC);
  await selectByText(proj, A);
  await b.arrived();
  if (!(await submit.isDisabled())) fail(`${F.name}: the form could be sent while its cost codes were loading`);
  await b.release();
  await b.stop();
  await p.waitForFunction((label) => { const x = [...document.querySelectorAll('button[type=submit]')].find((e) => e.textContent.trim() === label || true); return x && !x.disabled; }, F.submit);

  // 4. an older failure after a newer success is not reported
  b = await barrier(p, CC);
  await selectByText(proj, B);
  await b.arrived();
  await selectByText(proj, A);
  await waitOffers('01-MAT');
  await b.release(500);
  await b.stop();
  const alerts = await stays(p.locator('#alert'), (el) => el.textContent.trim());
  if (alerts.some((x) => /held answer failed/.test(x))) fail(`${F.name}: a stale failure was reported after a newer success`);
  // 5. leaving a form with typed input asks first
  const asked = dialogs[F.role].length;
  await nav(p, '#/home', 'My work');
  if (dialogs[F.role].length !== asked + 1) fail(`${F.name}: leaving a form with typed input did not ask first`);
  log(F.name, 'form state checks done');
}

// CNX-07.2 (0087): the project manager asks the site engineer to do something; only the engineer marks it done.
{
  const accept = (role, p) => { if (!dialogs[role]) { dialogs[role] = []; p.on('dialog', (d) => { dialogs[role].push(d.message()); d.accept(); }); } };
  const pm = await as('pm'); accept('pm', pm);
  const se = await as('siteEng'); accept('siteEng', se);
  await nav(se, '#/home', 'My work');
  await nav(pm, '#/tasks', 'Tasks');
  await pm.getByLabel('Task', { exact: true }).fill('Book the crane for the bridge beams');
  await pm.getByLabel('Assigned to', { exact: true }).fill('u-se');
  await pm.getByRole('button', { name: 'Assign task' }).click();
  await pm.getByRole('table').filter({ hasText: 'Book the crane for the bridge beams' }).filter({ hasText: 'u-se' }).waitFor();
  if (await pm.getByRole('button', { name: 'Mark done' }).count()) fail('the person who asked is offered to complete the task');
  await nav(se, '#/home', 'My work');
  await se.getByRole('link', { name: /Tasks assigned to you/ }).filter({ hasText: '1' }).waitFor();
  await nav(se, '#/tasks', 'Tasks');
  await se.getByRole('button', { name: 'Mark done' }).first().click();
  await se.getByRole('table').filter({ hasText: 'Book the crane for the bridge beams' }).filter({ hasText: 'DONE' }).waitFor();
  log('task assigned by the project manager and done by the site engineer');
}

// EST-06 (0094): the estimator prices resources, the commercial manager approves them; the build-up of
// 1 m³ of C25 concrete reproduces the Bible's example (7,400 direct, 8,140 full, 9,576.47 at 15% margin).
{
  const est = await as('estimator');
  const com = await as('commercial');
  for (const p of [est, com]) p.on('dialog', (d) => d.accept());
  const idOf = async (p) => (await p.evaluate(() => location.hash)).split('/').pop();
  const approve = async (type, id, heading) => {
    await nav(com, `#/doc/${type}/${id}`, heading);
    await com.getByRole('button', { name: 'Approve', exact: true }).click();
    await com.locator('p.notice').filter({ hasText: 'Approve: done' }).first().waitFor();
  };
  const rates = {};
  for (const [code, kind, unit, rate] of [['C25-MIX', 'Material', 'm3', '5800'], ['CREW-CONC', 'Labour', 'hr', '95'], ['MIXER-05', 'Equipment', 'hr', '325']]) {
    await nav(est, '#/new/resource_rate', 'New resource rate');
    await est.getByLabel('Code', { exact: true }).fill(code);
    await est.getByLabel('Description', { exact: true }).fill(`${code} for slabs`);
    await selectByText(est.getByLabel('Kind', { exact: true }), kind);
    await est.getByLabel('Unit', { exact: true }).fill(unit);
    await est.getByLabel('Rate', { exact: true }).fill(rate);
    await est.getByLabel('Source', { exact: true }).fill('Supplier quotation SQ-114');
    await est.getByLabel('Region', { exact: true }).fill('Addis Ababa');
    await est.getByLabel('Valid to', { exact: true }).fill('2028-12-31');
    await est.getByRole('button', { name: 'Record rate' }).click();
    await est.getByRole('heading', { level: 1, name: `Resource rate ${code}` }).waitFor();
    rates[code] = await idOf(est);
    if (await est.getByRole('button', { name: 'Approve', exact: true }).count()) fail('the recorder of a rate is offered its approval');
    await approve('resource_rate', rates[code], `Resource rate ${code}`);
  }
  log('three resource rates recorded and approved by a second person');

  await nav(est, '#/new/rate_buildup', 'New rate build-up');
  await selectByText(est.getByLabel('Project', { exact: true }), 'P-API');
  await est.getByLabel('Code', { exact: true }).fill('C25-SLAB');
  await est.getByLabel('Description', { exact: true }).fill('C25 concrete in suspended slabs');
  await est.getByLabel('Unit of output', { exact: true }).fill('m3');
  await est.getByLabel('Overhead %', { exact: true }).fill('10');
  await est.getByLabel('Margin %', { exact: true }).fill('15');
  const lines = [['C25-MIX', '1'], ['CREW-CONC', '10'], ['MIXER-05', '2']];
  for (let i = 0; i < lines.length; i++) {
    if (i) await est.getByRole('button', { name: 'Add line' }).click();
    await selectByText(est.getByLabel(`Resource rate, line ${i + 1}`), lines[i][0]);
    await est.getByLabel(`Quantity, line ${i + 1}`).fill(lines[i][1]);
  }
  await est.getByRole('button', { name: 'Create build-up' }).click();
  await est.getByRole('heading', { level: 1, name: 'Rate build-up C25-SLAB/R1' }).waitFor();
  const bu = await idOf(est);
  const figures = est.getByRole('table', { name: 'Rate build-up' });
  for (const [item, amount] of [['Direct cost', '7,400.00'], ['Overhead (10', '740.00'], ['Full cost', '8,140.00'], ['Selling rate at 15', '9,576.47']]) {
    const row = figures.getByRole('row').filter({ hasText: item });
    if (!(await row.textContent()).includes(amount)) fail(`build-up ${item}: ${await row.textContent()}, want ${amount}`);
  }
  await est.getByText(/Equivalent markup on full cost: 17\.6471%/).waitFor();
  await est.getByRole('button', { name: 'Submit for approval', exact: true }).click();
  await est.locator('p.notice').filter({ hasText: 'Submit for approval: done' }).first().waitFor();
  if (await est.getByRole('button', { name: 'Approve', exact: true }).count()) fail('the author of a build-up is offered its approval');
  await approve('rate_buildup', bu, 'Rate build-up C25-SLAB/R1');
  await nav(est, `#/doc/rate_buildup/${bu}`, 'Rate build-up C25-SLAB/R1');
  await est.getByText('Fixed when the build-up was submitted; an approved build-up never changes.').waitFor();
  await est.getByRole('button', { name: 'Start a revision' }).waitFor();
  log('rate build-up priced at 9,576.47 per m3, submitted and approved (frozen)');
}

await browser.close();
if (failures.length) { console.log(`${failures.length} failure(s)`); process.exit(1); }
console.log('forms: all checks passed');
