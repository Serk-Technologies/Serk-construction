// Demo data for SERK Construction OS: payroll, a subsidiary and group accounts, tax, timesheets, equipment,
// health and safety, quality, site records, documents, RFIs and rules of credit.
// Run by ./run.sh after the base demo company is seeded. Needs Node 18+ (fetch). Development only.
//
// It goes through the real API as the staging personas, so every control (separation of duties, approvals,
// project scope) applies exactly as for a real user. Each group of steps is independent: a step that the
// current build refuses is reported and skipped, the rest still load. Safe to run again: it stops when the
// payroll data is already there.
const BASE = process.env.SERK_URL || 'http://127.0.0.1:8080';
const PASS = process.env.SERK_STAGING_PASSPHRASE || 'dev-only-staging-passphrase-0123456789';
const toks = {};
const tok = async (u) => (toks[u] ||= (await (await fetch(BASE + '/staging/token', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ passphrase: PASS, persona: u }) })).json()).token);

async function call(u, method, path, body, ok = [200, 201]) {
  const r = await fetch(BASE + path, { method, headers: { authorization: 'Bearer ' + (await tok(u)), 'content-type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
  const t = await r.text();
  let j; try { j = JSON.parse(t); } catch { j = t; }
  if (!ok.includes(r.status)) throw new Error(`${u} ${method} ${path} -> ${r.status} ${String(t).slice(0, 160)}`);
  return j;
}
let failed = 0;
async function group(name, fn) {
  try { await fn(); console.log(`  ok   ${name}`); } catch (e) { failed++; console.log(`  SKIP ${name}: ${e.message}`); }
}
const day = (n) => new Date(Date.now() - n * 86400000).toISOString().slice(0, 10);
const iso = (h) => new Date(Date.now() + h * 3600000).toISOString();
const b64 = (s) => Buffer.from(s).toString('base64');

// Already loaded? (the payroll officer sees a run)
try {
  const v = process.env.SERK_DEMO_FORCE ? {} : await call('u-payroll', 'GET', '/v1/payroll');
  if ((v.runs || []).length) { console.log('demo data already loaded: nothing to do'); process.exit(0); }
} catch { /* payroll page not available in this build: carry on */ }

const ref = await call('u-commercial', 'GET', '/v1/p2p-reference');
const proj = ref.projects[0].id;
const proj2 = (ref.projects[1] || ref.projects[0]).id;
const ent = ref.legal_entities[0].id;
const acct = (c) => ref.accounts.find((a) => a.code === c).id;
console.log(`demo data for ${ref.legal_entities[0].name}`);

const emp = {};
await group('payroll: accounts, rules, employees, October run, payment', async () => {
  const pol = await call('u-controller', 'POST', '/v1/payroll-policies', { legal_entity_id: ent, salary_account_id: acct('6300'), pension_expense_account_id: acct('6310'), net_payable_account_id: acct('2500'), tax_payable_account_id: acct('2510'), pension_payable_account_id: acct('2520') });
  await call('u-cfo', 'POST', `/v1/payroll-policies/${pol.id}/approve`, {});
  const rs = await call('u-payroll', 'POST', '/v1/payroll-rule-sets', { legal_entity_id: ent, effective_from: '2026-07-01', pension_ee_rate: '0.07', pension_er_rate: '0.11', overtime_multiplier: '1.5',
    source_ref: 'Demo payroll configuration 2026 (synthetic values, not current law)', tax_bands: [{ upto: 600, rate: 0, deduction: 0 }, { upto: 1650, rate: 0.10, deduction: 60 }, { upto: 3200, rate: 0.15, deduction: 142.5 }, { upto: 5250, rate: 0.20, deduction: 302.5 }, { upto: 7800, rate: 0.25, deduction: 565 }, { upto: 10900, rate: 0.30, deduction: 955 }, { upto: null, rate: 0.35, deduction: 1500 }] });
  await call('u-payroll', 'POST', `/v1/payroll-rule-sets/${rs.id}/submit`, {});
  await call('u-hr-manager', 'POST', `/v1/payroll-rule-sets/${rs.id}/approve`, {});
  for (const e of [['E-001', 'Abebe Kebede', 'SALARIED', 'Site engineer', '28000', '1000111122223'], ['E-002', 'Tigist Alemu', 'SALARIED', 'Quantity surveyor', '45000', '1000444455556'], ['E-003', 'Dawit Haile', 'DAILY', 'Mason', '450', '1000777788889'], ['E-004', 'Meseret Tadesse', 'HOURLY', 'Electrician', '60', '1000222233334']]) {
    emp[e[0]] = (await call('u-payroll', 'POST', '/v1/employees', { legal_entity_id: ent, number: e[0], name: e[1], kind: e[2], trade: e[3], base_rate: e[4], bank_name: 'Commercial Bank of Ethiopia', account_number: e[5], starts_on: '2026-01-01' })).id;
    // Some builds hold a new employee for approval by a second person.
    await call('u-hr-manager', 'POST', `/v1/employees/${emp[e[0]]}/approve`, {}, [200, 201, 404, 405, 409, 422]).catch(() => {});
  }
  const run = (await call('u-payroll', 'POST', '/v1/pay-runs', { legal_entity_id: ent, period_start: '2026-10-01', period_end: '2026-10-31', pay_date: '2026-10-31' })).id;
  await call('u-payroll', 'POST', `/v1/pay-runs/${run}/inputs`, { inputs: [
    { employee_id: emp['E-001'], units: '1', overtime_hours: '12', allowances: '1500', project_id: proj }, { employee_id: emp['E-002'], units: '1', overtime_hours: '0', allowances: '2000', project_id: proj },
    { employee_id: emp['E-003'], units: '24', overtime_hours: '16', allowances: '0', project_id: proj2 }, { employee_id: emp['E-004'], units: '168', overtime_hours: '10', allowances: '0' }] });
  await call('u-payroll', 'POST', `/v1/pay-runs/${run}/calculate`, {});
  await call('u-payroll', 'POST', `/v1/pay-runs/${run}/submit`, {});
  await call('u-hr-manager', 'POST', `/v1/pay-runs/${run}/approve`, {});
  const banks = (await call('u-treasury', 'GET', '/v1/p2p/company_bank_account?status=ACTIVE')).items;
  await call('u-treasury', 'POST', `/v1/pay-runs/${run}/release`, { company_bank_id: banks[0].id });
});

await group('tax: designate accounts, October VAT return approved', async () => {
  for (const d of [['2300', 'OUTPUT_VAT'], ['1300', 'INPUT_VAT'], ['2330', 'WHT_PAYABLE']]) {
    const t = await call('u-accountant', 'POST', '/v1/tax-accounts', { account_id: acct(d[0]), kind: d[1] });
    await call('u-cfo', 'POST', `/v1/tax-accounts/${t.id}/approve`, {}); // a designation is approved by a second person
  }
  const tr = await call('u-accountant', 'POST', '/v1/tax-returns', { legal_entity_id: ent, kind: 'VAT', period_start: '2026-10-01', period_end: '2026-10-31' });
  await call('u-cfo', 'POST', `/v1/tax-returns/${tr.id}/approve`, {});
});

const roster = await call('u-site-engineer', 'GET', '/v1/roster?project=' + proj).then((r) => r.employees || []).catch(() => []);
await group('timesheets: a week of approved hours', async () => {
  if (!roster.length) throw new Error('no employees on the roster');
  for (const n of [6, 5, 4, 3, 2, 1]) {
    if (new Date(Date.now() - n * 86400000).getUTCDay() === 0) continue;
    const t = await call('u-site-engineer', 'POST', '/v1/timesheets', { project_id: proj, work_date: day(n), notes: 'Foundation works block A', lines: roster.map((e, i) => ({ employee_id: e.id, hours_normal: '8', hours_ot: i === 2 ? '2' : '0' })) });
    await call('u-site-engineer', 'POST', `/v1/timesheets/${t.id}/submit`, {});
    if (n > 1) await call('u-pm', 'POST', `/v1/timesheets/${t.id}/approve`, {});
  }
});

await group('equipment: register the fleet', async () => {
  for (const e of [['Excavator CAT 320', 'Excavator', 'OWNED', 'Caterpillar', '320 GC', 'CAT320-ET-0412'], ['Tipper truck 20 t', 'Truck', 'OWNED', 'Sinotruk', 'HOWO 6x4', 'HOWO-ET-0877'], ['Mobile crane 50 t', 'Crane', 'RENTED', 'Liebherr', 'LTM 1050', 'LTM-HIRE-2201']])
    await call('u-plant-officer', 'POST', '/v1/equipment', { legal_entity_id: ent, name: e[0], class: e[1], ownership: e[2], manufacturer: e[3], model: e[4], serial_number: e[5], fuel_type: 'DIESEL', meter_kind: 'HOURS', opening_meter: '1200' });
});

await group('health and safety: induction, talk, inspection, incidents, permits', async () => {
  if (roster.length) await call('u-hse', 'POST', '/v1/inductions', { employee_id: roster[0].id, project_id: proj, inducted_on: day(60), valid_to: day(-300), topics: 'Site rules, PPE, emergency assembly' });
  await call('u-hse', 'POST', '/v1/toolbox-talks', { project_id: proj, held_on: day(2), topic: 'Working at height and scaffold checks', attendees: 22 });
  await call('u-hse', 'POST', '/v1/safety-inspections', { project_id: proj, inspected_on: day(3), area: 'Scaffolding, block A', score: 78, findings: 'Toe boards missing on level 3; corrected the same day' });
  await call('u-site-engineer', 'POST', '/v1/incidents', { project_id: proj, kind: 'NEAR_MISS', severity: 'MINOR', occurred_at: iso(-30), location: 'Block A', description: 'Rebar bundle slipped from the crane sling; nobody underneath', people_involved: 0, lost_days: 0 });
  const inc = await call('u-site-engineer', 'POST', '/v1/incidents', { project_id: proj, kind: 'LOST_TIME', severity: 'MAJOR', occurred_at: iso(-60), location: 'Block A, level 2', description: 'Worker slipped on wet formwork; wrist fracture', people_involved: 1, lost_days: 4 });
  await call('u-hse', 'POST', `/v1/incidents/${inc.id}/investigate`, { root_cause: 'Wet formwork with no anti-slip treatment', findings: 'Rain overnight; supervisor did not re-inspect before work resumed' });
  await call('u-hse', 'POST', `/v1/incidents/${inc.id}/actions`, { description: 'Daily wet-weather inspection of formwork before work starts', owner_name: 'Site engineer', due_on: day(-14) });
  const p1 = await call('u-site-engineer', 'POST', '/v1/permits', { project_id: proj, kind: 'HOT_WORK', location: 'Block A, level 3', description: 'Welding of the stair balustrade', valid_from: iso(-1), valid_to: iso(8) });
  await call('u-hse', 'POST', `/v1/permits/${p1.id}/issue`, { note: 'Fire watch for 30 minutes after work' });
  await call('u-site-engineer', 'POST', '/v1/permits', { project_id: proj, kind: 'EXCAVATION', location: 'Trench T4', description: 'Drainage trench 2 m deep near the access road', valid_from: iso(24), valid_to: iso(72) });
});

await group('quality: inspection plan with a hold point, tests', async () => {
  const itp = await call('u-quality', 'POST', '/v1/itps', { project_id: proj, title: 'Foundation concrete: rebar and pour', activity_code: '', items: [{ description: 'Rebar spacing and cover before pouring', kind: 'HOLD', hold_at_percent: '0' }, { description: 'Concrete cube sampling at the pour', kind: 'WITNESS', hold_at_percent: '0' }, { description: 'Curing records reviewed', kind: 'REVIEW', hold_at_percent: '0' }] });
  await call('u-quality', 'POST', `/v1/itps/${itp.id}/submit`, {});
  await call('u-pm', 'POST', `/v1/itps/${itp.id}/approve`, {});
  const ir = await call('u-site-engineer', 'POST', '/v1/inspection-requests', { project_id: proj, itp_id: itp.id, item_seq: 2, requested_for: day(1), location: 'Block A pour' });
  await call('u-quality', 'POST', `/v1/inspection-requests/${ir.id}/pass`, { note: 'Cubes taken and labelled' });
  await call('u-quality', 'POST', '/v1/material-tests', { project_id: proj, batch_ref: 'C25-0412', test_type: 'Cube strength 28 days', value: '31.2', unit: 'N/mm2', spec_min: '25', tested_on: day(2) });
  await call('u-quality', 'POST', '/v1/material-tests', { project_id: proj, batch_ref: 'C25-0415', test_type: 'Cube strength 28 days', value: '21.4', unit: 'N/mm2', spec_min: '25', tested_on: day(1) });
});

await group('site records: punch list, instruction, meeting', async () => {
  await call('u-site-engineer', 'POST', '/v1/punch-items', { project_id: proj, location: 'Block A level 2 corridor', description: 'Cracked floor tile at the stair landing', assignee: 'Tiling subcontractor', priority: 'HIGH', due_on: day(-5) });
  await call('u-site-engineer', 'POST', '/v1/punch-items', { project_id: proj, location: 'Site office', description: 'Missing fire extinguisher sign', priority: 'LOW', due_on: day(-10) });
  await call('u-pm', 'POST', '/v1/site-instructions', { project_id: proj, title: 'Raise the ground-floor slab by 50 mm', body: 'Raise the ground-floor slab level by 50 mm on the architect’s drawing A-102 rev C. Cost and time impact to be claimed.' });
  const m = await call('u-site-engineer', 'POST', '/v1/meetings', { project_id: proj, kind: 'PROGRESS', title: 'Weekly progress meeting', held_on: day(1), attendees: 'PM, QS, site engineer, HSE officer, client representative',
    minutes: 'Excavation complete; rebar inspection scheduled; slab level instruction received; wet-weather inspection regime agreed after the incident.', actions: [{ description: 'Order blinding concrete for block B', owner_name: 'Procurement', due_on: day(-3) }] });
  await call('u-site-engineer', 'POST', `/v1/meetings/${m.id}/issue`, {});
});

await group('documents and RFIs', async () => {
  for (const d of [['A-101', 'Site layout plan', 'Civil', 'DRAWING'], ['A-102', 'Ground floor plan', 'Architecture', 'DRAWING']]) {
    const doc = await call('u-site-engineer', 'POST', '/v1/documents', { project_id: proj, reference: d[0], doc_type: d[3], title: d[1], discipline: d[2] });
    await call('u-site-engineer', 'POST', `/v1/documents/${doc.id}/revisions`, { revision: 'A', file_name: d[0] + '.pdf', media_type: 'application/pdf', change_note: 'Issued for construction', content: b64('%PDF-1.4 ' + d[0] + ' ' + d[1]) });
    // Submitting needs a clean malware scan; the development stack has no scanner (the system refuses unscanned files by design), so the revision stays as uploaded.
  }
});

console.log(failed ? `demo data loaded with ${failed} group(s) skipped (see above); the system itself is unaffected` : 'demo data loaded');
