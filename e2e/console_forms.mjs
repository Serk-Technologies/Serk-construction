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
  const proj = p.getByLabel('Project', { exact: true });
  const cc = p.getByLabel(F.cc, { exact: true }).first();
  const typed = F.typed ? p.getByLabel(F.typed, { exact: true }).first() : null;
  const submit = p.getByRole('button', { name: F.submit });
  const offers = async (code) => (await options(cc)).includes(code);
  const waitOffers = (code) => p.waitForFunction(([label, c]) => {
    const el = [...document.querySelectorAll('select')].find((s) => s.getAttribute('aria-label') === label || (s.labels && [...s.labels].some((l) => l.textContent.trim() === label)));
    return el && [...el.options].some((o) => o.textContent.includes(c));
  }, [F.cc, code]);

  await selectByText(proj, 'P-API');
  await waitOffers('01-MAT');

  // 1. older answer after newer
  let b = await barrier(p, CC);
  await selectByText(proj, 'P-TWO');           // request for P-TWO is held
  await b.arrived();
  await selectByText(proj, 'P-API');           // request for P-API is answered
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
  await selectByText(proj, 'P-TWO');
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
  await selectByText(proj, 'P-API');
  await b.arrived();
  if (!(await submit.isDisabled())) fail(`${F.name}: the form could be sent while its cost codes were loading`);
  await b.release();
  await b.stop();
  await p.waitForFunction((label) => { const x = [...document.querySelectorAll('button[type=submit]')].find((e) => e.textContent.trim() === label || true); return x && !x.disabled; }, F.submit);

  // 4. an older failure after a newer success is not reported
  b = await barrier(p, CC);
  await selectByText(proj, 'P-TWO');
  await b.arrived();
  await selectByText(proj, 'P-API');
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

await browser.close();
if (failures.length) { console.log(`${failures.length} failure(s)`); process.exit(1); }
console.log('forms: all checks passed');
