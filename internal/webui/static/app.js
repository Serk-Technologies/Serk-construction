// SERK Construction OS operations console: one page application for every
// module (projects and budgets, procurement, site, stores, commercial and
// revenue, payables, treasury, accounting, governance).
//
// Map of this file (search for the "// ----------" section markers):
//   DOM helpers ........ h(), $(), field builders
//   DOCS ............... every document type: list, detail, actions, forms
//   NAV ................ the sidebar categories (scrollable, collapsible)
//   page / guides ...... page() renders a page; guidePanel() adds the
//                        "How this page works" card from guide.js
//   route() ............ the hash router (#/list/..., #/doc/..., #/new/...)
//   FORMS .............. the create forms, one per document
//   render*() .......... special pages (budget, cost, stock, journals ...)
//   theme .............. day / night switch
//
// Security model: this page holds no authority. The access token lives only
// in memory (never in storage); every action is an API call that the kernel
// and PostgreSQL re-authorize, including separation of duties, project scope,
// MFA step-up and budget checks. Server data is only ever inserted as text
// (no innerHTML), under a CSP that forbids inline script.

import { guideFor } from './guide.js';

const S = { token: null, me: null, ref: null, can: new Set() };

// ---------- DOM helpers ----------
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'on') for (const [ev, fn] of Object.entries(v)) el.addEventListener(ev, fn);
    else if (k === 'value') el.value = v;
    else el.setAttribute(k, v === true ? '' : String(v));
  }
  for (const c of kids.flat(Infinity)) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}
const $ = (id) => document.getElementById(id);
let uid = 0;
const nextId = (p) => `${p}-${++uid}`;

function field(label, control, hint) {
  const id = control.id || (control.id = nextId('f'));
  const kids = [h('label', { for: id }, label), control];
  if (hint) {
    const hid = nextId('h');
    control.setAttribute('aria-describedby', hid);
    kids.push(h('span', { class: 'hint', id: hid }, hint));
  }
  return h('div', { class: 'field' }, kids);
}
const input = (name, attrs = {}) => h('input', { name, ...attrs });
function select(name, options, attrs = {}) {
  const s = h('select', { name, ...attrs });
  for (const o of options) s.append(h('option', { value: o.value }, o.label));
  return s;
}

function say(msg) { const s = $('status'); s.textContent = ''; setTimeout(() => { s.textContent = msg; }, 30); }
function showError(err) {
  const a = $('alert');
  a.textContent = '';
  a.append(h('strong', {}, errorTitle(err)), ' ', err.message || String(err));
  a.hidden = false;
  a.scrollIntoView({ block: 'nearest' });
}
function clearError() { $('alert').hidden = true; }
function errorTitle(e) {
  if (!(e instanceof ApiError)) return 'Something went wrong.';
  if (e.status === 401 && /insufficient_user_authentication/.test(e.challenge || '')) return 'Multi-factor sign-in required.';
  if (e.status === 401) return 'Your session is no longer valid.';
  if (e.status === 403) return 'Not permitted.';
  if (e.status === 404) return 'Not found.';
  if (e.status === 409) return 'Refused by a business rule.';
  if (e.status === 422 || e.status === 400) return 'Please correct the input.';
  if (e.status === 429) return 'Too many requests; wait a moment.';
  return 'The server could not complete the request.';
}

// ---------- API ----------
class ApiError extends Error {
  constructor(status, code, message, challenge) { super(message); this.status = status; this.code = code; this.challenge = challenge; }
}
async function api(method, path, body, raw) {
  const init = { method, headers: { Authorization: 'Bearer ' + S.token } };
  if (raw !== undefined) { init.headers['Content-Type'] = 'application/json'; init.body = raw; }
  else if (body !== undefined) { init.headers['Content-Type'] = 'application/json'; init.body = JSON.stringify(body); }
  const r = await fetch(path, init);
  let data = null;
  try { data = await r.json(); } catch { /* empty body */ }
  if (!r.ok) {
    const e = (data && data.error) || {};
    throw new ApiError(r.status, e.code || 'error', e.message || r.statusText, r.headers.get('WWW-Authenticate'));
  }
  return data;
}

// ---------- formatting & exact decimals ----------
function money(s) {
  if (s == null || s === '') return '';
  s = String(s);
  const neg = s.startsWith('-');
  if (neg) s = s.slice(1);
  let [i, f = ''] = s.split('.');
  f = f.replace(/0+$/, '');
  f = (f + '00').slice(0, Math.max(2, f.length));
  i = i.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  return (neg ? '−' : '') + i + '.' + f;
}
const SC = 1000000n; // 6 decimal places, as stored
function dparse(s) {
  s = String(s ?? '').trim();
  if (!/^-?\d+(\.\d{1,6})?$/.test(s)) return null;
  const neg = s[0] === '-';
  if (neg) s = s.slice(1);
  const [i, f = ''] = s.split('.');
  const v = BigInt(i) * SC + BigInt(f.padEnd(6, '0'));
  return neg ? -v : v;
}
function dfmt(v) {
  const neg = v < 0n;
  if (neg) v = -v;
  let f = (v % SC).toString().padStart(6, '0').replace(/0+$/, '');
  if (f.length < 2) f = f.padEnd(2, '0');
  return (neg ? '-' : '') + (v / SC).toString() + '.' + f;
}
const dmul = (a, b) => (a * b) / SC;
const when = (s) => (s ? new Date(s).toLocaleString() : '');
const words = (k) => k.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase());

const OK = /^(APPROVED|ACTIVE|CONFIRMED|SETTLED|MATCHED|RECONCILED|POSTED|COMPLIANT|AWARDED)/;
const BAD = /^(REJECTED|CANCELLED|BLOCKED|EXCEPTION|NON_COMPLIANT|REVOKED|CLOSED)/;
function chip(status) {
  const cls = OK.test(status) ? 'chip ok' : BAD.test(status) ? 'chip bad' : 'chip warn';
  return h('span', { class: cls }, String(status).replace(/_/g, ' '));
}

// ---------- document catalogue ----------
// actions: [action, permission, mode]; mode: undefined | 'opt' (optional
// reason) | 'req' (reason required) | 'award' | 'evaluate'.
const DOCS = {
  budget_version: { title: 'Budget versions', one: 'Budget version', base: '/v1/budgets/revisions', create: 'budget.revise', form: 'budget_revision',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'budget.revise']], SUBMITTED: [['approve', 'budget.approve', 'opt'], ['reject', 'budget.approve', 'req']] } },
  budget_transfer: { title: 'Budget transfers', one: 'Budget transfer', base: '/v1/budgets/transfers', create: 'budget.transfer', form: 'budget_transfer',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'budget.transfer']], SUBMITTED: [['approve', 'budget.approve', 'opt'], ['reject', 'budget.approve', 'req']] } },
  project_stage_gate: { title: 'Stage gates', one: 'Stage gate', base: '/v1/project-gates', create: 'project.gate.submit', form: null,
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'project.gate.submit']], SUBMITTED: [['approve', 'project.gate.decide', 'opt'], ['reject', 'project.gate.decide', 'req']] } },
  forecast_cycle: { title: 'Cost forecasts', one: 'Cost forecast', base: '/v1/forecasts', create: 'forecast.prepare', form: 'forecast',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'CANCELLED'],
    actions: { DRAFT: [['submit', 'forecast.prepare'], ['cancel', 'forecast.prepare', 'req']], SUBMITTED: [['approve', 'forecast.approve', 'opt'], ['reject', 'forecast.approve', 'req']] } },
  change_event: { title: 'Change events', one: 'Change event', base: '/v1/change-events', create: 'change.manage', form: 'change_event',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'change.manage']], SUBMITTED: [['approve', 'change.approve', 'opt'], ['reject', 'change.approve', 'req']] } },
  contract_notice: { title: 'Contract notices', one: 'Contract notice', base: '/v1/notices', create: 'claim.manage', form: 'notice',
    statuses: ['OPEN', 'SENT'], actions: {} },
  contract_claim: { title: 'Claims', one: 'Claim', base: '/v1/claims', create: 'claim.manage', form: 'claim',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'claim.manage']], SUBMITTED: [['approve', 'claim.approve', 'opt'], ['reject', 'claim.approve', 'req']] } },
  ld_terms: { title: 'Damages terms', one: 'Damages terms', base: '/v1/ld-terms', create: 'claim.manage', form: null, statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'claim.manage']], SUBMITTED: [['approve', 'claim.approve', 'opt'], ['reject', 'claim.approve', 'req']] } },
  guarantee: { title: 'Guarantees', one: 'Guarantee', base: '/v1/guarantees', create: 'bond.manage', form: null, statuses: ['ACTIVE', 'RELEASED', 'CALLED'], actions: {} },
  price_index_value: { title: 'Index values', one: 'Index value', base: '/v1/price-index-values', create: 'escalation.manage', form: null,
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'escalation.manage']], SUBMITTED: [['approve', 'escalation.approve', 'opt'], ['reject', 'escalation.approve', 'req']] } },
  escalation_formula: { title: 'Price adjustment formulas', one: 'Price adjustment formula', base: '/v1/escalation/formulas', create: 'escalation.manage', form: null,
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'escalation.manage']], SUBMITTED: [['approve', 'escalation.approve', 'opt'], ['reject', 'escalation.approve', 'req']] } },
  price_adjustment: { title: 'Price adjustments', one: 'Price adjustment', base: '/v1/price-adjustments', create: 'escalation.manage', form: null,
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'escalation.manage']], SUBMITTED: [['approve', 'escalation.approve', 'opt'], ['reject', 'escalation.approve', 'req']] } },
  uom_conversion: { title: 'Unit conversions', one: 'Unit conversion', base: '/v1/stock/uom-conversions', create: 'stock.item.manage', form: 'uom_conversion',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'stock.item.manage']], SUBMITTED: [['approve', 'stock.item.approve', 'opt'], ['reject', 'stock.item.approve', 'req']] } },
  authority_matrix: { title: 'Authority matrices', one: 'Authority matrix', base: '/v1/authority/matrices', create: 'authority.manage', form: 'authority_matrix',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'RETIRED'],
    actions: { DRAFT: [['submit', 'authority.manage']], SUBMITTED: [['approve', 'authority.approve', 'opt'], ['reject', 'authority.approve', 'req']] } },
  delegation: { title: 'Delegations of authority', one: 'Delegation', base: '/v1/delegations', create: 'delegation.create', form: 'delegation',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'REVOKED'],
    actions: { DRAFT: [['submit', 'delegation.create']], SUBMITTED: [['approve', 'delegation.approve', 'opt'], ['reject', 'delegation.approve', 'req'], ['revoke', 'delegation.create|delegation.approve', 'req']],
      APPROVED: [['revoke', 'delegation.create|delegation.approve', 'req']] } },
  sod_exception: { title: 'Duty-conflict exceptions', one: 'Duty-conflict exception', base: '/v1/sod/exceptions', create: 'authority.manage', form: 'sod_exception',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'authority.manage']], SUBMITTED: [['approve', 'authority.approve', 'opt'], ['reject', 'authority.approve', 'req']] } },
  contingency_drawdown: { title: 'Contingency drawdowns', one: 'Contingency drawdown', base: '/v1/budgets/drawdowns', create: 'budget.revise', form: 'contingency_drawdown',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'budget.revise']], SUBMITTED: [['approve', 'budget.approve', 'opt'], ['reject', 'budget.approve', 'req']] } },
  boq_version: { title: 'BOQ versions', one: 'BOQ version', base: '/v1/boqs', create: 'boq.manage', form: 'boq',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'SUPERSEDED'],
    actions: { DRAFT: [['submit', 'boq.manage']], SUBMITTED: [['approve', 'boq.approve', 'opt'], ['reject', 'boq.approve', 'req']] } },
  purchase_requisition: { title: 'Requisitions', one: 'Requisition', base: '/v1/requisitions', create: 'pr.create', form: 'requisition',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'pr.create']], SUBMITTED: [['approve', 'pr.approve', 'opt'], ['reject', 'pr.approve', 'req']] } },
  rfq: { title: 'RFQs', one: 'RFQ', base: '/v1/rfqs', create: 'rfq.manage', form: 'rfq',
    statuses: ['DRAFT', 'ISSUED', 'AWARDED', 'CANCELLED'],
    actions: { DRAFT: [['issue', 'rfq.manage']], ISSUED: [['award', 'rfq.award', 'award']] } },
  rfq_quote: { title: 'Quotes', one: 'Quote', base: '/v1/quotes', create: 'rfq.manage', form: 'quote',
    statuses: ['PENDING', 'COMPLIANT', 'NON_COMPLIANT'],
    actions: { PENDING: [['evaluate', 'rfq.evaluate', 'evaluate']] } },
  purchase_order: { title: 'Purchase orders', one: 'Purchase order', base: '/v1/purchase-orders', create: 'po.manage', form: 'po',
    statuses: ['DRAFT', 'APPROVED', 'CLOSED', 'CANCELLED'],
    actions: { DRAFT: [['approve', 'po.approve', 'opt']] } },
  grn: { title: 'Goods receipts', one: 'Goods receipt', base: '/v1/goods-receipts', create: 'grn.create', form: 'grn',
    statuses: ['DRAFT', 'SUBMITTED', 'CONFIRMED'],
    actions: { DRAFT: [['submit', 'grn.create']], SUBMITTED: [['confirm', 'grn.confirm']] } },
  supplier_invoice: { title: 'Supplier invoices', one: 'Supplier invoice', base: '/v1/invoices', create: 'ap.enter', form: 'invoice',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'ap.enter']], SUBMITTED: [['approve', 'ap.approve', 'opt'], ['reject', 'ap.approve', 'req']] } },
  payment_instruction: { title: 'Payments', one: 'Payment instruction', base: '/v1/payments', create: 'payment.prepare', form: 'payment',
    statuses: ['DRAFT', 'SUBMITTED', 'PARTIALLY_APPROVED', 'APPROVED', 'RELEASED', 'SETTLED', 'CANCELLED'],
    actions: {
      DRAFT: [['submit', 'payment.prepare'], ['cancel', 'payment.prepare', 'req']],
      SUBMITTED: [['approve', 'payment.approve'], ['cancel', 'payment.approve', 'req']],
      PARTIALLY_APPROVED: [['approve', 'payment.approve'], ['cancel', 'payment.approve', 'req']],
      APPROVED: [['release', 'payment.release'], ['cancel', 'payment.approve', 'req']] } },
  supplier: { title: 'Suppliers', one: 'Supplier', base: '/v1/suppliers', create: 'supplier.manage', form: 'supplier',
    statuses: ['PENDING', 'ACTIVE', 'BLOCKED'],
    actions: { PENDING: [['approve', 'supplier.approve']], ACTIVE: [['block', 'supplier.approve', 'req']] } },
  supplier_bank_account: { title: 'Supplier bank accounts', one: 'Supplier bank account', base: '/v1/supplier-bank-accounts', create: 'supplier.manage', form: 'supplierbank',
    statuses: ['PENDING', 'APPROVED', 'REVOKED'], actions: { PENDING: [['approve', 'supplier.bank.approve']] } },
  company_bank_account: { title: 'Company bank accounts', one: 'Company bank account', base: '/v1/company-bank-accounts', create: 'bankacct.manage', form: 'companybank',
    statuses: ['PENDING', 'ACTIVE', 'CLOSED'], actions: { PENDING: [['approve', 'bankacct.approve']] } },
  p2p_policy: { title: 'P2P policies', one: 'P2P policy', base: '/v1/p2p/policies', create: 'p2p.policy.manage', form: 'policy',
    statuses: ['DRAFT', 'APPROVED', 'REJECTED', 'RETIRED'],
    actions: { DRAFT: [['approve', 'p2p.policy.approve'], ['reject', 'p2p.policy.approve']] } },
  // Contract to cash (Journey B).
  customer: { title: 'Customers', one: 'Customer', base: '/v1/customers', create: 'customer.manage', form: 'customer',
    statuses: ['PENDING', 'ACTIVE', 'BLOCKED'], actions: { PENDING: [['approve', 'customer.approve']] } },
  contract_policy: { title: 'Contract policies', one: 'Contract policy', base: '/v1/contract-policies', create: 'contract.policy.manage', form: 'contractpolicy',
    statuses: ['DRAFT', 'APPROVED', 'REJECTED', 'RETIRED'],
    actions: { DRAFT: [['approve', 'contract.policy.approve'], ['reject', 'contract.policy.approve']] } },
  contract: { title: 'Contracts', one: 'Contract', base: '/v1/contracts', create: 'contract.manage', form: 'contract',
    statuses: ['DRAFT', 'SUBMITTED', 'ACTIVE', 'REJECTED', 'CLOSED'],
    actions: { DRAFT: [['submit', 'contract.manage']], SUBMITTED: [['approve', 'contract.approve', 'opt'], ['reject', 'contract.approve', 'req']] } },
  measurement: { title: 'Measurements', one: 'Measurement', base: '/v1/measurements', create: 'measure.create', form: 'measurement',
    statuses: ['DRAFT', 'SUBMITTED', 'VERIFIED', 'REJECTED'],
    actions: { DRAFT: [['submit', 'measure.create']], SUBMITTED: [['verify', 'measure.verify'], ['reject', 'measure.verify', 'req']] } },
  ipc: { title: 'Payment certificates', one: 'Payment certificate', base: '/v1/ipcs', create: 'ipc.prepare', form: 'ipc',
    statuses: ['DRAFT', 'SUBMITTED', 'CERTIFIED', 'INVOICED', 'REJECTED', 'CANCELLED'],
    actions: {
      DRAFT: [['submit', 'ipc.prepare'], ['cancel', 'ipc.prepare', 'req']],
      SUBMITTED: [['certify', 'ipc.certify', 'certify'], ['reject', 'ipc.certify', 'req'], ['cancel', 'ipc.prepare', 'req']],
      CERTIFIED: [['invoice', 'ar.invoice.create', 'invoice']] } },
  customer_invoice: { title: 'Customer invoices', one: 'Customer invoice', base: '/v1/customer-invoices',
    statuses: ['DRAFT', 'APPROVED'], actions: { DRAFT: [['approve', 'ar.invoice.approve']] } },
  customer_receipt: { title: 'Receipts', one: 'Receipt', base: '/v1/receipts', create: 'ar.receipt.create', form: 'receipt',
    statuses: ['DRAFT', 'CONFIRMED', 'CANCELLED'], actions: { DRAFT: [['confirm', 'ar.receipt.confirm'], ['cancel', 'ar.receipt.create', 'req'], ['cancel', 'ar.receipt.confirm', 'req']] } },
  contract_variation: { title: 'Variations', one: 'Variation', base: '/v1/variations',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'CANCELLED'],
    actions: { DRAFT: [['submit', 'contract.manage'], ['cancel', 'contract.manage', 'req']],
      SUBMITTED: [['approve', 'contract.approve'], ['reject', 'contract.approve', 'req'], ['cancel', 'contract.manage', 'req']] } },
  customer_credit_note: { title: 'Credit notes', one: 'Credit note', base: '/v1/credit-notes',
    statuses: ['DRAFT', 'APPROVED', 'CANCELLED'],
    actions: { DRAFT: [['approve', 'ar.credit.approve'], ['cancel', 'ar.credit.create', 'req'], ['cancel', 'ar.credit.approve', 'req']] } },
  retention_release: { title: 'Retention releases', one: 'Retention release', base: '/v1/retention-releases',
    statuses: ['DRAFT', 'APPROVED', 'CANCELLED'],
    actions: { DRAFT: [['approve', 'ar.retention.approve'], ['cancel', 'ar.retention.create', 'req'], ['cancel', 'ar.retention.approve', 'req']] } },
  // Stock (Journey C).
  stock_item: { title: 'Stock items', one: 'Stock item', base: '/v1/stock-items', create: 'stock.item.manage', form: 'stockitem',
    statuses: ['PENDING', 'ACTIVE'], actions: { PENDING: [['approve', 'stock.item.approve']] } },
  warehouse: { title: 'Warehouses', one: 'Warehouse', create: 'warehouse.manage', form: 'warehouse', statuses: ['ACTIVE'], actions: {} },
  stock_receipt: { title: 'Put-aways', one: 'Put-away', statuses: ['POSTED'], actions: {} },
  stock_issue: { title: 'Stock issues', one: 'Stock issue', base: '/v1/stock-issues', create: 'stock.issue', form: 'stockissue',
    statuses: ['DRAFT', 'APPROVED', 'REJECTED', 'CANCELLED'],
    actions: { DRAFT: [['approve', 'stock.issue.approve'], ['reject', 'stock.issue.approve', 'req'], ['cancel', 'stock.issue', 'req']] } },
  stock_count: { title: 'Stock counts', one: 'Stock count', base: '/v1/stock-counts', create: 'stock.count', form: 'stockcount',
    statuses: ['DRAFT', 'APPROVED', 'REJECTED'], actions: { DRAFT: [['approve', 'stock.adjust.approve'], ['reject', 'stock.adjust.approve', 'req']] } },
  stock_transfer: { title: 'Stock transfers', one: 'Stock transfer', base: '/v1/stock-transfers', create: 'stock.transfer', form: 'stocktransfer',
    statuses: ['DRAFT', 'APPROVED', 'REJECTED', 'CANCELLED'],
    actions: { DRAFT: [['approve', 'stock.transfer.approve'], ['reject', 'stock.transfer.approve', 'req'], ['cancel', 'stock.transfer', 'req']] } },
  schedule_version: { title: 'Schedules', one: 'Schedule', base: '/v1/schedules', create: 'schedule.manage', form: 'schedule',
    statuses: ['DRAFT', 'SUBMITTED', 'BASELINE', 'REJECTED', 'SUPERSEDED'],
    actions: { DRAFT: [['submit', 'schedule.manage']], SUBMITTED: [['approve', 'schedule.approve'], ['reject', 'schedule.approve', 'req']] } },
  schedule_progress: { title: 'Progress reports', one: 'Progress report', base: '/v1/progress', create: 'schedule.manage', form: 'progress',
    statuses: ['DRAFT', 'APPROVED', 'REJECTED'], actions: { DRAFT: [['approve', 'schedule.approve'], ['reject', 'schedule.approve', 'req']] } },
  // Site operations (0037): diaries prepared online or on a field device.
  site_diary: { title: 'Site diaries', one: 'Site diary', base: '/v1/site/diaries', create: 'site.diary.manage', form: 'diary',
    statuses: ['DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'CANCELLED'],
    actions: { DRAFT: [['submit', 'site.diary.manage'], ['cancel', 'site.diary.manage', 'req'], ['reject', 'site.diary.approve', 'req']],
      SUBMITTED: [['approve', 'site.diary.approve'], ['reject', 'site.diary.approve', 'req']] } },
  money_in: { title: 'Money received', one: 'Money received', statuses: ['UNAPPLIED', 'RECORDED'], actions: {},
    link: (d) => (d.status === 'UNAPPLIED' && S.can.has('ar.receipt.create') ? `#/new/receipt/${d.id}` : null) },
};
const ACTION_LABEL = { submit: 'Submit for approval', approve: 'Approve', reject: 'Reject', issue: 'Issue to suppliers', award: 'Award',
  evaluate: 'Record evaluation', confirm: 'Confirm receipt', release: 'Release payment', cancel: 'Cancel', block: 'Block supplier',
  verify: 'Verify measurement', certify: 'Certify', invoice: 'Issue customer invoice', revoke: 'Revoke' };

// Sidebar: categories in the order work flows through a construction company.
// Each category folds; the folded set is remembered per browser (NAV_KEY).
const NAV = [
  ['Overview', [['#/home', 'My work', null], ['#/me', 'My profile', null], ['#/setup', 'Company setup', 'p2p.read|tenant.settings.manage|org.manage'], ['#/dashboard', 'Dashboard', 'p2p.read|intelligence.read|journal.read'], ['#/ask', 'Ask', 'intelligence.read'], ['#/risk', 'Risks', 'intelligence.read'], ['#/reports', 'Reports', null]]],
  ['Projects & planning', [['#/projects', 'Projects', 'p2p.read|project.create|project.update|project.gate.decide'], ['#/list/project_stage_gate', 'Stage gates', 'p2p.read|project.gate.submit|project.gate.decide'], ['#/budget', 'Budget overview', 'p2p.read'], ['#/list/forecast_cycle', 'Cost forecasts', 'p2p.read'], ['#/list/change_event', 'Change events', 'p2p.read'], ['#/list/budget_version', 'Budget versions', 'p2p.read'], ['#/list/budget_transfer', 'Budget transfers', 'p2p.read'], ['#/list/contingency_drawdown', 'Contingency drawdowns', 'p2p.read'], ['#/list/boq_version', 'BOQ', 'p2p.read'], ['#/cost', 'Cost report', 'p2p.read'],
    ['#/list/schedule_version', 'Schedules', 'p2p.read'], ['#/list/schedule_progress', 'Progress', 'p2p.read'], ['#/evm', 'Earned value', 'p2p.read']]],
  ['Procurement', [['#/list/purchase_requisition', 'Requisitions', 'p2p.read'], ['#/list/rfq', 'RFQs', 'p2p.read'], ['#/list/rfq_quote', 'Quotes', 'p2p.read'],
    ['#/list/purchase_order', 'Purchase orders', 'p2p.read'], ['#/list/supplier', 'Suppliers', 'p2p.read']]],
  ['Site', [['#/list/site_diary', 'Site diaries', 'p2p.read'], ['#/list/grn', 'Goods receipts', 'p2p.read'],
    ['#/site/devices', 'Field devices', 'site.device.register|site.device.manage']]],
  ['Stores', [['#/stock', 'Stock on hand', 'p2p.read'], ['#/list/stock_issue', 'Stock issues', 'p2p.read'], ['#/list/stock_receipt', 'Put-aways', 'p2p.read'],
    ['#/list/stock_transfer', 'Transfers', 'p2p.read'], ['#/list/stock_count', 'Stock counts', 'p2p.read'],
    ['#/reservations', 'Reservations', 'p2p.read'], ['#/report/low-stock', 'Low stock', 'p2p.read'], ['#/list/uom_conversion', 'Unit conversions', 'p2p.read'],
    ['#/list/stock_item', 'Stock items', 'p2p.read'], ['#/list/warehouse', 'Warehouses', 'p2p.read']]],
  ['Commercial & revenue', [['#/list/contract', 'Contracts', 'p2p.read'], ['#/list/contract_variation', 'Variations', 'p2p.read'], ['#/list/contract_notice', 'Contract notices', 'p2p.read'], ['#/list/contract_claim', 'Claims', 'p2p.read'],
    ['#/obligations', 'Contract obligations', 'p2p.read'],
    ['#/bonds', 'Guarantees & price adjustment', 'p2p.read|bond.manage|bond.approve|escalation.manage|escalation.approve'],
    ['#/list/measurement', 'Measurements', 'p2p.read'], ['#/list/ipc', 'Payment certificates', 'p2p.read'],
    ['#/list/customer_invoice', 'Customer invoices', 'p2p.read'], ['#/list/customer_credit_note', 'Credit notes', 'p2p.read'],
    ['#/list/retention_release', 'Retention releases', 'p2p.read'], ['#/list/money_in', 'Money received', 'ar.receipt.create|ar.receipt.confirm'],
    ['#/list/customer_receipt', 'Receipts', 'scope.tenant_wide'], ['#/aging/ar', 'Receivables aging', 'scope.tenant_wide'], ['#/list/customer', 'Customers', 'p2p.read']]],
  ['Payables', [['#/list/supplier_invoice', 'Supplier invoices', 'p2p.read'], ['#/list/payment_instruction', 'Payments', 'p2p.read'],
    ['#/aging/ap', 'Payables aging', 'scope.tenant_wide']]],
  ['Treasury & banking', [['#/bank', 'Bank statements', 'bank.import|bank.reconcile'], ['#/list/company_bank_account', 'Company banks', 'scope.tenant_wide'],
    ['#/list/supplier_bank_account', 'Supplier banks', 'p2p.read']]],
  ['Accounting', [['#/journals', 'Journals', 'journal.read'], ['#/recon', 'Reconciliation', 'journal.read'], ['#/tb', 'Trial balance', 'journal.read']]],
  ['Governance', [['#/people', 'People & access', 'user.manage|role.manage|audit.read'], ['#/org', 'Organisation & calendar', 'p2p.read|org.manage'], ['#/authority', 'Authority & duties', 'p2p.read|authority.manage|authority.approve|delegation.create|delegation.approve'], ['#/list/authority_matrix', 'Authority matrices', 'authority.manage|authority.approve'],
    ['#/list/delegation', 'Delegations', 'delegation.create|delegation.approve'], ['#/list/sod_exception', 'Duty-conflict exceptions', 'authority.manage|authority.approve'],
    ['#/imports', 'Data imports', 'supplier.manage|customer.manage|stock.item.manage'], ['#/list/p2p_policy', 'P2P policies', 'scope.tenant_wide'], ['#/list/contract_policy', 'Contract policies', 'scope.tenant_wide'],
    ['#/controls', 'Control review', 'scope.tenant_wide'], ['#/automation', 'Automation rules', 'automation.manage|automation.activate|workflow.read'],
    ['#/settings', 'Company settings', null]]],
];
const NAV_KEY = 'serk.nav.folded';
const allowed = (perm) => !perm || perm.split('|').some((p) => S.can.has(p));

const QUEUES = [
  ['budget.approve', 'budget_version', 'SUBMITTED', 'Budget revisions to approve'],
  ['budget.approve', 'budget_transfer', 'SUBMITTED', 'Budget transfers to approve'],
  ['budget.approve', 'contingency_drawdown', 'SUBMITTED', 'Contingency drawdowns to approve'],
  ['authority.approve', 'authority_matrix', 'SUBMITTED', 'Authority matrices to approve'],
  ['project.gate.decide', 'project_stage_gate', 'SUBMITTED', 'Stage gates to decide'],
  ['forecast.approve', 'forecast_cycle', 'SUBMITTED', 'Cost forecasts to approve'],
  ['change.approve', 'change_event', 'SUBMITTED', 'Change events to decide'],
  ['claim.approve', 'contract_claim', 'SUBMITTED', 'Claims to approve for sending'],
  ['claim.approve', 'ld_terms', 'SUBMITTED', 'Damages terms to approve'],
  ['escalation.approve', 'price_index_value', 'SUBMITTED', 'Index values to approve'],
  ['escalation.approve', 'escalation_formula', 'SUBMITTED', 'Price adjustment formulas to approve'],
  ['escalation.approve', 'price_adjustment', 'SUBMITTED', 'Price adjustments to approve'],
  ['delegation.approve', 'delegation', 'SUBMITTED', 'Delegations to approve'],
  ['authority.approve', 'sod_exception', 'SUBMITTED', 'Duty-conflict exceptions to approve'],
  ['boq.approve', 'boq_version', 'SUBMITTED', 'BOQs to approve'],
  ['pr.approve', 'purchase_requisition', 'SUBMITTED', 'Requisitions to approve'],
  ['rfq.manage', 'purchase_requisition', 'APPROVED', 'Approved requisitions'],
  ['rfq.manage', 'rfq', 'DRAFT', 'RFQs to issue'],
  ['rfq.evaluate', 'rfq_quote', 'PENDING', 'Quotes to evaluate'],
  ['rfq.award', 'rfq', 'ISSUED', 'RFQs awaiting award'],
  ['po.manage', 'rfq', 'AWARDED', 'Awarded RFQs'],
  ['po.approve', 'purchase_order', 'DRAFT', 'Purchase orders to approve'],
  ['grn.create', 'purchase_order', 'APPROVED', 'Open purchase orders'],
  ['grn.create', 'grn', 'DRAFT', 'Receipts to submit'],
  ['grn.confirm', 'grn', 'SUBMITTED', 'Receipts to confirm'],
  ['ap.enter', 'supplier_invoice', 'DRAFT', 'Invoices to submit'],
  ['ap.approve', 'supplier_invoice', 'SUBMITTED', 'Invoices to approve'],
  ['payment.prepare', 'payment_instruction', 'DRAFT', 'Payments to submit'],
  ['payment.approve', 'payment_instruction', 'SUBMITTED', 'Payments to approve'],
  ['payment.approve', 'payment_instruction', 'PARTIALLY_APPROVED', 'Payments needing 2nd approval'],
  ['payment.release', 'payment_instruction', 'APPROVED', 'Payments to release'],
  ['supplier.approve', 'supplier', 'PENDING', 'Suppliers to approve'],
  ['supplier.bank.approve', 'supplier_bank_account', 'PENDING', 'Supplier bank accounts to approve'],
  ['bankacct.approve', 'company_bank_account', 'PENDING', 'Company bank accounts to approve'],
  ['p2p.policy.approve', 'p2p_policy', 'DRAFT', 'Policies to approve'],
  ['bank.reconcile', 'bank_statement', 'OPEN', 'Statements to reconcile'],
  ['customer.approve', 'customer', 'PENDING', 'Customers to approve'],
  ['contract.policy.approve', 'contract_policy', 'DRAFT', 'Contract policies to approve'],
  ['contract.approve', 'contract', 'SUBMITTED', 'Contracts to approve'],
  ['contract.manage', 'contract_variation', 'DRAFT', 'Variations to submit'],
  ['contract.approve', 'contract_variation', 'SUBMITTED', 'Variations to approve'],
  ['measure.create', 'measurement', 'DRAFT', 'Measurements to submit'],
  ['measure.verify', 'measurement', 'SUBMITTED', 'Measurements to verify'],
  ['ipc.prepare', 'ipc', 'DRAFT', 'Certificates to submit'],
  ['ipc.certify', 'ipc', 'SUBMITTED', 'Certificates to certify'],
  ['ar.invoice.create', 'ipc', 'CERTIFIED', 'Certified IPCs to invoice'],
  ['ar.invoice.approve', 'customer_invoice', 'DRAFT', 'Customer invoices to approve'],
  ['ar.credit.approve', 'customer_credit_note', 'DRAFT', 'Credit notes to approve'],
  ['ar.retention.approve', 'retention_release', 'DRAFT', 'Retention releases to approve'],
  ['ar.receipt.create', 'money_in', 'UNAPPLIED', 'Money received to apply'],
  ['ar.receipt.confirm', 'customer_receipt', 'DRAFT', 'Receipts to confirm'],
  ['schedule.manage', 'schedule_version', 'DRAFT', 'Schedules to submit'],
  ['schedule.approve', 'schedule_version', 'SUBMITTED', 'Schedules to approve'],
  ['schedule.approve', 'schedule_progress', 'DRAFT', 'Progress reports to approve'],
  ['site.diary.manage', 'site_diary', 'DRAFT', 'Site diaries to submit'],
  ['site.diary.approve', 'site_diary', 'SUBMITTED', 'Site diaries to approve'],
  ['stock.item.approve', 'stock_item', 'PENDING', 'Stock items to approve'],
  ['stock.receive', 'grn', 'CONFIRMED', 'Goods receipts to put away'],
  ['stock.issue.approve', 'stock_issue', 'DRAFT', 'Stock issues to approve'],
  ['stock.adjust.approve', 'stock_count', 'DRAFT', 'Stock counts to approve'],
  ['stock.transfer.approve', 'stock_transfer', 'DRAFT', 'Stock transfers to approve'],
];

// ---------- shell ----------
function page(title, lede, ...kids) {
  const v = $('view');
  v.textContent = '';
  const h1 = h('h1', { tabindex: '-1' }, title);
  v.append(...[h1, lede ? h('p', { class: 'lede' }, lede) : null, guidePanel(), ...kids].filter(Boolean));
  document.title = `${title} — SERK Construction`;
  h1.focus();
  return v;
}
// ---------- page guides ("How this page works", see guide.js) ----------
// Route key of the current page: "home", "budget", "list/purchase_order",
// "doc/purchase_order", "new/requisition" ...
function routeKey() {
  const [, view, a] = location.hash.split('/');
  if (!view || view === 'home') return 'home';
  return ['list', 'doc', 'new'].includes(view) ? `${view}/${a}` : view;
}
// The role catalogue (GET /v1/meta/roles) says who may do what; loaded once.
let ROLES = null;
let rolesLoading = null;
function loadRoles() {
  if (!rolesLoading) rolesLoading = api('GET', '/v1/meta/roles').then((r) => { ROLES = r.roles || {}; }).catch(() => { ROLES = {}; });
  return rolesLoading;
}
const GUIDE_KEY = 'serk.guide.closed';
function guideClosed() { try { return new Set(JSON.parse(localStorage.getItem(GUIDE_KEY) || '[]')); } catch { return new Set(); } }
function fillGuide(box, g) {
  const body = box.querySelector('.guidebody');
  body.textContent = '';
  if (g.what) body.append(h('p', { class: 'what' }, g.what));
  const cols = h('div', { class: 'guidecols' });
  if (g.steps && g.steps.length) cols.append(h('div', {}, h('h3', {}, 'How to use it'), h('ol', {}, g.steps.map((x) => h('li', {}, x)))));
  if (g.life && g.life.length) cols.append(h('div', {}, h('h3', {}, 'Life cycle'), h('ul', { class: 'life' }, g.life.map((x) => h('li', {}, x)))));
  if (g.who && g.who.length) {
    cols.append(h('div', {}, h('h3', {}, 'Who does it'), h('dl', {}, g.who.map(([k, rs]) => [h('dt', {}, k), h('dd', {}, rs.length ? rs.join(', ') : (ROLES ? 'No role in this company holds this duty' : 'Loading…'))]))));
  }
  if (g.rules && g.rules.length) cols.append(h('div', {}, h('h3', {}, 'The system enforces'), h('ul', {}, g.rules.map((x) => h('li', {}, x)))));
  body.append(cols);
  if (g.related && g.related.length) body.append(h('p', { class: 'related' }, 'Related: ', g.related.map(([href, label], i) => [i ? ' · ' : '', h('a', { href }, label)])));
}
function guidePanel() {
  if (!S.token) return null;
  const key = routeKey();
  const g = guideFor(key, DOCS, ROLES, ACTION_LABEL);
  if (!g) return null;
  const box = h('details', { class: 'guide' }, h('summary', {}, 'How this page works'), h('div', { class: 'guidebody' }));
  if (!guideClosed().has(key)) box.open = true;
  box.addEventListener('toggle', () => {
    const set = guideClosed();
    if (box.open) set.delete(key); else set.add(key);
    try { localStorage.setItem(GUIDE_KEY, JSON.stringify([...set])); } catch { /* storage unavailable */ }
  });
  fillGuide(box, g);
  // Roles arrive once per session; refresh the "Who does it" lines then.
  if (!ROLES) loadRoles().then(() => { if (box.isConnected) fillGuide(box, guideFor(key, DOCS, ROLES, ACTION_LABEL)); });
  return box;
}

function navFolded() { try { return new Set(JSON.parse(localStorage.getItem(NAV_KEY) || '[]')); } catch { return new Set(); } }
function saveFolded(set) { try { localStorage.setItem(NAV_KEY, JSON.stringify([...set])); } catch { /* storage unavailable */ } }
function setGroupOpen(sec, open) {
  sec.classList.toggle('folded', !open);
  sec.querySelector('.ghead').setAttribute('aria-expanded', String(open));
}
function rememberFolds() {
  const set = new Set();
  for (const sec of $('nav').querySelectorAll('.group')) if (sec.classList.contains('folded')) set.add(sec.dataset.group);
  saveFolded(set);
}
function buildNav() {
  const nav = $('nav');
  nav.textContent = '';
  // Phones: the menu folds away behind one button.
  const tog = h('button', { type: 'button', class: 'ghost navtoggle', 'aria-expanded': 'false', 'aria-controls': 'navbody' }, 'Menu');
  tog.addEventListener('click', () => { const o = nav.classList.toggle('open'); tog.setAttribute('aria-expanded', String(o)); });
  nav.addEventListener('click', (e) => { if (e.target.closest('a')) { nav.classList.remove('open'); tog.setAttribute('aria-expanded', 'false'); } });
  nav.append(tog);

  const body = h('div', { class: 'navbody', id: 'navbody' });
  const find = h('input', { type: 'search', class: 'navfind', placeholder: 'Find a page', 'aria-label': 'Find a page in the menu', autocomplete: 'off' });
  const expand = h('button', { type: 'button', class: 'linkbtn' }, 'Expand all');
  const collapse = h('button', { type: 'button', class: 'linkbtn' }, 'Collapse all');
  body.append(h('div', { class: 'navtools' }, h('span', { class: 'navlabel' }, 'Workspace'), expand, collapse), find);

  const folded = navFolded();
  for (const [group, items] of NAV) {
    const vis = items.filter(([, , p]) => allowed(p));
    if (!vis.length) continue;
    const id = 'g-' + group.toLowerCase().replace(/[^a-z]+/g, '-');
    const head = h('button', { type: 'button', class: 'ghead', 'aria-controls': id, 'aria-expanded': 'true' },
      h('span', { class: 'gname' }, group), h('span', { class: 'gcount', 'aria-hidden': 'true' }, String(vis.length)),
      h('span', { class: 'chev', 'aria-hidden': 'true' }));
    const sec = h('section', { class: 'group', 'data-group': group },
      h('h2', {}, head), h('ul', { id }, vis.map(([href, label]) => h('li', {}, h('a', { href }, label)))));
    setGroupOpen(sec, !folded.has(group));
    head.addEventListener('click', () => { setGroupOpen(sec, sec.classList.contains('folded')); rememberFolds(); });
    body.append(sec);
  }
  const all = (open) => { for (const sec of body.querySelectorAll('.group')) setGroupOpen(sec, open); rememberFolds(); };
  expand.addEventListener('click', () => all(true));
  collapse.addEventListener('click', () => all(false));
  const empty = h('p', { class: 'navempty', hidden: '' }, 'No page matches.');
  body.append(empty);
  // Filtering shows matching pages in every category, folded or not.
  find.addEventListener('input', () => {
    const q = find.value.trim().toLowerCase();
    nav.classList.toggle('filtering', !!q);
    let any = false;
    for (const sec of body.querySelectorAll('.group')) {
      let n = 0;
      for (const li of sec.querySelectorAll('li')) {
        const hit = !q || li.textContent.toLowerCase().includes(q) || sec.dataset.group.toLowerCase().includes(q);
        li.hidden = !hit; if (hit) n++;
      }
      sec.hidden = n === 0; if (n) any = true;
    }
    empty.hidden = any;
  });
  find.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { find.value = ''; find.dispatchEvent(new Event('input')); }
    if (e.key === 'Enter') { const a = body.querySelector('.group:not([hidden]) li:not([hidden]) a'); if (a) a.click(); }
  });
  nav.append(body);
  nav.hidden = false;
  markNav();
}
function markNav() {
  const cur = location.hash.split('/').slice(0, 3).join('/');
  for (const a of $('nav').querySelectorAll('a')) {
    if (a.getAttribute('href') === cur || (cur.startsWith('#/doc/') && a.getAttribute('href') === '#/list/' + location.hash.split('/')[2])) {
      a.setAttribute('aria-current', 'page');
      // The current page is never hidden inside a folded category.
      const sec = a.closest('.group');
      if (sec && sec.classList.contains('folded')) setGroupOpen(sec, true);
      if (a.scrollIntoView) a.scrollIntoView({ block: 'nearest' });
    } else a.removeAttribute('aria-current');
  }
}

async function route() {
  clearError();
  closeSheet();
  if (!S.token) return renderSignin();
  markNav();
  const [, view, a, b] = location.hash.split('/');
  try {
    switch (view) {
      case 'list': return await renderList(a, b ? decodeURIComponent(b) : '');
      case 'doc': return await renderDoc(a, b);
      case 'new': return await renderForm(a, b);
      case 'journals': return await renderJournals();
      case 'bank': return await renderBank();
      case 'recon': return await renderRecon();
      case 'budget': return await renderBudget();
      case 'report': return await renderCatalogueReport(a);
      case 'reservations': return await renderReservations();
      case 'projects': return await renderProjects();
      case 'setup': return await renderSetup();
      case 'imports': return await renderImports(a);
      case 'org': return await renderOrg();
      case 'dashboard': return await renderDashboard(a);
      case 'authority': return await renderAuthority();
      case 'cost': return await renderCost(a);
      case 'stock': return await renderStock();
      case 'evm': return await renderEVM();
      case 'site': return await renderDevices();
      case 'reports': return await renderReports();
      case 'tb': return await renderTB();
      case 'aging': return await renderAging(a === 'ar' ? 'ar' : 'ap');
      case 'ask': return await renderAsk();
      case 'risk': return await renderRisk(a);
      case 'controls': return await renderControls();
      case 'automation': return await renderAutomation();
      case 'settings': return await renderSettings();
      case 'people': return await renderPeople(a);
      case 'me': return await renderMe();
      case 'bonds': return await renderBonds(a);
      case 'obligations': return await renderObligations();
      default: return await renderHome();
    }
  } catch (e) {
    if (e instanceof ApiError && e.status === 401 && !/insufficient_user_authentication/.test(e.challenge || '')) return signOut('Your session ended. Sign in again.');
    showError(e);
  }
}

// ---------- sign-in ----------
function renderSignin(msg) {
  $('nav').hidden = true;
  $('who').hidden = true;
  const ta = h('textarea', { name: 'token', autocomplete: 'off', spellcheck: 'false', required: true });
  const form = h('form', { class: 'panel signin', on: { submit: async (ev) => {
    ev.preventDefault();
    S.token = ta.value.trim();
    try {
      S.me = await api('GET', '/v1/identity/me');
      S.can = new Set(S.me.permissions);
      S.ref = S.can.has('p2p.read') ? await api('GET', '/v1/p2p-reference') : { legal_entities: [], projects: [], accounts: [] };
      try { S.company = await api('GET', '/v1/tenant/settings'); } catch { S.company = null; }
      brandCompany();
      paintWho(S.me.user_id);
      $('mebtn').title = `Your profile — ${S.me.roles.join(', ') || 'no role'}`;
      api('GET', '/v1/me/profile').then((pr) => { if (pr.display_name) paintWho(pr.display_name); }).catch(() => { /* the id is shown */ });
      $('who-mfa').hidden = !S.me.mfa;
      $('who').hidden = false;
      buildNav();
      startBell();
      if (location.hash === '' || location.hash === '#/') location.hash = '#/home';
      else route();
      say(`Signed in as ${S.me.user_id}`);
    } catch (e) {
      S.token = null;
      showError(e);
    }
  } } },
  h('h1', { tabindex: '-1' }, 'Sign in'),
  h('p', { class: 'lede' }, 'Paste the access token issued by your organisation’s identity provider. It is kept in this tab’s memory only and is forgotten when you sign out or close the tab.'),
  msg ? h('p', { class: 'notice' }, msg) : null,
  field('Access token', ta),
  h('div', { class: 'actions' }, h('button', { type: 'submit' }, 'Sign in')));
  const v = $('view');
  v.textContent = '';
  v.append(form);
  document.title = 'Sign in — SERK Construction';
  ta.focus();
  stagingSignin(v, ta);
}

// Test environments only: the server offers /staging/* when it runs with
// SERK_ENV=staging and a staging passphrase (refused in production). The
// minted token is an ordinary bearer token; it carries no extra authority.
async function stagingSignin(v, ta) {
  let data;
  try {
    const r = await fetch('/staging/personas');
    if (!r.ok) return;
    data = await r.json();
  } catch { return; }
  const sel = h('select', { name: 'persona', required: true });
  for (const p of data.personas) sel.append(h('option', { value: p.id }, `${p.label} — ${p.roles.join(', ')}`));
  const co = h('select', { name: 'company', required: true });
  for (const c of data.companies || []) co.append(h('option', { value: c.id }, c.name));
  const pw = h('input', { type: 'password', name: 'passphrase', autocomplete: 'off', required: true });
  const panel = h('form', { class: 'panel signin', on: { submit: async (ev) => {
    ev.preventDefault();
    clearError();
    try {
      const r = await fetch('/staging/token', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ passphrase: pw.value, persona: sel.value, company: co.value || undefined }) });
      const d = await r.json().catch(() => ({}));
      if (!r.ok) throw new ApiError(r.status, (d.error || {}).code || 'error', (d.error || {}).message || r.statusText);
      ta.value = d.token;
      ta.form.requestSubmit();
    } catch (e) { showError(e); }
  } } },
  h('h2', {}, 'Test environment sign-in'),
  h('p', { class: 'notice' }, 'STAGING — synthetic data only. Pick a company and a test persona; each person holds one duty, so switch persona to approve what another prepared. Step-up actions need a sign-in made within the last 10 minutes.'),
  (data.companies || []).length > 1 ? field('Company', co) : null,
  field('Persona', sel),
  field('Staging passphrase', pw),
  h('div', { class: 'actions' }, h('button', { type: 'submit' }, 'Sign in as persona')));
  v.prepend(panel);
}
async function signOut(msg) {
  if (S.token) { try { await api('POST', '/v1/identity/logout'); } catch { /* session may already be gone */ } }
  S.token = null; S.me = null; S.ref = null; S.can = new Set(); S.company = null; ROLES = null; rolesLoading = null;
  brandCompany();
  stopBell();
  history.replaceState(null, '', '#/');
  renderSignin(msg || 'You have signed out.');
}

// ---------- home ----------
// Work queues for the signed-in person's permissions (also the bell).
async function workQueues() {
  const qs = QUEUES.filter(([p]) => S.can.has(p));
  const jobs = qs.map(([, type, status]) => api('GET', `/v1/p2p/${type}?status=${status}`).then((r) => r.items.length).catch(() => null));
  if (S.can.has('journal.approve')) jobs.push(Promise.all(['P2P_', 'AR_', 'STK_'].map((p) => api('GET', `/v1/journals?status=DRAFT&source_prefix=${p}`)))
    .then((rs) => rs.reduce((n, r) => n + r.journals.length, 0)).catch(() => null));
  if (S.can.has('journal.post')) jobs.push(api('GET', '/v1/journals?status=APPROVED').then((r) => r.journals.length).catch(() => null));
  const counts = await Promise.all(jobs);
  const all = qs.map(([, type, status, label], i) => ({ label, href: `#/list/${type}/${status}`, n: counts[i] }));
  let k = qs.length;
  if (S.can.has('journal.approve')) all.push({ label: 'Subledger journals to approve', href: '#/journals', n: counts[k++] });
  if (S.can.has('journal.post')) all.push({ label: 'Approved journals to post', href: '#/journals', n: counts[k++] });
  return all;
}
function greeting() {
  const hr = new Date().getHours();
  return hr < 12 ? 'Good morning' : hr < 17 ? 'Good afternoon' : 'Good evening';
}
async function renderHome() {
  const cards = h('div', { class: 'cards' });
  const hero = h('section', { class: 'hero', 'aria-label': 'Summary' });
  page('My work', null, hero, h('h2', { class: 'section' }, 'Waiting for you'), cards,
    h('p', { class: 'foot-note' }, 'Counts include only the projects you can see. Every approval remains a human decision, checked again by the database.'));
  const all = await workQueues();
  const total = all.reduce((n, q) => n + (q.n || 0), 0);
  hero.append(h('div', {}, h('p', { class: 'eyebrow' }, new Date().toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' })),
    h('h2', { class: 'hello' }, `${greeting()}, ${S.me.user_id}`), h('p', {}, `Signed in as ${S.me.roles.join(', ').replace(/_/g, ' ')}.`)),
  h('div', { class: 'big' }, String(total), h('small', {}, total === 1 ? 'item waiting' : 'items waiting')));
  if (!all.length) cards.append(h('p', { class: 'empty' }, 'No work queues for your roles.'));
  for (const q of all.sort((a, b) => (b.n || 0) - (a.n || 0))) {
    cards.append(h('a', { class: q.n ? 'card hot' : 'card', href: q.href }, h('div', { class: 'n' }, q.n == null ? '–' : String(q.n)), h('div', { class: 't' }, q.label)));
  }
  setBell(all);
}

// ---------- lists ----------
function table(caption, cols, rows, empty) {
  const t = h('table', {}, h('caption', {}, caption),
    h('thead', {}, h('tr', {}, cols.map((c) => h('th', { scope: 'col', class: c.num ? 'num' : null }, c.label)))),
    h('tbody', {}, rows.length ? rows.map((r) => h('tr', { class: r._class || null }, cols.map((c) => h('td', { class: c.num ? 'num' : null }, c.cell(r))))) :
      h('tr', {}, h('td', { colspan: cols.length, class: 'empty' }, empty || 'Nothing here.'))));
  return h('div', { class: 'tablewrap' }, t);
}

async function renderList(type, status) {
  if (type === 'bank_statement') return renderBank();
  const meta = DOCS[type];
  if (!meta) return renderHome();
  const r = await api('GET', `/v1/p2p/${type}${status ? '?status=' + encodeURIComponent(status) : ''}`);
  const filter = select('status', [{ value: '', label: 'All statuses' }, ...meta.statuses.map((s) => ({ value: s, label: words(s.toLowerCase()) }))]);
  filter.value = status || '';
  filter.addEventListener('change', () => { location.hash = `#/list/${type}${filter.value ? '/' + filter.value : ''}`; });
  const listTitle = `${meta.title}${status ? ' — ' + words(status.toLowerCase()) : ''}`;
  const tools = h('div', { class: 'toolbar' }, field('Status', filter),
    meta.form && S.can.has(meta.create) ? h('a', { class: 'btn', href: `#/new/${meta.form}` }, `New ${meta.one.toLowerCase()}`) : null,
    h('span', { class: 'spacer' }), exportBar(`/v1/p2p/${type}${status ? '?status=' + encodeURIComponent(status) : ''}`, listTitle));
  page(meta.title, null, tools, table(`${meta.title}${status ? ' — ' + words(status.toLowerCase()) : ''} (${r.items.length})`, [
    { label: 'Reference', cell: (d) => {
      const href = meta.link ? meta.link(d) : `#/doc/${type}/${d.id}`;
      return href ? h('a', { href, class: 'ref' }, d.number || d.id.slice(0, 8)) : h('span', { class: 'ref' }, d.number || d.id.slice(0, 8));
    } },
    { label: 'Description', cell: (d) => d.label },
    { label: 'Status', cell: (d) => chip(d.status) },
    { label: 'Amount', num: true, cell: (d) => (d.amount && d.amount !== '0' ? money(d.amount) : '') },
    { label: 'Created by', cell: (d) => d.created_by },
    { label: 'Created', cell: (d) => when(d.created_at) },
  ], r.items, 'No documents match.'));
}

// ---------- document view ----------
// Internal or derived fields that mean nothing to a reader.
const HIDE = new Set(['tenant_id', 'id', 'normalized_number', 'bank_snapshot', 'command_hash', 'source_sha256', 'legal_entity_id', 'match_detail']);
// References to other documents: shown as links, not raw ids.
const REF = { pr_id: 'purchase_requisition', rfq_id: 'rfq', po_id: 'purchase_order', quote_id: 'rfq_quote', awarded_quote_id: 'rfq_quote',
  supplier_id: 'supplier', supplier_bank_id: 'supplier_bank_account', company_bank_id: 'company_bank_account', policy_id: 'p2p_policy',
  customer_id: 'customer', contract_id: 'contract', ipc_id: 'ipc', stock_item_id: 'stock_item', warehouse_id: 'warehouse',
  from_warehouse_id: 'warehouse', to_warehouse_id: 'warehouse' };
// Fields whose target depends on the document carrying them.
const REF_BY_TYPE = { policy_id: { contract: 'contract_policy', ipc: 'contract_policy', customer_invoice: 'contract_policy', customer_credit_note: 'contract_policy', retention_release: 'contract_policy' },
  invoice_id: { customer_credit_note: 'customer_invoice' } };
const refOf = (k, type) => (REF_BY_TYPE[k] && REF_BY_TYPE[k][type]) || REF[k];
// Reading order: what the document is, its state, money, dates, people.
const ORDER = ['number', 'invoice_number', 'statement_no', 'code', 'name', 'description', 'justification', 'status', 'technical_status', 'match_status',
  'currency', 'subtotal', 'tax_amount', 'total', 'amount', 'invoice_date', 'due_date', 'received_on', 'value_date', 'needed_by', 'valid_until', 'due_at'];
const MONEY = /amount|total|price|balance|subtotal|threshold|rate$|gross$|_value$|recovery$|_held$|received$|recovered$|_open$|_released$|_cost$|value_change$|_approved$|^budget$/;
const STAMP = /_at$/;
const rank = (k) => { const i = ORDER.indexOf(k); return i < 0 ? ORDER.length + (/_by$|_at$/.test(k) ? 1 : 0) : i; };
const ordered = (keys) => [...keys].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
function fmtValue(k, v) {
  if (typeof v === 'boolean') return v ? 'Yes' : 'No';
  if (STAMP.test(k)) return when(v);
  if (MONEY.test(k) && !/_pct$|_float$/.test(k)) return money(v);
  return String(v);
}
function kv(obj, type) {
  const dl = h('dl', { class: 'kv' });
  for (const k of ordered(Object.keys(obj || {}))) {
    const v = obj[k];
    if (HIDE.has(k) || v == null || typeof v === 'object') continue;
    const ref = refOf(k, type);
    if (ref) {
      if (DOCS[ref]) dl.append(h('dt', {}, words(k.replace(/_id$/, ''))), h('dd', {}, h('a', { href: `#/doc/${ref}/${v}` }, `Open ${DOCS[ref].one.toLowerCase()}`)));
      continue;
    }
    if (/_id$/.test(k)) continue;
    dl.append(h('dt', {}, words(k)), h('dd', {}, fmtValue(k, v)));
  }
  return dl;
}
const LINE_ORDER = ['line_no', 'po_line_no', 'pr_line_no', 'po_line', 'item_no', 'code', 'activity_code', 'name', 'pred_code', 'succ_code', 'link_type', 'lag',
  'duration', 'early_start', 'early_finish', 'late_start', 'late_finish', 'total_float', 'free_float', 'critical', 'budget', 'percent_complete',
  'actual_start', 'actual_finish', 'description', 'unit', 'quantity', 'qty_received', 'received_qty',
  'billed_qty_cumulative', 'est_unit_price', 'unit_price', 'po_price', 'invoice_price', 'rate', 'price_variance_pct', 'within_tolerance', 'est_amount', 'amount', 'note'];
function rowsTable(caption, rows) {
  if (!Array.isArray(rows) || !rows.length) return null;
  const lr = (k) => { const i = LINE_ORDER.indexOf(k); return i < 0 ? LINE_ORDER.length : i; };
  const offsets = new Set(['es', 'ef', 'ls', 'lf']);
  const keys = Object.keys(rows[0]).filter((k) => !HIDE.has(k) && !offsets.has(k) && !/_id$/.test(k) && typeof rows[0][k] !== 'object').sort((a, b) => lr(a) - lr(b));
  return table(caption, keys.map((k) => ({ label: words(k), num: /quantity|qty|price|amount|rate|total|pct|line|budget|duration|float|percent|lag|hours|headcount|version/.test(k),
    cell: (r) => (r[k] == null ? '' : /_pct$/.test(k) ? `${(Number(r[k]) * 100).toFixed(2)}%` : fmtValue(k, r[k])) })), rows);
}

async function renderDoc(type, id) {
  const meta = DOCS[type];
  if (!meta) return renderHome();
  const d = await api('GET', `/v1/p2p/${type}/${id}`);
  const det = d.detail || {};
  const head = det.h || det.q || det;
  const parts = [h('div', { class: 'panel' }, h('p', {}, chip(d.status), ' ', `created by ${d.created_by}`), kv(head, type))];
  const md = head.match_detail;
  if (md && Array.isArray(md.lines)) {
    parts.push(h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Three-way match'),
      h('p', {}, chip(md.status || head.match_status), ` price tolerance ${(Number(md.price_tolerance_pct) * 100).toFixed(2)}% · VAT ${(Number(md.vat_rate) * 100).toFixed(2)}% · policy version ${md.policy_version}`),
      rowsTable('Invoice against purchase order and confirmed receipts', md.lines)));
  }
  if (type === 'forecast_cycle') parts.push(await forecastPanel(d)); else parts.push(rowsTable('Lines', det.lines));
  parts.push(rowsTable('Items', det.items), rowsTable('Allocations', det.alloc),
    rowsTable('Activities (critical path computed; the database re-verifies it)', det.activities), rowsTable('Dependencies', det.links),
    rowsTable('Labour (this version)', det.labour), rowsTable('Equipment (this version)', det.equipment));
  if (Array.isArray(det.quotes)) parts.push(rowsTable('Quotes received', det.quotes.map((x) => x.q)));
  if (det.payee) parts.push(h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Payee (approved bank account)'), kv(det.payee)));
  if (type === 'contract' && d.status === 'ACTIVE') parts.push(await positionPanel(d.id), await timePanel(d.id));
  if (type === 'change_event' && d.status === 'APPROVED') {
    const ce = await api('GET', `/v1/change-events/${d.id}`);
    parts.push(h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Effect on the budget'),
      h('p', {}, `Cost impact ${money(ce.total_cost_impact)} · revenue impact ${money(ce.revenue_impact)} · margin impact ${money(ce.margin_impact)} · schedule ${ce.schedule_impact_days} day(s).`),
      ce.budget_version_id ? h('p', {}, 'A budget revision for the cost lines was drafted. The budget moves only when it is submitted and approved: ', h('a', { href: `#/doc/budget_version/${ce.budget_version_id}` }, 'open the drafted revision'), '.')
        : h('p', { class: 'hint' }, 'This change event has no cost lines, so no budget revision was drafted.')));
  }
  if (type === 'contract_notice' && d.status === 'OPEN' && S.can.has('claim.manage')) {
    const on = input('sent_on', { type: 'date', required: true, value: new Date().toISOString().slice(0, 10) });
    const btn = h('button', { type: 'button', class: 'primary' }, 'Record as sent');
    btn.addEventListener('click', async () => {
      clearError();
      try { await api('POST', `/v1/notices/${d.id}/send`, { sent_on: on.value }); say('Notice recorded as sent.'); route(); } catch (e) { showError(e); }
    });
    parts.push(h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Record sending'), h('p', { class: 'hint' }, 'Enter the day the notice was actually sent. It cannot be in the future or before the event, and it is recorded once.'), h('div', { class: 'actions' }, field('Date sent', on), btn)));
  }
  if (type === 'contract_claim' && d.status === 'APPROVED' && !head.client_outcome && S.can.has('claim.manage')) {
    const out = select('outcome', [['AGREED', 'Agreed'], ['REJECTED', 'Rejected'], ['DISPUTED', 'Disputed']].map(([value, label]) => ({ value, label })));
    const ref = input('client_reference', { type: 'text', required: true, maxlength: '100', placeholder: 'The client\'s letter or reference' });
    const amt = input('agreed_amount', { type: 'number', step: '0.01', min: '0', value: '0' });
    const dd = input('agreed_days', { type: 'number', step: '1', min: '0', value: '0' });
    const on = input('outcome_on', { type: 'date', required: true, value: new Date().toISOString().slice(0, 10) });
    const btn = h('button', { type: 'button', class: 'primary' }, 'Record the answer');
    btn.addEventListener('click', async () => {
      clearError();
      try {
        await api('POST', `/v1/claims/${d.id}/outcome`, { outcome: out.value, client_reference: ref.value.trim(), agreed_amount: out.value === 'AGREED' ? amt.value || '0' : '0', agreed_days: out.value === 'AGREED' ? Number(dd.value || 0) : 0, outcome_on: on.value });
        say('The client\'s answer was recorded.'); route();
      } catch (e) { showError(e); }
    });
    parts.push(h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'The client\'s answer'),
      h('p', { class: 'hint' }, 'Record what the client decided, once. Agreed money and days cannot exceed what was claimed.'),
      h('div', { class: 'grid' }, field('Outcome', out), field('Client reference', ref), field('Agreed amount', amt), field('Agreed days', dd), field('Date of answer', on)), h('div', { class: 'actions' }, btn)));
  }
  parts.push(actionsPanel(type, d, det), nextSteps(type, d), revealPanel(type, d));
  const docTitle = `${meta.one} ${d.number || head.code || (head.version ? 'v' + head.version : '')}`.trim();
  parts.unshift(h('div', { class: 'toolbar' }, h('span', { class: 'spacer' }), exportBar(`/v1/p2p/${type}/${id}`, docTitle, { entity: () => head.legal_entity_id || '' })));
  page(docTitle, null, ...parts);
}

// Full account numbers are masked everywhere; a holder of bank.details.reveal
// may display one with a stated purpose and a fresh MFA sign-in. The reveal is
// audited on the server; the number is shown once and never stored here.
function revealPanel(type, d) {
  if (!['supplier_bank_account', 'company_bank_account'].includes(type) || !S.can.has('bank.details.reveal')) return null;
  const purpose = input('purpose', { type: 'text', maxlength: '300', required: true });
  const out = h('output', { class: 'reveal', 'aria-live': 'polite' });
  const btn = h('button', { type: 'button', class: 'ghost' }, 'Reveal full account number');
  btn.addEventListener('click', async () => {
    clearError();
    out.textContent = '';
    if (purpose.value.trim().length < 20) { showError(new Error('State the purpose of the reveal (at least 20 characters).')); purpose.focus(); return; }
    btn.disabled = true;
    try {
      const r = await api('POST', `/v1/p2p/${type}/${d.id}/reveal-bank-details`, { purpose: purpose.value.trim() });
      out.textContent = `${r.account_number} (${r.account_name || ''}${r.bank_name ? ', ' + r.bank_name : ''}). This reveal was recorded in the audit log.`;
      purpose.value = '';
    } catch (e) { showError(e); } finally { btn.disabled = false; }
  });
  return h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Bank details'),
    h('p', { class: 'hint' }, 'Account numbers are masked. Revealing one requires a recent multi-factor sign-in and is audited.'),
    h('div', { class: 'actions' }, field('Purpose of the reveal', purpose), btn), out);
}

// The forecast page: each cost code's budget, cost to date and commitments (frozen
// by the database), the method that sets the estimate to complete, and the
// resulting estimate at completion (EAC) and variance (VAC = budget - EAC).
async function forecastPanel(d) {
  const c = await api('GET', `/v1/forecasts/${d.id}`);
  const edit = d.status === 'DRAFT' && c.created_by === S.me.user_id && S.can.has('forecast.prepare');
  const METHODS = [['REMAINING_BUDGET', 'Remaining budget'], ['OPEN_COMMITMENTS', 'Open commitments only'], ['MANUAL', 'Manual estimate']];
  const cols = [
    { label: 'Cost code', cell: (l) => l.cost_code },
    { label: 'Current budget', num: true, cell: (l) => money(l.budget) },
    { label: 'Committed', num: true, cell: (l) => (l.total ? '' : money(l.committed)) },
    { label: 'Cost to date', num: true, cell: (l) => money(l.cost_to_date) },
    { label: 'Method', cell: (l) => (l.total ? '' : edit ? methodCell(l) : words(l.method.toLowerCase())) },
    { label: 'Estimate to complete', num: true, cell: (l) => money(l.etc) },
    { label: 'Estimate at completion', num: true, cell: (l) => money(l.eac) },
    { label: 'Variance (budget - EAC)', num: true, cell: (l) => money(l.vac) },
    { label: 'Why / note', cell: (l) => (l.total ? '' : edit ? noteCell(l) : l.note || '') },
  ];
  const rows = !(c.lines || []).length ? [] : [...c.lines, { cost_code: 'Total', budget: c.total_budget, committed: '', cost_to_date: c.total_cost_to_date, method: '', etc: c.total_etc, eac: c.total_eac, vac: c.total_vac, note: '', total: true }];
  const cells = new Map();
  const save = async (l, method, manual, note) => {
    clearError();
    try {
      await api('PUT', `/v1/forecasts/${d.id}/lines/${l.cost_code_id}`, { method, manual_etc: method === 'MANUAL' ? manual : null, note });
      say(`${l.cost_code} updated.`);
      route();
    } catch (e) { showError(e); }
  };
  function methodCell(l) {
    const sel = select('method', METHODS.map(([value, label]) => ({ value, label })), { 'aria-label': `Method for ${l.cost_code}` });
    sel.value = l.method;
    const man = input('manual_etc', { type: 'number', step: '0.01', min: '0', value: l.manual_etc || '', 'aria-label': `Manual estimate to complete for ${l.cost_code}`, size: '10' });
    man.hidden = l.method !== 'MANUAL';
    sel.addEventListener('change', () => { man.hidden = sel.value !== 'MANUAL'; if (sel.value !== 'MANUAL') save(l, sel.value, null, (cells.get(l.cost_code_id) || {}).value || l.note || ''); else man.focus(); });
    man.addEventListener('change', () => save(l, 'MANUAL', man.value, (cells.get(l.cost_code_id) || {}).value || l.note || ''));
    return h('span', {}, sel, ' ', man);
  }
  function noteCell(l) {
    const n = input('note', { type: 'text', value: l.note || '', maxlength: '500', 'aria-label': `Note for ${l.cost_code}`, placeholder: Number(l.eac) > Number(l.budget) ? 'Required: why over budget' : '' });
    cells.set(l.cost_code_id, n);
    n.addEventListener('change', () => save(l, l.method, l.manual_etc, n.value.trim()));
    return n;
  }
  const over = (c.lines || []).filter((l) => Number(l.eac) > Number(l.budget)).length;
  return h('div', { class: 'panel' }, h('h2', { class: 'section' }, `Forecast at completion as at ${c.period_end}`),
    h('p', { class: 'hint' }, edit
      ? `Change a line's method or add a note; each change saves at once and the figures are recomputed by the database. ${over ? over + ' line(s) are forecast above budget and need an explanation before you can submit.' : ''}`
      : 'Figures are frozen by the database from the cost position when the line was last saved. A negative variance is a forecast overrun.'),
    table('Estimate at completion by cost code', cols, rows, 'No cost codes have money on them yet.'));
}

async function positionPanel(id) {
  const p = await api('GET', `/v1/contracts/${id}/position`);
  return h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Commercial position'), kv(p));
}

function nextSteps(type, d) {
  const links = [];
  const add = (perm, href, label) => { if (S.can.has(perm)) links.push(h('a', { class: 'btn', href }, label)); };
  if (type === 'purchase_requisition' && d.status === 'APPROVED') add('rfq.manage', `#/new/rfq/${d.id}`, 'Create RFQ');
  if (type === 'rfq' && d.status === 'ISSUED') add('rfq.manage', `#/new/quote/${d.id}`, 'Record a quote');
  if (type === 'rfq' && d.status === 'AWARDED') add('po.manage', `#/new/po/${d.id}`, 'Create purchase order');
  if (type === 'purchase_order' && d.status === 'APPROVED') {
    add('grn.create', `#/new/grn/${d.id}`, 'Record goods receipt'); add('ap.enter', `#/new/invoice/${d.id}`, 'Enter supplier invoice');
    const assets = new Set(S.ref.accounts.filter((x) => x.kind === 'ASSET').map((x) => x.id));
    if (((d.detail || {}).lines || []).some((l) => !l.stock_item_id && assets.has(l.account_id))) add('po.approve', `#/new/designate/${d.id}`, 'Designate stock items');
  }
  if (type === 'supplier_invoice' && d.status === 'APPROVED') add('payment.prepare', `#/new/payment/${d.id}`, 'Prepare payment');
  if (type === 'customer_invoice' && d.status === 'APPROVED') add('ar.credit.create', `#/new/creditnote/${d.id}`, 'Issue credit note');
  if (type === 'supplier' && d.status === 'ACTIVE') add('supplier.manage', `#/new/supplierbank/${d.id}`, 'Add bank account');
  if (type === 'grn' && d.status === 'CONFIRMED') add('stock.receive', `#/new/putaway/${d.id}`, 'Put away into a warehouse');
  if (type === 'contract' && d.status === 'ACTIVE') { add('measure.create', `#/new/measurement/${d.id}`, 'Record measurement'); add('ipc.prepare', `#/new/ipc/${d.id}`, 'Prepare payment certificate');
    add('ar.retention.create', `#/new/retention/${d.id}`, 'Release retention'); add('contract.manage', `#/new/variation/${d.id}`, 'Record variation'); }
  return links.length ? h('div', { class: 'actions' }, links) : null;
}

function actionsPanel(type, d, det) {
  const meta = DOCS[type];
  const seen = new Set();
  const acts = (meta.actions[d.status] || []).filter(([a, perm]) => allowed(perm) && !seen.has(a) && seen.add(a));
  if (!acts.length) return null;
  const box = h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Actions'));
  for (const [action, , mode] of acts) {
    const extra = [];
    let collect = () => ({});
    if (mode === 'opt' || mode === 'req') {
      const r = input('reason', { type: 'text', maxlength: '500', required: mode === 'req' });
      extra.push(field(mode === 'req' ? `Reason for ${action}` : 'Comment (optional)', r));
      collect = () => (r.value.trim() ? { reason: r.value.trim() } : mode === 'req' ? null : {});
    } else if (mode === 'award') {
      const comp = (det.quotes || []).map((x) => x.q).filter((q) => q.technical_status === 'COMPLIANT');
      const sel = select('quote_id', comp.map((q) => ({ value: q.id, label: `${q.supplier_reference || q.id.slice(0, 8)} — ${money(q.total)}` })));
      const r = input('reason', { type: 'text', maxlength: '500' });
      extra.push(field('Winning quote (technically compliant only)', sel), field('Award reason (required if not the lowest)', r));
      collect = () => (sel.value ? { quote_id: sel.value, reason: r.value.trim() } : null);
    } else if (mode === 'evaluate') {
      const yes = h('input', { type: 'radio', name: 'compliant', value: 'yes', id: nextId('r') });
      const no = h('input', { type: 'radio', name: 'compliant', value: 'no', id: nextId('r') });
      const note = input('note', { type: 'text', maxlength: '500' });
      extra.push(h('fieldset', {}, h('legend', {}, 'Technical evaluation'),
        h('div', { class: 'check' }, yes, h('label', { for: yes.id }, 'Compliant')), h('div', { class: 'check' }, no, h('label', { for: no.id }, 'Not compliant'))),
      field('Evaluation note', note));
      collect = () => (yes.checked || no.checked ? { compliant: yes.checked, note: note.value.trim() } : null);
    } else if (mode === 'certify') {
      const ref = input('certificate_ref', { type: 'text', maxlength: '100', required: true });
      const file = h('input', { type: 'file', name: 'certificate_file', accept: '.pdf,image/*' });
      const sum = h('output', { class: 'hint', 'aria-live': 'polite' });
      let sha = '';
      file.addEventListener('change', async () => {
        sha = '';
        sum.textContent = '';
        if (!file.files.length) return;
        const buf = await crypto.subtle.digest('SHA-256', await file.files[0].arrayBuffer());
        sha = [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, '0')).join('');
        sum.textContent = `SHA-256 ${sha}`;
      });
      extra.push(field('Client certificate reference', ref), field('Signed certificate file', file, 'Its SHA-256 is recorded with the certification; the file stays with you.'), sum);
      collect = () => (ref.value.trim() && sha ? { certificate_ref: ref.value.trim(), certificate_sha256: sha } : null);
    } else if (mode === 'invoice') {
      const idate = input('invoice_date', { type: 'date', value: today(), required: true });
      const due = input('due_date', { type: 'date', value: plusDays(30), required: true });
      extra.push(field('Invoice date', idate), field('Due date', due));
      collect = () => (idate.value && due.value ? { invoice_date: idate.value, due_date: due.value } : null);
    }
    const btn = h('button', { type: 'button', class: action === 'reject' || action === 'cancel' || action === 'block' ? 'danger' : null }, ACTION_LABEL[action] || action);
    btn.addEventListener('click', async () => {
      clearError();
      const body = collect();
      if (body === null) { showError(new Error('Complete the required fields for this action.')); return; }
      btn.disabled = true;
      try {
        const res = await api('POST', `${meta.base}/${d.id}/${action}`, Object.keys(body).length ? body : undefined);
        if (action === 'invoice' && res && res.id) return created('customer_invoice', res, 'Customer invoice');
        let msg = `${ACTION_LABEL[action] || action}: done.`;
        if (res && res.journal_id) msg += ' A draft journal was created for finance to approve and post.';
        if (res && res.match_status) msg += ` Three-way match: ${res.match_status}.`;
        if (res && res.status) msg += ` Status: ${res.status}.`;
        await renderDoc(type, d.id);
        $('view').insertBefore(h('p', { class: 'notice', role: 'status' }, msg), $('view').children[1]);
        say(msg);
      } catch (e) { btn.disabled = false; showError(e); }
    });
    box.append(h('div', { class: 'actions' }, extra, btn));
  }
  return box;
}

// ---------- forms ----------
const opt = (items, label) => items.map((x) => ({ value: x.id, label: label(x) }));
const entityOpts = () => opt(S.ref.legal_entities, (e) => e.name);
const projectOpts = () => opt(S.ref.projects, (p) => `${p.code} — ${p.name}`);
const accountOpts = (types) => opt(S.ref.accounts.filter((a) => !types || types.includes(a.kind)), (a) => `${a.code} ${a.name}`);
async function docOpts(type, status) {
  const r = await api('GET', `/v1/p2p/${type}?status=${status}`);
  return r.items.map((d) => ({ value: d.id, label: `${d.number || ''} ${d.label}${d.amount && d.amount !== '0' ? ' — ' + money(d.amount) : ''}`.trim() }));
}
const today = () => new Date().toISOString().slice(0, 10);
const plusDays = (n) => new Date(Date.now() + n * 864e5).toISOString().slice(0, 10);

function formPage(title, lede, fields, submitLabel, onSubmit) {
  const btn = h('button', { type: 'submit' }, submitLabel);
  const form = h('form', { class: 'panel', novalidate: true }, fields, h('div', { class: 'actions' }, btn));
  form.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    clearError();
    for (const el of form.querySelectorAll('[required]')) el.setAttribute('aria-invalid', String(!el.value.trim()));
    const bad = form.querySelector('[aria-invalid=true]');
    if (bad) { showError(new Error('Fill in the highlighted fields.')); bad.focus(); return; }
    btn.disabled = true;
    try { await onSubmit(); } catch (e) { showError(e); } finally { btn.disabled = false; }
  });
  page(title, lede, form);
  return form;
}
async function created(type, res, what) {
  say(`${what} created.`);
  location.hash = `#/doc/${type}/${res.id}`;
}

// Editable line grid: cols = [{key,label,make()}].
function lineGrid(legend, cols, initial, allowAdd) {
  const tbody = h('tbody');
  const addRow = (vals = {}) => {
    const tr = h('tr');
    const n = tbody.children.length + 1;
    for (const c of cols) {
      const el = c.make(vals, n);
      el.setAttribute('aria-label', `${c.label}, line ${n}`);
      if (vals[c.key] != null && el.tagName !== 'SPAN') el.value = vals[c.key];
      tr.append(h('td', {}, el));
    }
    if (allowAdd) tr.append(h('td', {}, h('button', { type: 'button', class: 'ghost', 'aria-label': `Remove line ${n}`, on: { click: () => tr.remove() } }, 'Remove')));
    tbody.append(tr);
  };
  (initial.length ? initial : allowAdd ? [{}] : []).forEach(addRow);
  const fs = h('fieldset', {}, h('legend', {}, legend), h('div', { class: 'tablewrap' }, h('table', { class: 'lines' },
    h('thead', {}, h('tr', {}, cols.map((c) => h('th', { scope: 'col' }, c.label)), allowAdd ? h('th', { scope: 'col' }, h('span', { class: 'sr' }, 'Remove')) : null)), tbody)),
  allowAdd ? h('div', { class: 'actions' }, h('button', { type: 'button', class: 'ghost', on: { click: () => addRow() } }, 'Add line')) : null);
  fs.rows = () => [...tbody.children].map((tr) => {
    const o = {};
    cols.forEach((c, i) => { const el = tr.children[i].firstChild; if (el.name) o[c.key] = el.value.trim(); });
    return o;
  });
  return fs;
}
const num = (key, attrs = {}) => () => input(key, { inputmode: 'decimal', autocomplete: 'off', ...attrs });
const txt = (key, attrs = {}) => () => input(key, { type: 'text', ...attrs });
const ro = (key) => (v) => h('span', {}, v[key] == null ? '' : String(v[key]));

async function renderForm(kind, arg) {
  const F = FORMS[kind];
  if (!F) return renderHome();
  return F(arg);
}

const FORMS = {
  async budget_revision() {
    const proj = select('project_id', projectOpts(), { required: true });
    const kind = select('kind', [{ value: 'REVISION', label: 'Revision' }, { value: 'ORIGINAL', label: 'Original' }], { required: true });
    const reason = input('reason', { type: 'text', required: true, maxlength: '500' });
    let grid;
    const holder = h('div');
    const load = async () => {
      const cc = proj.value ? (await api('GET', `/v1/projects/${proj.value}/cost-codes`)).cost_codes : [];
      grid = lineGrid('Budget lines', [
        { key: 'cost_code_id', label: 'Cost code', make: () => select('cost_code_id', opt(cc, (c) => `${c.code} ${c.name}`)) },
        { key: 'amount', label: 'Amount', make: num('amount') },
        { key: 'contingency_flag', label: 'Contingency?', make: () => select('contingency_flag', [{ value: false, label: 'No' }, { value: true, label: 'Yes' }]) },
      ], [], true);
      holder.replaceChildren(grid);
    };
    proj.addEventListener('change', load);
    formPage('New budget revision', 'Prepare the ORIGINAL baseline or a REVISION per cost code. A revision may reduce a code (negative amount). Mark a line as contingency to allow drawdowns from it. A different person approves.',
      [h('div', { class: 'grid' }, field('Project', proj), field('Kind', kind), field('Reason', reason)), holder], 'Create budget revision', async () => {
        const lines = grid.rows().map((r) => ({ cost_code_id: r.cost_code_id, amount: r.amount, contingency_flag: r.contingency_flag === true || r.contingency_flag === 'true' }));
        const res = await api('POST', '/v1/budgets/revisions', { project_id: proj.value, kind: kind.value, reason: reason.value.trim(), lines });
        created('budget_version', res, 'Budget version');
      });
    await load();
  },

  async budget_transfer() {
    const proj = select('project_id', projectOpts(), { required: true });
    const fromCC = select('from_cost_code', [], { required: true });
    const toCC = select('to_cost_code', [], { required: true });
    const amt = input('amount', { type: 'number', step: '0.01', min: '0.01', required: true });
    const reason = input('reason', { type: 'text', required: true, maxlength: '500' });
    const load = async () => {
      const ccs = proj.value ? (await api('GET', `/v1/projects/${proj.value}/cost-codes`)).cost_codes : [];
      const opts = opt(ccs, (c) => `${c.code} ${c.name}`);
      fromCC.replaceChildren(...opts.map((o) => h('option', { value: o.value }, o.label)));
      toCC.replaceChildren(...opts.map((o) => h('option', { value: o.value }, o.label)));
    };
    proj.addEventListener('change', load);
    formPage('Request budget transfer', 'Transfer budget between cost codes within the same project with net-zero impact.',
      [h('div', { class: 'grid' }, field('Project', proj), field('From cost code', fromCC), field('To cost code', toCC), field('Amount', amt), field('Reason', reason))], 'Submit transfer request', async () => {
        const res = await api('POST', '/v1/budgets/transfers', {
          project_id: proj.value,
          from_cost_code: fromCC.value,
          to_cost_code: toCC.value,
          amount: amt.value,
          reason: reason.value.trim()
        });
        created('budget_transfer', res, 'Budget transfer');
      });
    await load();
  },

  async contingency_drawdown() {
    const proj = select('project_id', projectOpts(), { required: true });
    const fromCC = select('from_cost_code', []);
    const toCC = select('to_cost_code', [], { required: true });
    const amt = input('amount', { type: 'number', step: '0.01', min: '0.01', required: true });
    const reason = input('reason', { type: 'text', required: true, maxlength: '500' });
    const load = async () => {
      const ccs = proj.value ? (await api('GET', `/v1/projects/${proj.value}/cost-codes`)).cost_codes : [];
      const opts = opt(ccs, (c) => `${c.code} ${c.name}`);
      toCC.replaceChildren(...opts.map((o) => h('option', { value: o.value }, o.label)));
      // Source: the project's contingency code(s); empty = the only one.
      const pos = proj.value ? ((await api('GET', `/v1/projects/${proj.value}/budget`)).cost_codes || []).filter((r) => r.is_contingency) : [];
      fromCC.replaceChildren(h('option', { value: '' }, pos.length === 1 ? `${pos[0].cost_code} (the project's contingency)` : 'Choose the contingency code'),
        ...pos.map((r) => h('option', { value: r.cost_code_id }, `${r.cost_code} — current ${r.current}`)));
    };
    proj.addEventListener('change', load);
    formPage('Request contingency drawdown', 'Draw down from project contingency into a project cost code.',
      [h('div', { class: 'grid' }, field('Project', proj), field('From contingency code', fromCC, 'Money comes out of this code. It must hold contingency.'),
        field('To cost code', toCC), field('Amount', amt), field('Reason', reason))], 'Create drawdown request', async () => {
        const res = await api('POST', '/v1/budgets/drawdowns', {
          project_id: proj.value,
          from_cost_code: fromCC.value,
          to_cost_code: toCC.value,
          amount: amt.value,
          reason: reason.value.trim()
        });
        created('contingency_drawdown', res, 'Contingency drawdown');
      });
    await load();
  },

  async authority_matrix() {
    await loadRoles();
    const perms = [...new Set(Object.values(ROLES || {}).flat())].sort();
    const reason = input('reason', { type: 'text', required: true, maxlength: '500' });
    const types = [['budget_version', 'Budget version'], ['budget_transfer', 'Budget transfer'], ['contingency_drawdown', 'Contingency drawdown'],
      ['purchase_requisition', 'Requisition'], ['purchase_order', 'Purchase order'], ['supplier_invoice', 'Supplier invoice'], ['payment_instruction', 'Payment']];
    const grid = lineGrid('Approval bands (from the amount shown, up to but not including the next band)', [
      { key: 'doc_type', label: 'Document', make: () => select('doc_type', types.map(([value, label]) => ({ value, label }))) },
      { key: 'min_amount', label: 'From amount', make: num('min_amount', { value: '0' }) },
      { key: 'max_amount', label: 'Below (empty = no limit)', make: num('max_amount') },
      { key: 'currency', label: 'Currency', make: txt('currency', { size: '4', value: 'ETB', maxlength: '3' }) },
      { key: 'required_perm', label: 'Permission needed', make: () => select('required_perm', perms.map((value) => ({ value, label: value })) ) },
      { key: 'requires_mfa', label: 'Fresh MFA?', make: () => select('requires_mfa', [{ value: false, label: 'No' }, { value: true, label: 'Yes' }]) },
    ], [], true);
    formPage('New authority matrix', 'Prepare who may approve how much. It takes effect only when a different person (the CFO, with a fresh multi-factor sign-in) approves it, and it then replaces the matrix in force.',
      [h('div', { class: 'grid' }, field('Reason for this version', reason)), grid], 'Create matrix', async () => {
        const rules = grid.rows().map((r) => ({ doc_type: r.doc_type, min_amount: r.min_amount || '0', max_amount: r.max_amount ? r.max_amount : undefined,
          currency: (r.currency || 'ETB').toUpperCase(), required_perm: r.required_perm, requires_mfa: r.requires_mfa === true || r.requires_mfa === 'true' }));
        const res = await api('POST', '/v1/authority/matrices', { reason: reason.value.trim(), rules });
        created('authority_matrix', res, 'Authority matrix');
      });
  },

  async delegation() {
    const nonDelegable = new Set(['scope.tenant_wide', 'identity.revoke_sessions', 'delegation.approve', 'authority.manage', 'authority.approve', 'budget.override', 'tenant.settings.manage']);
    const mine = (S.me.permissions || []).filter((p) => !nonDelegable.has(p) && !p.startsWith('serk.') && !p.startsWith('principal.') && !/\.(read)$/.test(p));
    const to = input('delegate', { type: 'text', required: true, maxlength: '200', autocomplete: 'off' });
    const perms = select('perms', mine.map((value) => ({ value, label: value })), { multiple: true, size: '8', required: true });
    const day = (d) => new Date(Date.now() + d * 86400000).toISOString().slice(0, 16);
    const from = input('valid_from', { type: 'datetime-local', required: true, value: day(0) });
    const until = input('valid_to', { type: 'datetime-local', required: true, value: day(7) });
    const reason = input('reason', { type: 'text', required: true, maxlength: '500', placeholder: 'For example: annual leave cover' });
    formPage('Delegate my authority', 'Lend part of your own approval authority to a colleague for a limited time (at most 90 days). A third person approves it; you can revoke it at any time. Your colleague can never approve what you prepared, and permissions that would give them a conflict of duties are not applied.',
      [h('div', { class: 'grid' }, field('Colleague (their sign-in name)', to), field('Permissions to lend', perms, 'Hold Ctrl or Cmd to choose several. Only permissions you hold yourself are listed.'),
        field('From', from), field('Until', until), field('Reason', reason))], 'Create delegation', async () => {
        const chosen = [...perms.selectedOptions].map((o) => o.value);
        const res = await api('POST', '/v1/delegations', { delegate: to.value.trim(), perms: chosen, valid_from: new Date(from.value).toISOString(),
          valid_to: new Date(until.value).toISOString(), reason: reason.value.trim() });
        created('delegation', res, 'Delegation');
      });
  },

  async sod_exception() {
    await loadRoles();
    const user = input('user_id', { type: 'text', required: true, maxlength: '200', autocomplete: 'off' });
    const all = [...new Set(Object.values(ROLES || {}).flat())].sort().map((value) => ({ value, label: value }));
    const a = select('perm_a', all, { required: true });
    const b = select('perm_b', all, { required: true });
    const control = input('mitigating_control', { type: 'text', required: true, minlength: '20', maxlength: '1000', placeholder: 'The written control that makes this acceptable (20+ characters)' });
    const until = input('expires_at', { type: 'date', required: true, value: new Date(Date.now() + 90 * 86400000).toISOString().slice(0, 10) });
    formPage('Duty-conflict exception', 'Allow one named person to hold two permissions that are normally kept apart, for a limited time, under a written control. A different person approves it, with a fresh multi-factor sign-in. It does not waive the rule that nobody approves what they prepared.',
      [h('div', { class: 'grid' }, field('Person (their sign-in name)', user), field('First permission', a), field('Second permission', b),
        field('Mitigating control', control), field('Expires', until))], 'Create exception', async () => {
        const res = await api('POST', '/v1/sod/exceptions', { user_id: user.value.trim(), perm_a: a.value, perm_b: b.value,
          mitigating_control: control.value.trim(), expires_at: new Date(until.value + 'T23:59:59Z').toISOString() });
        created('sod_exception', res, 'Duty-conflict exception');
      });
  },

  async uom_conversion() {
    const item = select('stock_item_id', [{ value: '', label: 'All items whose own unit is the unit below' }, ...(await docOpts('stock_item', 'ACTIVE'))]);
    const from = input('from_unit', { type: 'text', required: true, maxlength: '20', placeholder: 'for example: bundle, bag, truck' });
    const to = input('to_unit', { type: 'text', required: true, maxlength: '20', placeholder: 'the item\'s own unit, for example: t, m3, piece' });
    const factor = input('factor', { inputmode: 'decimal', required: true, placeholder: '1 of the first unit = this many of the second' });
    const note = input('note', { type: 'text', maxlength: '300' });
    formPage('New unit conversion', 'Stock is always kept in the item\'s own unit. A conversion lets people count in another unit (a bag, a bundle, a truck). It is exact, applies only after a different person approves it, and there is one approved factor per pair of units.',
      [h('div', { class: 'grid' }, field('Item', item), field('From unit', from), field('To unit', to), field('Factor', factor), field('Note', note))], 'Create conversion', async () => {
        const res = await api('POST', '/v1/stock/uom-conversions', { stock_item_id: item.value || undefined, from_unit: from.value.trim(), to_unit: to.value.trim(), factor: factor.value.trim(), note: note.value.trim() });
        created('uom_conversion', res, 'Unit conversion');
      });
  },

  async reservation() {
    const wh = select('warehouse_id', await docOpts('warehouse', 'ACTIVE'), { required: true });
    const item = select('stock_item_id', await docOpts('stock_item', 'ACTIVE'), { required: true });
    const proj = select('project_id', projectOpts(), { required: true });
    const qty = input('quantity', { inputmode: 'decimal', required: true });
    const unit = input('unit', { type: 'text', maxlength: '20', placeholder: 'Leave empty for the item\'s own unit' });
    const why = input('purpose', { type: 'text', required: true, maxlength: '300', placeholder: 'For example: level 3 slab pour' });
    const until = input('expires_at', { type: 'date', required: true, value: plusDays(14) });
    formPage('Reserve stock', 'Sets material aside for a project so nobody else can issue it. You cannot reserve more than is on hand and not already reserved. A reservation lapses on its end date, and you can release it at any time.',
      [h('div', { class: 'grid' }, field('Store', wh), field('Stock item', item), field('Project', proj), field('Quantity', qty), field('Unit (optional)', unit), field('Purpose', why), field('Keep until', until))], 'Reserve', async () => {
        await api('POST', '/v1/stock/reservations', { warehouse_id: wh.value, stock_item_id: item.value, project_id: proj.value, quantity: qty.value.trim(), unit: unit.value.trim() || undefined,
          purpose: why.value.trim(), expires_at: new Date(until.value + 'T23:59:59Z').toISOString() });
        say('Stock reserved.');
        location.hash = '#/reservations';
      });
  },

  async reorderrule() {
    const wh = select('warehouse_id', await docOpts('warehouse', 'ACTIVE'), { required: true });
    const item = select('stock_item_id', await docOpts('stock_item', 'ACTIVE'), { required: true });
    const min = input('min_qty', { inputmode: 'decimal', required: true });
    const max = input('max_qty', { inputmode: 'decimal', required: true });
    const re = input('reorder_qty', { inputmode: 'decimal', required: true });
    formPage('Reorder rule', 'When the available stock (on hand minus reserved) of an item in a store falls below the minimum, the Low stock report suggests how much to order to get back to the maximum, and at least the reorder quantity. It never orders by itself.',
      [h('div', { class: 'grid' }, field('Store', wh), field('Stock item', item), field('Minimum', min), field('Maximum', max), field('Reorder quantity', re))], 'Save rule', async () => {
        await api('PUT', '/v1/stock/reorder-rules', { warehouse_id: wh.value, stock_item_id: item.value, min_qty: min.value.trim(), max_qty: max.value.trim(), reorder_qty: re.value.trim() });
        say('Reorder rule saved.');
        location.hash = '#/report/low-stock';
      });
  },

  async project() {
    const [regions, units, customers] = await Promise.all([api('GET', '/v1/org/regions'), api('GET', '/v1/org/business-units'), docOpts('customer', 'ACTIVE')]);
    const f = projectFields(null, regions.items, units.items, customers);
    formPage('New project', 'Creates the project PLANNED, in phase BID. It takes no part in costs until it passes its first stage gate (a different person approves it). The code and company cannot be changed later.',
      [f.grid], 'Create project', async () => {
        const res = await api('POST', '/v1/projects', f.body());
        say('Project created.');
        location.hash = '#/projects';
        return res;
      });
  },

  async projectedit(id) {
    const [pl, regions, units, customers] = await Promise.all([api('GET', '/v1/projects'), api('GET', '/v1/org/regions'), api('GET', '/v1/org/business-units'), docOpts('customer', 'ACTIVE')]);
    const pr = pl.projects.find((x) => x.id === id);
    if (!pr) { page('Project', 'This project does not exist or is not in your scope.'); return; }
    const f = projectFields(pr, regions.items, units.items, customers);
    formPage(`Edit ${pr.code}`, 'Changes the project\'s details. The code, company and phase are fixed here: the phase moves only through a stage gate.', [f.grid], 'Save changes', async () => {
      await api('PUT', `/v1/projects/${id}`, f.body());
      say('Project updated.');
      location.hash = '#/projects';
    });
  },

  async change_event() {
    const proj = select('project_id', projectOpts(), { required: true });
    const title = input('title', { type: 'text', required: true, minlength: '3', maxlength: '200' });
    const src = select('source', [['RFI', 'Request for information'], ['SITE_INSTRUCTION', 'Site instruction'], ['CLIENT', 'Client request'], ['DESIGN', 'Design change'], ['INTERNAL', 'Internal']].map(([value, label]) => ({ value, label })), { required: true });
    const desc = input('description', { type: 'text', required: true, minlength: '5', maxlength: '2000', placeholder: 'What changed and why' });
    const rev = input('revenue_impact', { type: 'number', step: '0.01', value: '0' });
    const days = input('schedule_impact_days', { type: 'number', step: '1', value: '0' });
    let grid;
    const holder = h('div');
    const load = async () => {
      const cc = proj.value ? (await api('GET', `/v1/projects/${proj.value}/cost-codes`)).cost_codes : [];
      grid = lineGrid('Expected cost impact by cost code (more cost is positive, a saving is negative)', [
        { key: 'cost_code_id', label: 'Cost code', make: () => select('cost_code_id', opt(cc, (c) => `${c.code} ${c.name}`)) },
        { key: 'cost_impact', label: 'Cost impact', make: num('cost_impact') },
        { key: 'note', label: 'Note', make: txt('note', { maxlength: '200' }) },
      ], [], true);
      holder.replaceChildren(grid);
    };
    proj.addEventListener('change', load);
    formPage('Raise a change event', 'Record a change to the money or time of a project before it is agreed. A different person decides it with a fresh multi-factor sign-in; if it has cost lines, approval drafts the budget revision that carries them (a draft only: the budget moves when that revision is approved).',
      [h('div', { class: 'grid' }, field('Project', proj), field('Title', title), field('Source', src), field('Description', desc), field('Revenue impact (what the client pays; signed)', rev), field('Schedule impact (days; signed)', days)), holder], 'Create change event', async () => {
        const lines = grid.rows().filter((r) => r.cost_impact !== '' && Number(r.cost_impact) !== 0);
        const res = await api('POST', '/v1/change-events', { project_id: proj.value, title: title.value.trim(), source: src.value, description: desc.value.trim(),
          revenue_impact: rev.value || '0', schedule_impact_days: Number(days.value || 0), lines });
        created('change_event', res, 'Change event');
      });
    await load();
  },

  async notice() {
    const ctr = select('contract_id', await docOpts('contract', 'ACTIVE'), { required: true });
    const clause = input('clause_ref', { type: 'text', required: true, maxlength: '100', placeholder: 'e.g. 20.1' });
    const subject = input('subject', { type: 'text', required: true, minlength: '3', maxlength: '300', placeholder: 'What happened' });
    const ev = input('event_date', { type: 'date', required: true, value: new Date().toISOString().slice(0, 10) });
    const days = input('due_days', { type: 'number', min: '1', max: '365', value: '28' });
    formPage('Raise a contract notice', 'Record an event that may entitle the contractor to time or money. The system computes the date by which the contract requires it to be notified (event date + days) and tracks it: open, due soon, overdue or sent. Send the notice, then record the day you sent it.',
      [h('div', { class: 'grid' }, field('Contract', ctr), field('Clause', clause), field('What happened', subject), field('Date of the event', ev), field('Days allowed to notify', days))], 'Create notice', async () => {
        const res = await api('POST', '/v1/notices', { contract_id: ctr.value, clause_ref: clause.value.trim(), subject: subject.value.trim(), event_date: ev.value, due_days: Number(days.value || 28) });
        created('contract_notice', res, 'Notice');
      });
  },

  async claim() {
    const ctr = select('contract_id', await docOpts('contract', 'ACTIVE'), { required: true });
    const notice = select('notice_id', [{ value: '', label: 'No notice' }]);
    const kind = select('kind', [['TIME', 'Time (extension)'], ['COST', 'Cost'], ['BOTH', 'Time and cost']].map(([value, label]) => ({ value, label })), { required: true });
    const title = input('title', { type: 'text', required: true, minlength: '3', maxlength: '200' });
    const desc = input('description', { type: 'text', required: true, minlength: '10', maxlength: '4000', placeholder: 'The basis of the claim' });
    const amount = input('amount', { type: 'number', step: '0.01', min: '0', value: '0' });
    const days = input('days', { type: 'number', step: '1', min: '0', value: '0' });
    const waiver = input('waiver_reason', { type: 'text', maxlength: '500', placeholder: 'Required if the notice is missing or was sent late' });
    const loadNotices = async () => {
      const list = ctr.value ? (await api('GET', `/v1/notices?contract=${ctr.value}`)).notices || [] : [];
      notice.replaceChildren(h('option', { value: '' }, 'No notice'), ...list.map((n) => h('option', { value: n.id }, `${n.number} — ${n.subject} (${words(n.standing.toLowerCase())})`)));
    };
    ctr.addEventListener('change', loadNotices);
    formPage('Prepare a claim', 'A claim for time, cost or both. Time needs days, cost needs an amount. Link the notice it follows: if the notice is missing or was sent late the claim cannot be submitted without a reason (for example a written waiver of the time bar). A different person approves it for sending to the client with a fresh multi-factor sign-in.',
      [h('div', { class: 'grid' }, field('Contract', ctr), field('Notice', notice), field('Kind', kind), field('Title', title), field('Basis', desc), field('Amount claimed', amount), field('Days claimed', days), field('Why it stands without a timely notice', waiver))], 'Create claim', async () => {
        const res = await api('POST', '/v1/claims', { contract_id: ctr.value, notice_id: notice.value, kind: kind.value, title: title.value.trim(), description: desc.value.trim(),
          amount: kind.value === 'TIME' ? '0' : amount.value || '0', days: kind.value === 'COST' ? 0 : Number(days.value || 0), waiver_reason: waiver.value.trim() });
        created('contract_claim', res, 'Claim');
      });
    await loadNotices();
  },

  async forecast() {
    const proj = select('project_id', projectOpts(), { required: true });
    const end = input('period_end', { type: 'date', value: new Date().toISOString().slice(0, 10), required: true });
    const note = input('note', { type: 'text', maxlength: '500', placeholder: 'Optional: what this forecast assumes' });
    formPage('Prepare a cost forecast', 'Opens this month\'s forecast for a project with every cost code that has money on it. The system fills in cost to date, commitments and the estimate to complete; you then adjust each line before submitting it for a different person to approve.',
      [h('div', { class: 'grid' }, field('Project', proj), field('Forecast as at (month end)', end), field('Note', note))], 'Create forecast', async () => {
        const res = await api('POST', `/v1/projects/${proj.value}/forecasts`, { period_end: end.value, note: note.value.trim() });
        created('forecast_cycle', res, 'Cost forecast');
      });
  },

  async stagegate(id) {
    const pl = await api('GET', '/v1/projects');
    const pr = pl.projects.find((x) => x.id === id);
    if (!pr) { page('Stage gate', 'This project does not exist or is not in your scope.'); return; }
    const next = { BID: ['MOBILISATION', 'CLOSED'], MOBILISATION: ['EXECUTION'], EXECUTION: ['PRACTICAL_COMPLETION'], PRACTICAL_COMPLETION: ['DEFECTS'], DEFECTS: ['CLOSED'] }[pr.phase] || [];
    const to = select('to_phase', next.map((v) => ({ value: v, label: words(v.toLowerCase()) })), { required: true });
    const notes = input('notes', { type: 'text', required: true, minlength: '5', maxlength: '1000', placeholder: 'The evidence for passing this gate' });
    formPage(`Stage gate for ${pr.code}`, `The project is in phase ${words(pr.phase.toLowerCase())}. Prepare the step to the next phase with the evidence for it; a different person decides it with a fresh multi-factor sign-in, and the project moves only then.`,
      [h('div', { class: 'grid' }, field('Move to', to), field('Evidence', notes))], 'Create gate', async () => {
        const res = await api('POST', `/v1/projects/${id}/gates`, { to_phase: to.value, notes: notes.value.trim() });
        created('project_stage_gate', res, 'Stage gate');
      });
  },

  async requisition() {
    const proj = select('project_id', projectOpts(), { required: true });
    const just = input('justification', { type: 'text', required: true, maxlength: '500' });
    const need = input('needed_by', { type: 'date', value: plusDays(14) });
    let grid;
    const holder = h('div');
    const load = async () => {
      const cc = proj.value ? (await api('GET', `/v1/projects/${proj.value}/cost-codes`)).cost_codes : [];
      grid = lineGrid('Requested lines', [
        { key: 'cost_code_id', label: 'Cost code', make: () => select('cost_code_id', opt(cc, (c) => `${c.code} ${c.name}`)) },
        { key: 'description', label: 'Description', make: txt('description') },
        { key: 'unit', label: 'Unit', make: txt('unit', { size: '5' }) },
        { key: 'quantity', label: 'Quantity', make: num('quantity') },
        { key: 'est_unit_price', label: 'Estimated unit price', make: num('est_unit_price') },
      ], [], true);
      holder.replaceChildren(grid);
    };
    proj.addEventListener('change', load);
    formPage('New requisition', 'Request materials or services against an approved cost code. Approval checks the project budget.',
      [h('div', { class: 'grid' }, field('Project', proj), field('Justification', just), field('Needed by', need)), holder], 'Create requisition', async () => {
        const lines = grid.rows().map((r, i) => ({ line_no: i + 1, ...r }));
        const res = await api('POST', '/v1/requisitions', { project_id: proj.value, justification: just.value.trim(), needed_by: need.value || undefined, lines });
        created('purchase_requisition', res, 'Requisition');
      });
    await load();
  },

  async boq() {
    const proj = select('project_id', projectOpts(), { required: true });
    const desc = input('description', { type: 'text', required: true });
    let grid;
    const holder = h('div');
    const load = async () => {
      const cc = proj.value ? (await api('GET', `/v1/projects/${proj.value}/cost-codes`)).cost_codes : [];
      grid = lineGrid('BOQ items', [
        { key: 'item_no', label: 'Item no.', make: txt('item_no', { size: '6' }) },
        { key: 'description', label: 'Description', make: txt('description') },
        { key: 'unit', label: 'Unit', make: txt('unit', { size: '5' }) },
        { key: 'quantity', label: 'Quantity', make: num('quantity') },
        { key: 'rate', label: 'Rate', make: num('rate') },
        { key: 'cost_code_id', label: 'Cost code', make: () => select('cost_code_id', opt(cc, (c) => `${c.code} ${c.name}`)) },
      ], [], true);
      holder.replaceChildren(grid);
    };
    proj.addEventListener('change', load);
    formPage('New BOQ version', 'Priced bill of quantities; once approved it is the budget for its cost codes.',
      [h('div', { class: 'grid' }, field('Project', proj), field('Description', desc)), holder], 'Create BOQ', async () => {
        const res = await api('POST', '/v1/boqs', { project_id: proj.value, description: desc.value.trim(), items: grid.rows() });
        created('boq_version', res, 'BOQ');
      });
    await load();
  },

  async rfq(prID) {
    const prs = await docOpts('purchase_requisition', 'APPROVED');
    const pr = select('pr_id', prs, { required: true });
    if (prID) pr.value = prID;
    const due = input('due_at', { type: 'date', value: plusDays(3), required: true });
    const min = input('min_suppliers', { type: 'number', min: '1', max: '10', value: '3' });
    const reason = input('single_source_reason', { type: 'text', maxlength: '500' });
    const sups = (await api('GET', '/v1/p2p/supplier?status=ACTIVE')).items;
    const boxes = sups.map((s) => { const c = h('input', { type: 'checkbox', name: 'supplier', value: s.id, id: nextId('s') }); return h('div', { class: 'check' }, c, h('label', { for: c.id }, `${s.number} — ${s.label}`)); });
    formPage('New RFQ', 'Invite approved suppliers to quote for an approved requisition.', [
      h('div', { class: 'grid' }, field('Requisition', pr), field('Quotes due', due), field('Minimum suppliers', min, 'Fewer than the policy minimum needs a single-source reason.')),
      field('Single-source reason', reason),
      h('fieldset', {}, h('legend', {}, 'Suppliers invited'), boxes.length ? boxes : h('p', { class: 'empty' }, 'No approved suppliers.')),
    ], 'Create RFQ', async () => {
      const ids = boxes.map((b) => b.querySelector('input')).filter((c) => c.checked).map((c) => c.value);
      const res = await api('POST', '/v1/rfqs', { pr_id: pr.value, due_at: new Date(due.value + 'T17:00:00').toISOString(), min_suppliers: Number(min.value),
        single_source_reason: reason.value.trim(), supplier_ids: ids });
      created('rfq', res, 'RFQ');
    });
  },

  async quote(rfqID) {
    const rfqs = await docOpts('rfq', 'ISSUED');
    const rfq = select('rfq_id', rfqs, { required: true });
    if (rfqID) rfq.value = rfqID;
    const holder = h('div');
    let grid, sup, valid, ref;
    const load = async () => {
      holder.textContent = '';
      if (!rfq.value) return;
      const r = (await api('GET', `/v1/p2p/rfq/${rfq.value}`)).detail;
      const prd = (await api('GET', `/v1/p2p/purchase_requisition/${r.h.pr_id}`)).detail;
      const supNames = Object.fromEntries((await api('GET', '/v1/p2p/supplier')).items.map((s) => [s.id, `${s.number} — ${s.label}`]));
      sup = select('supplier_id', (r.suppliers || []).map((id) => ({ value: id, label: supNames[id] || id })), { required: true });
      valid = input('valid_until', { type: 'date', value: plusDays(30), required: true });
      ref = input('supplier_reference', { type: 'text' });
      grid = lineGrid('Quoted prices', [
        { key: 'pr_line_no', label: 'Line', make: (v) => input('pr_line_no', { type: 'hidden', value: String(v.line_no) }) },
        { key: 'description', label: 'Description', make: ro('description') },
        { key: 'quantity', label: 'Quantity', make: ro('quantity') },
        { key: 'unit_price', label: 'Unit price', make: num('unit_price', { required: true }) },
      ], prd.lines.map((l) => ({ ...l, unit_price: '' })), false);
      holder.append(h('div', { class: 'grid' }, field('Supplier', sup), field('Valid until', valid), field('Supplier reference', ref)), grid);
    };
    rfq.addEventListener('change', load);
    formPage('Record quote', 'Enter a supplier’s written quotation against an issued RFQ.', [field('RFQ', rfq), holder], 'Record quote', async () => {
      const lines = grid.rows().map((r) => ({ pr_line_no: Number(r.pr_line_no), unit_price: r.unit_price }));
      const res = await api('POST', `/v1/rfqs/${rfq.value}/quotes`, { supplier_id: sup.value, valid_until: valid.value, supplier_reference: ref.value.trim(), lines });
      say('Quote recorded.');
      location.hash = `#/doc/rfq/${rfq.value}`;
      return res;
    });
    await load();
  },

  async po(rfqID) {
    const rfqs = await docOpts('rfq', 'AWARDED');
    const rfq = select('rfq_id', rfqs, { required: true });
    if (rfqID) rfq.value = rfqID;
    const holder = h('div');
    let grid;
    const load = async () => {
      holder.textContent = '';
      if (!rfq.value) return;
      const r = (await api('GET', `/v1/p2p/rfq/${rfq.value}`)).detail;
      const prd = (await api('GET', `/v1/p2p/purchase_requisition/${r.h.pr_id}`)).detail;
      grid = lineGrid('GL account per line', [
        { key: 'line_no', label: 'Line', make: (v) => input('line_no', { type: 'hidden', value: String(v.line_no) }) },
        { key: 'description', label: 'Description', make: ro('description') },
        { key: 'account', label: 'Expense or asset account', make: () => select('account', accountOpts(['EXPENSE', 'ASSET'])) },
      ], prd.lines, false);
      holder.append(grid);
    };
    rfq.addEventListener('change', load);
    formPage('New purchase order', 'Create the purchase order from the awarded quote. Prices come from the quote and cannot be changed here.', [field('Awarded RFQ', rfq), holder], 'Create purchase order', async () => {
      const accounts = Object.fromEntries(grid.rows().map((r) => [r.line_no, r.account]));
      const res = await api('POST', '/v1/purchase-orders', { rfq_id: rfq.value, accounts });
      created('purchase_order', res, 'Purchase order');
    });
    await load();
  },

  async grn(poID) {
    const pos = await docOpts('purchase_order', 'APPROVED');
    const po = select('po_id', pos, { required: true });
    if (poID) po.value = poID;
    const on = input('received_on', { type: 'date', value: today(), required: true });
    const dn = input('delivery_note', { type: 'text', required: true });
    const holder = h('div');
    let grid;
    const load = async () => {
      holder.textContent = '';
      if (!po.value) return;
      const d = (await api('GET', `/v1/p2p/purchase_order/${po.value}`)).detail;
      grid = lineGrid('Quantities received', [
        { key: 'po_line_no', label: 'Line', make: (v) => input('po_line_no', { type: 'hidden', value: String(v.line_no) }) },
        { key: 'description', label: 'Description', make: ro('description') },
        { key: 'quantity', label: 'Ordered', make: ro('quantity') },
        { key: 'qty_received', label: 'Received now', make: num('qty_received') },
        { key: 'note', label: 'Note', make: txt('note') },
      ], d.lines.map((l) => ({ ...l, qty_received: '' })), false);
      holder.append(grid);
    };
    po.addEventListener('change', load);
    formPage('Record goods receipt', 'Record what physically arrived on site. A site engineer confirms it.', [
      h('div', { class: 'grid' }, field('Purchase order', po), field('Received on', on), field('Delivery note', dn)), holder], 'Record receipt', async () => {
      const lines = grid.rows().filter((r) => r.qty_received).map((r) => ({ po_line_no: Number(r.po_line_no), qty_received: r.qty_received, note: r.note || undefined }));
      const res = await api('POST', '/v1/goods-receipts', { po_id: po.value, received_on: on.value, delivery_note: dn.value.trim(), lines });
      created('grn', res, 'Goods receipt');
    });
    await load();
  },

  async invoice(poID) {
    const pos = await docOpts('purchase_order', 'APPROVED');
    const po = select('po_id', pos, { required: true });
    if (poID) po.value = poID;
    const no = input('invoice_number', { type: 'text', required: true });
    const idate = input('invoice_date', { type: 'date', value: today(), required: true });
    const due = input('due_date', { type: 'date', value: plusDays(30), required: true });
    const sub = input('subtotal', { inputmode: 'decimal', required: true, readonly: true });
    const tax = input('tax_amount', { inputmode: 'decimal', required: true, value: '0' });
    const tot = input('total', { inputmode: 'decimal', required: true, readonly: true });
    const holder = h('div');
    let grid;
    const recalc = () => {
      let s = 0n;
      for (const r of grid ? grid.rows() : []) { const q = dparse(r.quantity), p = dparse(r.unit_price); if (q != null && p != null) s += dmul(q, p); }
      sub.value = dfmt(s);
      const t = dparse(tax.value);
      tot.value = t == null ? '' : dfmt(s + t);
    };
    const load = async () => {
      holder.textContent = '';
      if (!po.value) return;
      const d = (await api('GET', `/v1/p2p/purchase_order/${po.value}`)).detail;
      grid = lineGrid('Invoice lines (as billed)', [
        { key: 'po_line_no', label: 'Line', make: (v) => input('po_line_no', { type: 'hidden', value: String(v.line_no) }) },
        { key: 'description', label: 'Description', make: ro('description') },
        { key: 'quantity', label: 'Quantity billed', make: num('quantity') },
        { key: 'unit_price', label: 'Unit price billed', make: num('unit_price') },
      ], d.lines.map((l) => ({ ...l, quantity: '', unit_price: String(l.unit_price) })), false);
      grid.addEventListener('input', recalc);
      holder.append(grid);
      recalc();
    };
    tax.addEventListener('input', recalc);
    po.addEventListener('change', load);
    formPage('Enter supplier invoice', 'Billed quantities are matched against confirmed receipts and PO prices (three-way match) when you submit.', [
      h('div', { class: 'grid' }, field('Purchase order', po), field('Invoice number', no), field('Invoice date', idate), field('Due date', due)), holder,
      h('div', { class: 'grid' }, field('Subtotal', sub, 'Computed from the lines.'), field('Tax (VAT)', tax, 'As printed on the invoice.'), field('Total', tot))], 'Save invoice', async () => {
      const lines = grid.rows().filter((r) => r.quantity).map((r) => ({ po_line_no: Number(r.po_line_no), quantity: r.quantity, unit_price: r.unit_price }));
      const res = await api('POST', '/v1/invoices', { po_id: po.value, invoice_number: no.value.trim(), invoice_date: idate.value, due_date: due.value,
        subtotal: sub.value, tax_amount: tax.value.trim(), total: tot.value, lines });
      created('supplier_invoice', res, 'Invoice');
    });
    await load();
  },

  async payment(invID) {
    const invs = await docOpts('supplier_invoice', 'APPROVED');
    const inv = select('invoice_id', invs, { required: true });
    if (invID) inv.value = invID;
    const ent = select('legal_entity_id', entityOpts(), { required: true });
    const cbanks = (await api('GET', '/v1/p2p/company_bank_account?status=ACTIVE')).items;
    const cbank = select('company_bank_id', cbanks.map((b) => ({ value: b.id, label: b.label })), { required: true });
    const vdate = input('value_date', { type: 'date', value: today(), required: true });
    const amount = input('amount', { inputmode: 'decimal', required: true });
    const sbank = select('supplier_bank_id', [], { required: true });
    let supplierID = '';
    const load = async () => {
      sbank.textContent = '';
      if (!inv.value) return;
      const d = (await api('GET', `/v1/p2p/supplier_invoice/${inv.value}`)).detail.h;
      supplierID = d.supplier_id;
      amount.value = String(d.total);
      const banks = (await api('GET', '/v1/p2p/supplier_bank_account?status=APPROVED')).items;
      for (const b of banks) {
        const bd = (await api('GET', `/v1/p2p/supplier_bank_account/${b.id}`)).detail;
        if (bd.supplier_id === supplierID) sbank.append(h('option', { value: b.id }, b.label));
      }
    };
    inv.addEventListener('change', load);
    formPage('Prepare payment', 'Pays an approved invoice to the supplier’s approved bank account. Approval and release are done by other people.', [
      h('div', { class: 'grid' }, field('Invoice', inv), field('Amount to pay', amount), field('Legal entity', ent), field('Pay from', cbank),
        field('Pay to (approved account)', sbank), field('Value date', vdate))], 'Create payment instruction', async () => {
      const res = await api('POST', '/v1/payments', { legal_entity_id: ent.value, supplier_id: supplierID, supplier_bank_id: sbank.value, company_bank_id: cbank.value,
        value_date: vdate.value, allocations: [{ invoice_id: inv.value, amount: amount.value.trim() }] });
      created('payment_instruction', res, 'Payment instruction');
    });
    await load();
  },

  async supplier() {
    const ent = select('legal_entity_id', entityOpts(), { required: true });
    const code = input('code', { type: 'text', required: true });
    const name = input('name', { type: 'text', required: true });
    const tin = input('tax_id', { type: 'text', required: true });
    formPage('New supplier', 'A procurement manager approves the supplier before it can be invited or paid.', [
      h('div', { class: 'grid' }, field('Legal entity', ent), field('Supplier code', code), field('Legal name', name), field('Tax ID (TIN)', tin))], 'Create supplier', async () => {
      const res = await api('POST', '/v1/suppliers', { legal_entity_id: ent.value, code: code.value.trim(), name: name.value.trim(), tax_id: tin.value.trim() });
      created('supplier', res, 'Supplier');
    });
  },

  async supplierbank(supID) {
    const sups = await docOpts('supplier', 'ACTIVE');
    const sup = select('supplier_id', sups, { required: true });
    if (supID) sup.value = supID;
    const bank = input('bank_name', { type: 'text', required: true });
    const an = input('account_name', { type: 'text', required: true });
    const no = input('account_number', { type: 'text', required: true, autocomplete: 'off' });
    const cur = input('currency', { type: 'text', value: 'ETB', maxlength: '3', required: true });
    formPage('New supplier bank account', 'Finance verifies and approves bank details before any payment can use them.', [
      h('div', { class: 'grid' }, field('Supplier', sup), field('Bank', bank), field('Account name', an), field('Account number', no), field('Currency', cur))], 'Add bank account', async () => {
      const res = await api('POST', `/v1/suppliers/${sup.value}/bank-accounts`, { bank_name: bank.value.trim(), account_name: an.value.trim(), account_number: no.value.trim(), currency: cur.value.trim() });
      created('supplier_bank_account', res, 'Bank account');
    });
  },

  async companybank() {
    const ent = select('legal_entity_id', entityOpts(), { required: true });
    const name = input('name', { type: 'text', required: true });
    const no = input('account_number', { type: 'text', required: true, autocomplete: 'off' });
    const cur = input('currency', { type: 'text', value: 'ETB', maxlength: '3', required: true });
    const gl = select('gl_account_id', accountOpts(['ASSET']), { required: true });
    const clr = select('clearing_account_id', accountOpts(['ASSET']), { required: true });
    formPage('New company bank account', 'The CFO approves company bank accounts.', [
      h('div', { class: 'grid' }, field('Legal entity', ent), field('Name', name), field('Account number', no), field('Currency', cur),
        field('Bank GL account', gl), field('Payments-in-transit account', clr))], 'Register account', async () => {
      const res = await api('POST', '/v1/company-bank-accounts', { legal_entity_id: ent.value, name: name.value.trim(), account_number: no.value.trim(),
        currency: cur.value.trim(), gl_account_id: gl.value, clearing_account_id: clr.value });
      created('company_bank_account', res, 'Company bank account');
    });
  },

  async customer() {
    const ent = select('legal_entity_id', entityOpts(), { required: true });
    const code = input('code', { type: 'text', required: true });
    const name = input('name', { type: 'text', required: true });
    const tin = input('tax_id', { type: 'text' });
    formPage('New customer', 'A finance manager approves the customer before contracts can be made with it.', [
      h('div', { class: 'grid' }, field('Legal entity', ent), field('Customer code', code), field('Legal name', name), field('Tax ID (TIN)', tin))], 'Create customer', async () => {
      const res = await api('POST', '/v1/customers', { legal_entity_id: ent.value, code: code.value.trim(), name: name.value.trim(), tax_id: tin.value.trim() });
      created('customer', res, 'Customer');
    });
  },

  async contractpolicy() {
    const ent = select('legal_entity_id', entityOpts(), { required: true });
    const vat = input('vat_rate', { inputmode: 'decimal', value: '0.15', required: true });
    const base = select('vat_base', [{ value: 'GROSS_LESS_ADVANCE', label: 'Gross less advance recovery' }, { value: 'GROSS', label: 'Gross' }]);
    const acc = (n, types) => select(n, accountOpts(types), { required: true });
    const ar = acc('ar_account_id', ['ASSET']), ret = acc('retention_account_id', ['ASSET']), adv = acc('advance_account_id', ['LIABILITY']);
    const rev = acc('revenue_account_id', ['REVENUE']), vout = acc('vat_output_account_id', ['LIABILITY']);
    formPage('Propose contract policy', 'VAT and receivable posting accounts for client contracts. Takes effect only after the CFO approves it. Rates are tenant configuration, not tax advice.', [
      h('div', { class: 'grid' }, field('Legal entity', ent), field('VAT rate (fraction)', vat, '0.15 = 15%'), field('VAT base', base),
        field('Accounts receivable', ar), field('Retention receivable', ret), field('Advances from customers', adv), field('Contract revenue', rev), field('VAT output', vout))],
    'Propose policy', async () => {
      const res = await api('POST', '/v1/contract-policies', { legal_entity_id: ent.value, vat_rate: vat.value.trim(), vat_base: base.value, ar_account_id: ar.value,
        retention_account_id: ret.value, advance_account_id: adv.value, revenue_account_id: rev.value, vat_output_account_id: vout.value });
      created('contract_policy', res, 'Contract policy');
    });
  },

  async contract() {
    const proj = select('project_id', projectOpts(), { required: true });
    const cust = select('customer_id', await docOpts('customer', 'ACTIVE'), { required: true });
    const title = input('title', { type: 'text', required: true, maxlength: '200' });
    const ref = input('client_reference', { type: 'text', required: true, maxlength: '100' });
    const signed = input('signed_on', { type: 'date', value: today(), required: true });
    const ret = input('retention_pct', { inputmode: 'decimal', value: '0.05', required: true });
    const adv = input('advance_amount', { inputmode: 'decimal', value: '0', required: true });
    const rec = input('advance_recovery_pct', { inputmode: 'decimal', value: '0', required: true });
    const grid = lineGrid('Contract lines (bill of quantities as signed)', [
      { key: 'description', label: 'Description', make: txt('description') },
      { key: 'unit', label: 'Unit', make: txt('unit', { size: '5' }) },
      { key: 'quantity', label: 'Quantity', make: num('quantity') },
      { key: 'rate', label: 'Rate', make: num('rate') },
    ], [], true);
    formPage('New client contract', 'The contract value is computed from the lines. The CFO activates the contract with a recent multi-factor sign-in.', [
      h('div', { class: 'grid' }, field('Project', proj), field('Customer', cust), field('Title', title), field('Client reference', ref), field('Signed on', signed),
        field('Retention (fraction)', ret, '0.05 = 5%'), field('Advance amount', adv), field('Advance recovery (fraction of each certificate)', rec)), grid], 'Create contract', async () => {
      const lines = grid.rows().map((r, i) => ({ line_no: i + 1, ...r }));
      const res = await api('POST', '/v1/contracts', { project_id: proj.value, customer_id: cust.value, title: title.value.trim(), client_reference: ref.value.trim(),
        signed_on: signed.value, retention_pct: ret.value.trim(), advance_amount: adv.value.trim(), advance_recovery_pct: rec.value.trim(), lines });
      created('contract', res, 'Contract');
    });
  },

  async measurement(contractID) {
    const ctr = select('contract_id', await docOpts('contract', 'ACTIVE'), { required: true });
    if (contractID) ctr.value = contractID;
    const period = input('period_end', { type: 'date', value: today(), required: true });
    let grid;
    const holder = h('div');
    const load = async () => {
      const lines = ctr.value ? ((await api('GET', `/v1/p2p/contract/${ctr.value}`)).detail.lines || []) : [];
      grid = lineGrid('Quantities measured this period (leave blank for no work)', [
        { key: 'contract_line_no', label: 'Line', make: (v) => input('contract_line_no', { type: 'hidden', value: String(v.line_no) }) },
        { key: 'description', label: 'Description', make: ro('description') },
        { key: 'unit', label: 'Unit', make: ro('unit') },
        { key: 'contract_qty', label: 'Signed quantity (variations add to it)', make: (v) => h('span', {}, String(v.contract_qty)) },
        { key: 'quantity', label: 'Measured quantity', make: num('quantity') },
      ], lines.map((l) => ({ line_no: l.line_no, description: l.description, unit: l.unit, contract_qty: l.quantity })), false);
      holder.replaceChildren(grid);
    };
    ctr.addEventListener('change', load);
    formPage('Record measurement', 'Quantities of work done in the period. A site engineer verifies them; the database refuses quantities beyond the contract.', [
      h('div', { class: 'grid' }, field('Contract', ctr), field('Period ending', period)), holder], 'Create measurement', async () => {
      const lines = grid.rows().filter((r) => r.quantity).map((r) => ({ contract_line_no: Number(r.contract_line_no), quantity: r.quantity }));
      if (!lines.length) throw new Error('Enter at least one measured quantity.');
      const res = await api('POST', `/v1/contracts/${ctr.value}/measurements`, { period_end: period.value, lines });
      created('measurement', res, 'Measurement');
    });
    await load();
  },

  async variation(contractID) {
    const ctr = select('contract_id', await docOpts('contract', 'ACTIVE'), { required: true });
    if (contractID) ctr.value = contractID;
    const title = input('title', { type: 'text', maxlength: '300', required: true });
    const reason = input('reason', { type: 'text', maxlength: '2000', required: true });
    const ref = input('client_instruction_ref', { type: 'text', maxlength: '100', required: true });
    const file = h('input', { type: 'file', name: 'instruction_file', accept: '.pdf,image/*' });
    const sum = h('output', { class: 'hint', 'aria-live': 'polite' });
    let sha = '';
    file.addEventListener('change', async () => {
      sha = '';
      sum.textContent = '';
      if (!file.files.length) return;
      const buf = await crypto.subtle.digest('SHA-256', await file.files[0].arrayBuffer());
      sha = [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, '0')).join('');
      sum.textContent = `SHA-256 ${sha}`;
    });
    let changes;
    const holder = h('div');
    const load = async () => {
      const lines = ctr.value ? ((await api('GET', `/v1/p2p/contract/${ctr.value}`)).detail.lines || []) : [];
      changes = lineGrid('Quantity changes to existing lines (negative for an omission; leave blank for none)', [
        { key: 'line_no', label: 'Line', make: (v) => input('line_no', { type: 'hidden', value: String(v.line_no) }) },
        { key: 'description', label: 'Description', make: ro('description') },
        { key: 'rate', label: 'Contract rate', make: (v) => h('span', {}, money(v.rate)) },
        { key: 'qty_delta', label: 'Quantity change', make: num('qty_delta') },
      ], lines.map((l) => ({ line_no: l.line_no, description: l.description, rate: l.rate })), false);
      holder.replaceChildren(changes);
    };
    ctr.addEventListener('change', load);
    const added = lineGrid('New lines', [
      { key: 'line_no', label: 'Line number', make: num('line_no') },
      { key: 'description', label: 'Description', make: txt('description') },
      { key: 'unit', label: 'Unit', make: txt('unit', { size: '6' }) },
      { key: 'qty_delta', label: 'Quantity', make: num('qty_delta') },
      { key: 'rate', label: 'Rate', make: num('rate') },
    ], [], true);
    formPage('Record variation', 'A change to the contract on the client\'s written instruction. Quantity changes are priced at the contract rate; new lines carry their own rate. The CFO approves with a fresh multi-factor sign-in.', [
      h('div', { class: 'grid' }, field('Contract', ctr), field('Title', title), field('Client instruction reference', ref)), field('Reason', reason),
      field('Signed client instruction', file, 'Its SHA-256 is recorded with the variation; the file stays with you.'), sum, holder, added], 'Create variation', async () => {
      if (!sha) throw new Error('Attach the signed client instruction so its SHA-256 can be recorded.');
      const lines = [...changes.rows().filter((r) => r.qty_delta).map((r) => ({ line_no: Number(r.line_no), kind: 'CHANGE', qty_delta: r.qty_delta })),
        ...added.rows().filter((r) => r.line_no && r.qty_delta).map((r) => ({ line_no: Number(r.line_no), kind: 'NEW', description: r.description, unit: r.unit,
          qty_delta: r.qty_delta, rate: r.rate || '0' }))];
      if (!lines.length) throw new Error('Enter at least one quantity change or new line.');
      const res = await api('POST', `/v1/contracts/${ctr.value}/variations`, { title: title.value.trim(), reason: reason.value.trim(),
        client_instruction_ref: ref.value.trim(), client_instruction_sha256: sha, lines });
      created('contract_variation', res, 'Variation');
    });
    await load();
  },

  async ipc(contractID) {
    const ctr = select('contract_id', await docOpts('contract', 'ACTIVE'), { required: true });
    if (contractID) ctr.value = contractID;
    const period = input('period_end', { type: 'date', value: today(), required: true });
    formPage('Prepare payment certificate', 'Every amount — gross, retention, advance recovery, VAT and amount due — is computed from verified measurements and the contract.', [
      h('div', { class: 'grid' }, field('Contract', ctr), field('Period ending', period))], 'Prepare certificate', async () => {
      const res = await api('POST', `/v1/contracts/${ctr.value}/ipcs`, { period_end: period.value });
      created('ipc', res, 'Payment certificate');
    });
  },

  async receipt(lineID) {
    const lines = (await api('GET', '/v1/p2p/money_in?status=UNAPPLIED')).items;
    const line = select('statement_line_id', lines.map((l) => ({ value: l.id, label: `${l.number} ${l.label} — ${money(l.amount)}` })), { required: true });
    if (lineID) line.value = lineID;
    const targets = [...(await docOpts('customer_invoice', 'APPROVED')).map((o) => ({ value: 'inv:' + o.value, label: 'Invoice ' + o.label })),
      ...(await docOpts('retention_release', 'APPROVED')).map((o) => ({ value: 'rel:' + o.value, label: 'Retention release ' + o.label })),
      ...(await docOpts('contract', 'ACTIVE')).map((o) => ({ value: 'adv:' + o.value, label: 'Advance on ' + o.label }))];
    const amountOf = () => (lines.find((l) => l.id === line.value) || {}).amount || '';
    const grid = lineGrid('Apply the money to', [
      { key: 'target', label: 'Invoice or contract advance', make: () => select('target', targets) },
      { key: 'amount', label: 'Amount', make: num('amount') },
    ], [{ amount: amountOf() }], true);
    formPage('Record receipt', 'Apply money received on the bank statement. The whole amount must be applied; an AR supervisor independent of the statement import confirms it.', [
      h('div', { class: 'grid' }, field('Money received', line)), grid], 'Record receipt', async () => {
      const key = { inv: 'invoice_id', rel: 'retention_release_id', adv: 'contract_id' };
      const allocations = grid.rows().filter((r) => r.target && r.amount).map((r) => ({ [key[r.target.slice(0, 3)]]: r.target.slice(4), amount: r.amount }));
      const res = await api('POST', '/v1/receipts', { statement_line_id: line.value, allocations });
      created('customer_receipt', res, 'Receipt');
    });
  },

  async creditnote(invoiceID) {
    const inv = select('invoice_id', await docOpts('customer_invoice', 'APPROVED'), { required: true });
    if (invoiceID) inv.value = invoiceID;
    const kind = select('kind', [{ value: 'COMMERCIAL', label: 'Commercial settlement (amount)' }, { value: 'QUANTITY_CORRECTION', label: 'Quantity correction (contract line and quantity)' }]);
    const amount = input('net_amount', { inputmode: 'decimal', autocomplete: 'off' });
    const line = input('correction_line_no', { inputmode: 'numeric', autocomplete: 'off' });
    const qty = input('correction_qty', { inputmode: 'decimal', autocomplete: 'off' });
    const date = input('credit_date', { type: 'date', value: today(), required: true });
    const reason = input('reason', { type: 'text', maxlength: '500', required: true });
    formPage('Issue credit note', 'Reduces what an approved invoice bills. A commercial settlement is an amount before VAT; a quantity correction names the contract line and the over-measured quantity, and is valued at the line rate (it also reduces the verified quantity, so the work is never deducted twice). A commercial manager approves it with a fresh multi-factor sign-in.', [
      h('div', { class: 'grid' }, field('Customer invoice', inv), field('Kind', kind), field('Credit date', date)),
      h('div', { class: 'grid' }, field('Amount credited (before VAT)', amount), field('Contract line', line), field('Quantity over-measured', qty)),
      field('Reason', reason)], 'Issue credit note', async () => {
      const body = { credit_date: date.value, reason: reason.value.trim() };
      if (kind.value === 'QUANTITY_CORRECTION') { body.correction_line_no = Number(line.value); body.correction_qty = qty.value.trim(); } else { body.net_amount = amount.value.trim(); }
      const res = await api('POST', `/v1/customer-invoices/${inv.value}/credit-notes`, body);
      created('customer_credit_note', res, 'Credit note');
    });
  },

  async retention(contractID) {
    const ctr = select('contract_id', await docOpts('contract', 'ACTIVE'), { required: true });
    if (contractID) ctr.value = contractID;
    const amount = input('amount', { inputmode: 'decimal', autocomplete: 'off', required: true });
    const date = input('release_date', { type: 'date', value: today(), required: true });
    const milestone = select('milestone', [{ value: 'TAKING_OVER', label: 'Taking over (practical completion)' },
      { value: 'DEFECTS_LIABILITY_END', label: 'End of defects liability period' }, { value: 'NEGOTIATED', label: 'Negotiated release' }]);
    const ref = input('certificate_ref', { type: 'text', maxlength: '100', required: true });
    const file = h('input', { type: 'file', name: 'certificate_file', accept: '.pdf,image/*' });
    const sum = h('output', { class: 'hint', 'aria-live': 'polite' });
    let sha = '';
    file.addEventListener('change', async () => {
      sha = '';
      sum.textContent = '';
      if (!file.files.length) return;
      const buf = await crypto.subtle.digest('SHA-256', await file.files[0].arrayBuffer());
      sha = [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, '0')).join('');
      sum.textContent = `SHA-256 ${sha}`;
    });
    formPage('Release retention', 'Records the client\'s retention release certificate. The amount becomes receivable once a commercial manager approves it with a fresh multi-factor sign-in; it cannot exceed the retention still held.', [
      h('div', { class: 'grid' }, field('Contract', ctr), field('Amount released', amount), field('Release date', date), field('Milestone', milestone),
        field('Client certificate reference', ref)),
      field('Signed release certificate', file, 'Its SHA-256 is recorded with the release; the file stays with you.'), sum], 'Record release', async () => {
      if (!sha) throw new Error('Attach the signed release certificate so its SHA-256 can be recorded.');
      const res = await api('POST', `/v1/contracts/${ctr.value}/retention-releases`, { amount: amount.value.trim(), release_date: date.value,
        milestone: milestone.value, certificate_ref: ref.value.trim(), certificate_sha256: sha });
      created('retention_release', res, 'Retention release');
    });
  },

  async schedule() {
    const proj = select('project_id', projectOpts(), { required: true });
    const desc = input('description', { type: 'text', maxlength: '500', required: true });
    const start = input('start_date', { type: 'date', value: today(), required: true });
    const finish = input('required_finish', { type: 'date' });
    const holidays = input('holidays', { type: 'text', placeholder: '2026-12-25, 2027-01-07' });
    const days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'].map((name, i) => {
      const cb = h('input', { type: 'checkbox', name: 'weekday', value: String(i), id: nextId('wd') });
      cb.checked = i >= 1 && i <= 6;
      return h('div', { class: 'check' }, cb, h('label', { for: cb.id }, name));
    });
    let acts;
    const actHolder = h('div');
    const loadActs = async () => {
      const ccs = proj.value ? (await api('GET', `/v1/projects/${proj.value}/cost-codes`)).cost_codes : [];
      const ccOpts = [{ value: '', label: '(none)' }, ...opt(ccs, (c) => `${c.code} ${c.name}`)];
      acts = lineGrid('Activities', [
        { key: 'code', label: 'Code', make: txt('code', { size: '8' }) },
        { key: 'name', label: 'Name', make: txt('name') },
        { key: 'duration', label: 'Duration (working days)', make: num('duration') },
        { key: 'cost_code_id', label: 'Cost code', make: () => select('cost_code_id', ccOpts) },
        { key: 'budget', label: 'Budget', make: num('budget') },
      ], [], true);
      actHolder.replaceChildren(acts);
    };
    proj.addEventListener('change', () => loadActs().catch(showError));
    await loadActs();
    const links = lineGrid('Dependencies', [
      { key: 'pred', label: 'Predecessor', make: txt('pred', { size: '8' }) },
      { key: 'succ', label: 'Successor', make: txt('succ', { size: '8' }) },
      { key: 'type', label: 'Type', make: () => select('type', ['FS', 'SS', 'FF', 'SF'].map((x) => ({ value: x, label: x }))) },
      { key: 'lag', label: 'Lag (days)', make: num('lag') },
    ], [], true);
    formPage('New schedule', 'Activities and dependencies on a working-day calendar. The critical path is computed when you save and verified again by the database when you submit; a project manager approves it as the baseline.', [
      h('div', { class: 'grid' }, field('Project', proj), field('Description', desc), field('Start date', start), field('Required finish (optional)', finish),
        field('Holidays (dates, comma-separated)', holidays)),
      h('fieldset', {}, h('legend', {}, 'Working days'), days), actHolder, links], 'Compute and save', async () => {
      const weekdays = [...document.querySelectorAll('input[name=weekday]')].filter((c) => c.checked).map((c) => Number(c.value));
      const hol = holidays.value.split(',').map((x) => x.trim()).filter(Boolean);
      const activities = acts.rows().filter((r) => r.code).map((r) => ({ code: r.code, name: r.name, duration: Number(r.duration || 0),
        budget: r.budget || '0', cost_code_id: r.cost_code_id || undefined }));
      const deps = links.rows().filter((r) => r.pred && r.succ).map((r) => ({ pred: r.pred, succ: r.succ, type: r.type, lag: Number(r.lag || 0) }));
      const res = await api('POST', `/v1/projects/${proj.value}/schedules`, { description: desc.value.trim(), start_date: start.value, working_weekdays: weekdays,
        holidays: hol, required_finish: finish.value || undefined, activities, links: deps });
      created('schedule_version', res, 'Schedule');
    });
  },

  async diary() {
    const proj = select('project_id', projectOpts(), { required: true });
    const at = input('diary_date', { type: 'date', value: today(), required: true });
    const weather = input('weather', { type: 'text', required: true, maxlength: '200' });
    const notes = h('textarea', { name: 'notes', maxlength: '8000', rows: '4' });
    const labour = lineGrid('Labour (trade, headcount, person-hours)', [
      { key: 'trade', label: 'Trade', make: txt('trade', { maxlength: '100' }) },
      { key: 'headcount', label: 'Headcount', make: num('headcount', { inputmode: 'numeric' }) },
      { key: 'hours', label: 'Person-hours', make: num('hours') },
    ], [], true);
    const equipment = lineGrid('Equipment (hours worked and idle)', [
      { key: 'description', label: 'Equipment', make: txt('description', { maxlength: '200' }) },
      { key: 'hours', label: 'Hours worked', make: num('hours') },
      { key: 'idle_hours', label: 'Hours idle', make: num('idle_hours') },
    ], [], true);
    formPage('Record a site diary', 'One diary per project and day. A project manager approves it after you submit it. Field devices record diaries offline and synchronise them.', [
      h('div', { class: 'grid' }, field('Project', proj), field('Date', at), field('Weather', weather)), field('Notes', notes), labour, equipment], 'Save diary', async () => {
      const lab = labour.rows().filter((r) => r.trade).map((r) => ({ trade: r.trade, headcount: Number(r.headcount), hours: r.hours }));
      const eq = equipment.rows().filter((r) => r.description).map((r) => ({ description: r.description, hours: r.hours || '0', idle_hours: r.idle_hours || '0' }));
      const res = await api('POST', '/v1/site/diaries', { client_ref: crypto.randomUUID(), project_id: proj.value, diary_date: at.value, weather: weather.value.trim(),
        notes: notes.value, base_version: 0, labour: lab, equipment: eq });
      created('site_diary', res, 'Site diary');
    });
  },

  async progress() {
    const proj = select('project_id', projectOpts(), { required: true });
    const at = input('status_date', { type: 'date', value: today(), required: true });
    let grid;
    let seq = 0;
    const holder = h('div');
    const load = async () => {
      const mine = ++seq;
      grid = null;
      holder.textContent = '';
      if (!proj.value) return;
      const base = (await api('GET', `/v1/p2p/schedule_version?status=BASELINE`)).items.find((x) => x.project_id === proj.value);
      if (mine !== seq) return;
      if (!base) { holder.append(h('p', { class: 'empty' }, 'The project has no baseline schedule.')); return; }
      const acts = (await api('GET', `/v1/p2p/schedule_version/${base.id}`)).detail.activities || [];
      if (mine !== seq) return;
      grid = lineGrid('Progress by activity (leave percent blank to skip)', [
        { key: 'activity_code', label: 'Activity', make: (v) => input('activity_code', { type: 'hidden', value: v.code }) },
        { key: 'name', label: 'Name', make: ro('name') },
        { key: 'percent_complete', label: 'Percent complete', make: num('percent_complete') },
        { key: 'actual_start', label: 'Actual start', make: () => input('actual_start', { type: 'date' }) },
        { key: 'actual_finish', label: 'Actual finish', make: () => input('actual_finish', { type: 'date' }) },
      ], acts.map((a) => ({ code: a.code, name: `${a.code} ${a.name}` })), false);
      holder.replaceChildren(grid);
    };
    proj.addEventListener('change', () => load().catch(showError));
    formPage('Report progress', 'Percent complete and actual dates at a status date, against the approved baseline. A project manager approves the report; earned value uses it.', [
      h('div', { class: 'grid' }, field('Project', proj), field('Status date', at)), holder], 'Record progress', async () => {
      const lines = (grid ? grid.rows() : []).filter((r) => r.percent_complete !== '').map((r) => ({ activity_code: r.activity_code, percent_complete: r.percent_complete,
        actual_start: r.actual_start || undefined, actual_finish: r.actual_finish || undefined }));
      if (!lines.length) throw new Error('Enter progress for at least one activity.');
      const res = await api('POST', `/v1/projects/${proj.value}/progress`, { status_date: at.value, lines });
      created('schedule_progress', res, 'Progress report');
    });
    await load();
  },

  async designate(poID) {
    const po = (await api('GET', `/v1/p2p/purchase_order/${poID}`)).detail;
    const assets = new Set(S.ref.accounts.filter((x) => x.kind === 'ASSET').map((x) => x.id));
    const open = (po.lines || []).filter((l) => !l.stock_item_id && assets.has(l.account_id));
    const line = select('line_no', open.map((l) => ({ value: String(l.line_no), label: `${l.line_no} — ${l.description} (${l.unit})` })), { required: true });
    const item = select('stock_item_id', await docOpts('stock_item', 'ACTIVE'), { required: true });
    formPage('Designate stock items', 'Names the stock item an inventory line of this purchase order delivers. It is set once, before anything is put away; the storekeeper then puts away only that item.', [
      h('div', { class: 'grid' }, field('Purchase order line', line), field('Stock item', item))], 'Designate', async () => {
      await api('POST', `/v1/purchase-orders/${poID}/lines/${line.value}/stock-item`, { stock_item_id: item.value });
      say('Stock item designated.');
      location.hash = `#/doc/purchase_order/${poID}`;
    });
  },

  async stockitem() {
    const ent = select('legal_entity_id', entityOpts(), { required: true });
    const code = input('code', { type: 'text', required: true });
    const name = input('name', { type: 'text', required: true });
    const unit = input('unit', { type: 'text', required: true, size: '6' });
    const method = select('valuation_method', [{ value: 'FIFO', label: 'FIFO (first in, first out)' }, { value: 'WAC', label: 'Weighted average cost' }]);
    const acct = select('inventory_account_id', accountOpts(['ASSET']), { required: true });
    formPage('New stock item', 'Finance approves the item before stock can be received or issued. The unit must match the purchase order line.', [
      h('div', { class: 'grid' }, field('Legal entity', ent), field('Item code', code), field('Name', name), field('Unit', unit), field('Valuation', method),
        field('Inventory account', acct))], 'Create stock item', async () => {
      const res = await api('POST', '/v1/stock-items', { legal_entity_id: ent.value, code: code.value.trim(), name: name.value.trim(), unit: unit.value.trim(),
        valuation_method: method.value, inventory_account_id: acct.value });
      created('stock_item', res, 'Stock item');
    });
  },

  async warehouse() {
    const ent = select('legal_entity_id', entityOpts(), { required: true });
    const proj = select('project_id', [{ value: '', label: 'Central (no project)' }, ...projectOpts()]);
    const code = input('code', { type: 'text', required: true });
    const name = input('name', { type: 'text', required: true });
    formPage('New warehouse', 'A project store holds stock for one project; a central warehouse serves the whole entity.', [
      h('div', { class: 'grid' }, field('Legal entity', ent), field('Project', proj), field('Code', code), field('Name', name))], 'Create warehouse', async () => {
      const res = await api('POST', '/v1/warehouses', { legal_entity_id: ent.value, project_id: proj.value || undefined, code: code.value.trim(), name: name.value.trim() });
      created('warehouse', res, 'Warehouse');
    });
  },

  async putaway(grnID) {
    const grns = await docOpts('grn', 'CONFIRMED');
    const grn = select('grn_id', grns, { required: true });
    if (grnID) grn.value = grnID;
    const items = select('stock_item_id', await docOpts('stock_item', 'ACTIVE'), { required: true });
    const wh = select('warehouse_id', await docOpts('warehouse', 'ACTIVE'), { required: true });
    const line = input('po_line_no', { type: 'number', min: '1', value: '1', required: true });
    const qty = input('quantity', { inputmode: 'decimal', required: true });
    formPage('Put away into a warehouse', 'Moves received goods into stock at the purchase order price. The goods receipt line must have been posted to the item\'s inventory account.', [
      h('div', { class: 'grid' }, field('Goods receipt', grn), field('Purchase order line', line), field('Stock item', items), field('Warehouse', wh), field('Quantity', qty))],
    'Put away', async () => {
      await api('POST', `/v1/goods-receipts/${grn.value}/put-away`, { po_line_no: Number(line.value), stock_item_id: items.value, warehouse_id: wh.value, quantity: qty.value.trim() });
      say('Put away.');
      location.hash = '#/stock';
    });
  },

  async stocktransfer() {
    const whs = await docOpts('warehouse', 'ACTIVE');
    const from = select('from_warehouse_id', whs, { required: true });
    const to = select('to_warehouse_id', whs, { required: true });
    const item = select('stock_item_id', await docOpts('stock_item', 'ACTIVE'), { required: true });
    const qty = input('quantity', { inputmode: 'decimal', required: true });
    const date = input('transfer_date', { type: 'date', value: today(), required: true });
    const note = input('note', { type: 'text', maxlength: '500' });
    formPage('Transfer stock', 'Moves stock between two warehouses at cost; nothing is expensed and no journal is needed. An inventory controller approves it.', [
      h('div', { class: 'grid' }, field('From warehouse', from), field('To warehouse', to), field('Stock item', item), field('Quantity', qty), field('Transfer date', date)),
      field('Note', note)], 'Request transfer', async () => {
      if (from.value === to.value) throw new Error('Choose two different warehouses.');
      const res = await api('POST', '/v1/stock-transfers', { stock_item_id: item.value, from_warehouse_id: from.value, to_warehouse_id: to.value,
        quantity: qty.value.trim(), transfer_date: date.value, note: note.value.trim() || undefined });
      created('stock_transfer', res, 'Stock transfer');
    });
  },

  async stockissue() {
    const wh = select('warehouse_id', await docOpts('warehouse', 'ACTIVE'), { required: true });
    const item = select('stock_item_id', await docOpts('stock_item', 'ACTIVE'), { required: true });
    const proj = select('project_id', projectOpts(), { required: true });
    const qty = input('quantity', { inputmode: 'decimal', required: true });
    const unit = input('unit', { type: 'text', maxlength: '20', placeholder: 'Leave empty for the item\'s own unit' });
    const resv = select('reservation_id', [{ value: '', label: 'None (use unreserved stock)' }, ...((await api('GET', '/v1/stock/reservations?status=ACTIVE')).reservations || []).map((r) => ({ value: r.id, label: `${r.item_code} ${r.quantity} in ${r.warehouse}: ${r.purpose}` }))]);
    const date = input('issue_date', { type: 'date', value: today(), required: true });
    const acct = select('expense_account_id', accountOpts(['EXPENSE']), { required: true });
    const cc = select('cost_code_id', [], { required: true });
    const loadCC = async () => {
      cc.textContent = '';
      if (!proj.value) return;
      for (const c of (await api('GET', `/v1/projects/${proj.value}/cost-codes`)).cost_codes) cc.append(h('option', { value: c.id }, `${c.code} ${c.name}`));
    };
    proj.addEventListener('change', () => loadCC().catch(showError));
    formPage('Issue stock to a project', 'The site engineer or project manager approves the issue; its cost (FIFO or average) is computed then and charged to the cost code.', [
      h('div', { class: 'grid' }, field('Warehouse', wh), field('Stock item', item), field('Quantity', qty), field('Unit (optional)', unit, 'Counted in another unit? It is converted with an approved conversion.'), field('Against a reservation', resv, 'An issue against a reservation uses it up when approved.'),
        field('Project', proj), field('Cost code', cc), field('Expense account', acct), field('Issue date', date))], 'Request issue', async () => {
      const res = await api('POST', '/v1/stock-issues', { warehouse_id: wh.value, stock_item_id: item.value, project_id: proj.value, cost_code_id: cc.value,
        expense_account_id: acct.value, quantity: qty.value.trim(), unit: unit.value.trim() || undefined, reservation_id: resv.value || undefined, issue_date: date.value });
      created('stock_issue', res, 'Stock issue');
    });
    await loadCC();
  },

  async stockcount() {
    const wh = select('warehouse_id', await docOpts('warehouse', 'ACTIVE'), { required: true });
    const item = select('stock_item_id', await docOpts('stock_item', 'ACTIVE'), { required: true });
    const qty = input('counted_qty', { inputmode: 'decimal', required: true });
    const date = input('count_date', { type: 'date', value: today(), required: true });
    const acct = select('variance_account_id', accountOpts(['EXPENSE']), { required: true });
    formPage('Record a stock count', 'The book quantity is taken when the count is recorded; finance approves the adjustment, and a count that has become stale is refused.', [
      h('div', { class: 'grid' }, field('Warehouse', wh), field('Stock item', item), field('Counted quantity', qty), field('Count date', date), field('Variance account', acct))],
    'Record count', async () => {
      const res = await api('POST', '/v1/stock-counts', { warehouse_id: wh.value, stock_item_id: item.value, counted_qty: qty.value.trim(),
        variance_account_id: acct.value, count_date: date.value });
      created('stock_count', res, 'Stock count');
    });
  },

  async policy() {
    const ent = select('legal_entity_id', entityOpts(), { required: true });
    const f = (n, v) => input(n, { inputmode: 'decimal', value: v, required: true });
    const tol = f('price_tolerance_pct', '0.02'), over = f('over_receipt_pct', '0.05'), vat = f('vat_rate', '0.15');
    const hv = f('high_value_po_threshold', '5000000'), dual = f('dual_payment_threshold', '500000');
    const acc = (n, types) => select(n, accountOpts(types), { required: true });
    const grni = acc('grni_account_id', ['LIABILITY']), ap = acc('ap_account_id', ['LIABILITY']), vin = acc('vat_input_account_id', ['ASSET']), ppv = acc('ppv_account_id', ['EXPENSE']);
    formPage('Propose P2P policy', 'Tolerances, thresholds and posting accounts. Takes effect only after another person approves it. Rates are tenant configuration, not tax advice.', [
      h('div', { class: 'grid' }, field('Legal entity', ent), field('Price tolerance (fraction)', tol, '0.02 = 2%'), field('Over-receipt allowance (fraction)', over),
        field('VAT rate (fraction)', vat), field('High-value PO threshold', hv), field('Dual-approval payment threshold', dual),
        field('GRNI account', grni), field('Accounts payable account', ap), field('VAT input account', vin), field('Price variance account', ppv))], 'Propose policy', async () => {
      const res = await api('POST', '/v1/p2p/policies', { legal_entity_id: ent.value, price_tolerance_pct: tol.value, over_receipt_pct: over.value, vat_rate: vat.value,
        high_value_po_threshold: hv.value, dual_payment_threshold: dual.value, grni_account_id: grni.value, ap_account_id: ap.value, vat_input_account_id: vin.value, ppv_account_id: ppv.value });
      created('p2p_policy', res, 'Policy');
    });
  },
};

// ---------- journals ----------
async function renderJournals() {
  const p2pOnly = h('input', { type: 'checkbox', id: nextId('c'), checked: true });
  const fetchJ = async (status) => (p2pOnly.checked
    ? (await Promise.all(['P2P_', 'AR_', 'STK_'].map((p) => api('GET', `/v1/journals?status=${status}&source_prefix=${p}`)))).flatMap((r) => r.journals)
    : (await api('GET', `/v1/journals?status=${status}`)).journals);
  const holder = h('div');
  page('Journals', 'Subledger documents create draft journals. A finance manager approves them and a controller posts them; neither can be the document’s creator.',
    h('div', { class: 'toolbar' }, h('div', { class: 'check' }, p2pOnly, h('label', { for: p2pOnly.id }, 'Subledger journals only (procure-to-pay, receivables, stock)'))), holder);
  const load = async () => {
    holder.textContent = '';
    const sections = [['DRAFT', 'journal.approve', 'approve-batch', 'Approve selected'], ['APPROVED', 'journal.post', 'post-batch', 'Post selected']];
    for (const [status, perm, path, label] of sections) {
      const js = await fetchJ(status);
      const can = S.can.has(perm);
      const boxes = new Map();
      const all = h('input', { type: 'checkbox', 'aria-label': `Select all ${status.toLowerCase()} journals` });
      all.addEventListener('change', () => boxes.forEach((b) => { b.checked = all.checked; }));
      const cols = [
        can ? { label: all, cell: (j) => { const b = h('input', { type: 'checkbox', 'aria-label': `Select journal ${j.source_ref || j.id.slice(0, 8)}` }); boxes.set(j.id, b); return b; } } : null,
        { label: 'Source', cell: (j) => `${j.source_type} ${j.source_ref || ''}` },
        { label: 'Description', cell: (j) => j.description },
        { label: 'Date', cell: (j) => (j.business_event_at || '').slice(0, 10) },
        { label: 'Debits', num: true, cell: (j) => money(j.debits) },
        { label: 'Created by', cell: (j) => j.created_by },
        status === 'APPROVED' ? { label: 'Approved by', cell: (j) => j.approved_by || '' } : null,
      ].filter(Boolean);
      const out = h('div');
      const btn = can && js.length ? h('button', { type: 'button' }, label) : null;
      if (btn) btn.addEventListener('click', async () => {
        clearError();
        const ids = [...boxes].filter(([, b]) => b.checked).map(([id]) => id);
        if (!ids.length) { showError(new Error('Select at least one journal.')); return; }
        btn.disabled = true;
        try {
          const res = (await api('POST', `/v1/journals/${path}`, { journal_ids: ids })).results;
          const ok = res.filter((r) => r.ok).length;
          await load();
          const failed = res.filter((r) => !r.ok);
          holder.prepend(h('p', { class: failed.length ? 'alert' : 'notice', role: 'status' }, `${ok} of ${res.length} journals done.`,
            failed.map((r) => h('span', {}, h('br'), `${r.journal_id.slice(0, 8)}: ${r.error}`))));
          say(`${ok} of ${res.length} journals done.`);
        } catch (e) { btn.disabled = false; showError(e); }
      });
      holder.append(h('div', { class: 'pagehead' }, h('h2', { class: 'section' }, status === 'DRAFT' ? 'Waiting for approval' : 'Approved, waiting to post'),
        exportBar(`/v1/journals?status=${status}`, `Journals — ${status === 'DRAFT' ? 'waiting for approval' : 'approved, waiting to post'}`)),
        ...[table(`${status === 'DRAFT' ? 'Draft' : 'Approved'} journals (${js.length})`, cols, js, 'None.'), btn ? h('div', { class: 'actions' }, btn) : null, out].filter(Boolean));
    }
  };
  p2pOnly.addEventListener('change', () => load().catch(showError));
  await load();
}

// ---------- bank ----------
async function renderBank() {
  const parts = [];
  if (S.can.has('bank.import')) {
    const banks = (await api('GET', '/v1/p2p/company_bank_account?status=ACTIVE')).items;
    const file = h('input', { type: 'file', name: 'statement', accept: 'application/json,.json', required: true });
    const preview = h('div');
    let raw = null;
    file.addEventListener('change', async () => {
      preview.textContent = '';
      raw = null;
      const f = file.files[0];
      if (!f) return;
      if (f.size > 900000) { showError(new Error('Statement file is too large (limit 900 kB).')); return; }
      raw = await f.text();
      try {
        const st = JSON.parse(raw);
        preview.append(table(`Statement ${st.statement_no || ''} — ${(st.lines || []).length} lines`, [
          { label: 'Date', cell: (l) => l.booking_date }, { label: 'Reference', cell: (l) => l.reference }, { label: 'Counterparty', cell: (l) => l.counterparty },
          { label: 'Amount', num: true, cell: (l) => money(l.amount) }], st.lines || []));
        if (st.company_bank_id && !banks.some((b) => b.id === st.company_bank_id)) showError(new Error('The file names a bank account that is not an active company account.'));
      } catch { showError(new Error('The file is not valid JSON.')); raw = null; }
    });
    const btn = h('button', { type: 'button' }, 'Import statement');
    btn.addEventListener('click', async () => {
      clearError();
      if (!raw) { showError(new Error('Choose a statement file first.')); file.focus(); return; }
      btn.disabled = true;
      try {
        const res = await api('POST', '/v1/bank-statements', undefined, raw);
        say('Statement imported.');
        await renderBank();
        $('view').insertBefore(h('p', { class: 'notice', role: 'status' }, `Statement imported (${res.id.slice(0, 8)}). The file’s SHA-256 digest is recorded with the import.`), $('view').children[1]);
      } catch (e) { btn.disabled = false; showError(e); }
    });
    parts.push(h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Import a statement'),
      h('p', { class: 'hint' }, 'Upload the bank’s statement file in SERK JSON form (company_bank_id, statement_no, statement_date, opening_balance, closing_balance, lines[booking_date, amount, reference, counterparty]). The exact bytes are hashed and recorded.'),
      field('Statement file', file), preview, h('div', { class: 'actions' }, btn)));
  }
  const sts = (await api('GET', '/v1/p2p/bank_statement')).items;
  parts.push(table(`Statements (${sts.length})`, [
    { label: 'Statement', cell: (s) => s.number }, { label: 'Account', cell: (s) => s.label }, { label: 'Status', cell: (s) => chip(s.status) },
    { label: 'Closing balance', num: true, cell: (s) => money(s.amount) }, { label: 'Imported by', cell: (s) => s.created_by },
    { label: 'Action', cell: (s) => (S.can.has('bank.reconcile') && s.status === 'OPEN' ? reconcileButton(s) : '') },
  ], sts, 'No statements imported yet.'));
  parts.push(h('div', { class: 'toolbar' }, h('span', { class: 'spacer' }), exportBar('/v1/p2p/bank_statement', 'Bank statements')));
  page('Bank statements', 'Import statements and reconcile them automatically against released payments. Unmatched lines are left for investigation, never forced.', ...parts);
}
function reconcileButton(s) {
  const b = h('button', { type: 'button', 'aria-label': `Reconcile statement ${s.number}` }, 'Reconcile');
  b.addEventListener('click', async () => {
    clearError();
    b.disabled = true;
    try {
      const r = await api('POST', `/v1/bank-statements/${s.id}/reconcile`);
      await renderBank();
      const v = $('view');
      v.insertBefore(h('div', { class: 'panel', role: 'status' }, h('p', { class: 'notice' }, `Statement ${s.number}: ${r.matched.length} matched, ${r.unmatched.length} left for investigation. Settlement journals are drafts for finance to approve.`),
        r.unmatched.length ? table('Unmatched lines', [{ label: 'Line', cell: (u) => u.line_no }, { label: 'Amount', num: true, cell: (u) => money(u.amount) },
          { label: 'Reason', cell: (u) => u.reason || '' }], r.unmatched) : null), v.children[1]);
      say(`${r.matched.length} matched, ${r.unmatched.length} unmatched.`);
    } catch (e) { b.disabled = false; showError(e); }
  });
  return b;
}

// ---------- reports ----------
async function renderRecon() {
  const ent = select('legal_entity_id', entityOpts());
  const out = h('div');
  const load = async () => {
    out.textContent = '';
    if (!ent.value) return;
    const checks = (await api('GET', `/v1/entities/${ent.value}/p2p-reconciliation`)).checks || [];
    for (const c of checks) c._class = c.difference !== '0' ? 'diff' : null;
    const breaks = checks.filter((c) => c._class).length;
    out.append(h('p', { class: breaks ? 'alert' : 'notice', role: 'status' }, breaks ? `${breaks} reconciliation difference(s) need investigation.` : 'Every subledger agrees with the general ledger.'),
      table('Subledger to general ledger', [
        { label: 'Check', cell: (c) => c.check }, { label: 'Subledger', num: true, cell: (c) => money(c.subledger) },
        { label: 'General ledger', num: true, cell: (c) => money(c.gl) }, { label: 'Difference', num: true, cell: (c) => money(c.difference) },
        { label: 'Unposted / unmatched', num: true, cell: (c) => String(c.unposted_or_unmatched) }], checks));
    const ar = (await api('GET', `/v1/entities/${ent.value}/ar-reconciliation`)).checks || [];
    for (const c of ar) c._class = c.difference !== '0' && !c.check.startsWith('Unapplied') ? 'diff' : null;
    if (ar.length) out.append(table('Receivables to general ledger', [
      { label: 'Check', cell: (c) => c.check }, { label: 'Subledger', num: true, cell: (c) => money(c.subledger) },
      { label: 'General ledger', num: true, cell: (c) => (c.check.startsWith('Unapplied') ? '' : money(c.gl)) },
      { label: 'Difference', num: true, cell: (c) => (c.check.startsWith('Unapplied') ? '' : money(c.difference)) },
      { label: 'Unposted / unapplied', num: true, cell: (c) => String(c.unposted_or_unmatched) }], ar));
    const stk = (await api('GET', `/v1/entities/${ent.value}/stock-reconciliation`)).checks || [];
    for (const c of stk) c._class = c.difference !== '0' && !c.check.startsWith('Received') ? 'diff' : null;
    if (stk.length) out.append(table('Inventory to general ledger', [
      { label: 'Check', cell: (c) => c.check }, { label: 'Subledger', num: true, cell: (c) => money(c.subledger) },
      { label: 'General ledger', num: true, cell: (c) => (c.check.startsWith('Received') ? '' : money(c.gl)) },
      { label: 'Difference', num: true, cell: (c) => (c.check.startsWith('Received') ? '' : money(c.difference)) },
      { label: 'Unposted / open', num: true, cell: (c) => String(c.unposted_or_unmatched) }], stk));
    const arBreaks = ar.filter((c) => c._class).length + stk.filter((c) => c._class).length;
    if (arBreaks) out.prepend(h('p', { class: 'alert', role: 'status' }, `${arBreaks} receivables or inventory difference(s) need investigation.`));
  };
  ent.addEventListener('change', () => load().catch(showError));
  const entName = () => (S.ref.legal_entities.find((e) => e.id === ent.value) || {}).name || '';
  const rx = (path, t) => exportBar(() => `/v1/entities/${ent.value}/${path}`, () => `${t} — ${entName()}`, { entity: () => ent.value });
  page('Reconciliation', 'GRNI, payables, payments in transit, bank, receivables, retention and advances compared with posted general-ledger balances.',
    h('div', { class: 'toolbar' }, field('Legal entity', ent), h('span', { class: 'spacer' }),
      h('div', { class: 'inline' }, rx('p2p-reconciliation', 'Payables reconciliation'), rx('ar-reconciliation', 'Receivables reconciliation'), rx('stock-reconciliation', 'Inventory reconciliation'))), out);
  await load();
}

// ---------- financial control dashboard (WP-1.4; formulas in docs/reports/KPI_CATALOGUE.md) ----------
// Each tile shows one KPI with its level, what it is made of, the largest
// contributors (each linking to its record) and a link to the page holding
// the rows behind it. "How is this calculated?" shows the formula and source.
const LEVEL_WORD = { ok: 'On track', watch: 'Watch', alert: 'Needs action', unknown: 'No data' };
async function renderDashboard(preset) {
  preset = ['executive', 'project', 'finance', 'procurement'].includes(preset) ? preset : 'executive';
  const params = new URLSearchParams(location.hash.split('?')[1] || '');
  const entity = params.get('entity') || '';
  const region = params.get('region') || '';
  const unitF = params.get('unit') || '';
  const q = `?preset=${preset}${entity ? '&entity_id=' + encodeURIComponent(entity) : ''}${region ? '&region_id=' + region : ''}${unitF ? '&business_unit_id=' + unitF : ''}`;
  const d = await api('GET', '/v1/dashboard' + q);
  const view = (name) => h('a', { class: 'btn' + (name === preset ? ' primary' : ' ghost'), href: `#/dashboard/${name}${entity || region || unitF ? '?entity=' + entity + (region ? '&region=' + region : '') + (unitF ? '&unit=' + unitF : '') : ''}`, 'aria-current': name === preset ? 'true' : null },
    { executive: 'Executive', project: 'Project', finance: 'Finance', procurement: 'Procurement' }[name]);
  const ent = select('entity', d.entities.map((e) => ({ value: e.id, label: `${e.name} (${e.currency})` })));
  ent.value = d.entity_id;
  const [rg, bu] = await Promise.all([api('GET', '/v1/org/regions').catch(() => ({ items: [] })), api('GET', '/v1/org/business-units').catch(() => ({ items: [] }))]);
  const regSel = select('region', [{ value: '', label: 'All regions' }, ...rg.items.map((x) => ({ value: x.id, label: x.name }))]);
  const unitSel = select('unit', [{ value: '', label: 'All business units' }, ...bu.items.map((x) => ({ value: x.id, label: x.name }))]);
  regSel.value = region; unitSel.value = unitF;
  const refilter = () => { location.hash = `#/dashboard/${preset}?entity=${ent.value}${regSel.value ? '&region=' + regSel.value : ''}${unitSel.value ? '&unit=' + unitSel.value : ''}`; };
  for (const el of [ent, regSel, unitSel]) el.addEventListener('change', refilter);
  const money_ = (t) => (t.unit === '%' ? `${t.value} %` : t.unit === 'count' ? t.value : t.unit ? `${t.unit} ${money(t.value)}` : t.value);
  const grid = h('div', { class: 'tiles' }, d.tiles.map((t) => h('section', { class: `tile lvl-${t.level}`, 'aria-label': t.title },
    h('div', { class: 'tilehead' }, h('h2', {}, t.title), h('span', { class: `lvl lvl-${t.level}` }, LEVEL_WORD[t.level] || t.level)),
    h('p', { class: 'big' }, money_(t)),
    t.note ? h('p', { class: 'hint' }, t.note) : null,
    t.detail && t.detail.length ? h('dl', { class: 'detail' }, t.detail.map((x) => [h('dt', {}, x.label), h('dd', {}, /^-?[\d,.]+$/.test(x.value) ? money(x.value) : x.value)])) : null,
    t.evidence && t.evidence.length ? h('ul', { class: 'evidence' }, t.evidence.map((e) => h('li', {}, h('a', { href: e.href }, e.label)))) : null,
    h('p', { class: 'tilefoot' }, h('a', { href: t.drill }, 'See the rows behind this number')),
    h('details', { class: 'how' }, h('summary', {}, 'How is this calculated?'), h('p', {}, t.formula), h('p', { class: 'hint' }, `${t.id}. Source: ${t.source}. Computed ${when(t.as_of)}.`)))));
  const hidden = d.hidden && d.hidden.length
    ? h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Not shown to your role'),
      h('ul', {}, d.hidden.map((x) => h('li', {}, `${x.title}: needs ${x.needs}.`))))
    : null;
  page('Dashboard', `${d.entity || 'Your company'}${d.currency ? ' (' + d.currency + ')' : ''}: ${d.projects_in_scope} project(s) in your scope. Every figure is computed now from approved and posted records.`,
    h('div', { class: 'toolbar' }, view('executive'), view('project'), view('finance'), view('procurement'), d.entities.length > 1 ? field('Legal entity', ent) : null, rg.items.length ? field('Region', regSel) : null, bu.items.length ? field('Business unit', unitSel) : null), grid, hidden);
}

// Shared fields of the project forms (create and edit).
function projectFields(pr, regions, units, customers) {
  const v = (k) => (pr && pr[k]) || '';
  const entity = select('legal_entity_id', entityOpts(), { required: true });
  const code = input('code', { type: 'text', required: true, maxlength: '20', value: v('code') });
  const name = input('name', { type: 'text', required: true, minlength: '3', maxlength: '200', value: v('name') });
  const none = (label) => [{ value: '', label }];
  const customer = select('customer_id', [...none('No client yet'), ...customers]);
  const region = select('region_id', [...none('No region'), ...regions.map((r) => ({ value: r.id, label: `${r.code} ${r.name}` }))]);
  const unit = select('business_unit_id', [...none('No business unit'), ...units.map((r) => ({ value: r.id, label: `${r.code} ${r.name}` }))]);
  const type = select('contract_type', [...none('Not set'), ...['LUMP_SUM', 'REMEASURE', 'COST_PLUS', 'DESIGN_BUILD', 'FRAMEWORK', 'OTHER'].map((t) => ({ value: t, label: words(t.toLowerCase()) }))]);
  const lat = input('latitude', { inputmode: 'decimal', placeholder: 'for example 13.4967', value: pr && pr.latitude ? String(pr.latitude) : '' });
  const lng = input('longitude', { inputmode: 'decimal', placeholder: 'for example 39.4753', value: pr && pr.longitude ? String(pr.longitude) : '' });
  const start = input('start_date', { type: 'date', value: v('start_date') });
  const finish = input('planned_finish', { type: 'date', value: v('planned_finish') });
  const pm = input('project_manager', { type: 'text', maxlength: '200', value: v('project_manager'), placeholder: 'their sign-in name' });
  const desc = input('description', { type: 'text', maxlength: '1000', value: v('description') });
  region.value = v('region_id'); unit.value = v('business_unit_id'); customer.value = v('customer_id'); type.value = v('contract_type');
  const grid = h('div', { class: 'grid' }, pr ? null : field('Company', entity), pr ? null : field('Project code', code), field('Name', name), field('Client', customer), field('Region', region),
    field('Business unit', unit), field('Contract type', type), field('Latitude', lat), field('Longitude', lng), field('Start date', start), field('Planned finish', finish),
    field('Project manager', pm), field('Description', desc));
  const body = () => ({ legal_entity_id: entity.value || undefined, code: code.value.trim(), name: name.value.trim(), customer_id: customer.value, region_id: region.value, business_unit_id: unit.value,
    contract_type: type.value, latitude: lat.value.trim() || undefined, longitude: lng.value.trim() || undefined, start_date: start.value, planned_finish: finish.value,
    project_manager: pm.value.trim(), description: desc.value.trim() });
  return { grid, body };
}

// ---------- projects, organisation and calendar (WP-1.7) ----------
async function renderProjects() {
  const r = await api('GET', '/v1/projects');
  const next = { BID: 'Mobilisation', MOBILISATION: 'Execution', EXECUTION: 'Practical completion', PRACTICAL_COMPLETION: 'Defects period', DEFECTS: 'Closed' };
  page('Projects', 'Every project you may see, with its client, region, contract type, manager, dates and phase. The phase moves only through a stage gate.',
    h('div', { class: 'toolbar' }, S.can.has('project.create') ? h('a', { class: 'btn primary', href: '#/new/project' }, 'New project') : null),
    table('Projects', [
      { label: 'Code', cell: (x) => h('span', { class: 'ref' }, x.code) }, { label: 'Name', cell: (x) => x.name },
      { label: 'Client', cell: (x) => x.customer }, { label: 'Region', cell: (x) => x.region }, { label: 'Unit', cell: (x) => x.business_unit },
      { label: 'Contract', cell: (x) => words((x.contract_type || '').toLowerCase()) }, { label: 'Manager', cell: (x) => x.project_manager },
      { label: 'Start', cell: (x) => x.start_date }, { label: 'Finish', cell: (x) => x.planned_finish },
      { label: 'Phase', cell: (x) => chip(x.phase) },
      { label: '', cell: (x) => h('span', {},
        S.can.has('project.update') ? h('a', { href: `#/new/projectedit/${x.id}` }, 'Edit') : null, ' ',
        x.open_gate_id ? h('a', { href: `#/doc/project_stage_gate/${x.open_gate_id}` }, `Gate to ${words(x.open_gate_to.toLowerCase())} (${x.open_gate_status.toLowerCase()})`)
          : (S.can.has('project.gate.submit') && next[x.phase] ? h('a', { href: `#/new/stagegate/${x.id}` }, `Request ${next[x.phase].toLowerCase()}`) : null)) },
    ], r.projects, 'No projects in your scope.'));
}

async function renderOrg() {
  const [regions, units, cal] = await Promise.all([api('GET', '/v1/org/regions'), api('GET', '/v1/org/business-units'), api('GET', '/v1/org/calendar')]);
  const can = S.can.has('org.manage');
  const addBox = (kind, path, label) => {
    const code = input('code', { type: 'text', required: true, maxlength: '20', placeholder: 'code' });
    const name = input('name', { type: 'text', required: true, maxlength: '120', placeholder: `${label} name` });
    const b = h('button', { type: 'button', on: { click: async () => {
      try { await api('POST', path, { Code: code.value.trim(), Name: name.value.trim() }); say(`${label} added.`); await renderOrg(); } catch (e) { showError(e); }
    } } }, `Add ${label.toLowerCase()}`);
    return can ? h('div', { class: 'toolbar' }, field('Code', code), field('Name', name), b) : null;
  };
  const day = input('day', { type: 'date', required: true });
  const dname = input('name', { type: 'text', required: true, maxlength: '120', placeholder: 'for example Ethiopian Christmas (Genna)' });
  const kind = select('kind', [{ value: 'HOLIDAY', label: 'Holiday (not worked)' }, { value: 'WORKING', label: 'Exceptional working day' }]);
  const addDay = can ? h('div', { class: 'toolbar' }, field('Day', day), field('Name', dname), field('Kind', kind), h('button', { type: 'button', on: { click: async () => {
    try { await api('POST', '/v1/org/calendar', { Day: day.value, Name: dname.value.trim(), Kind: kind.value }); say('Day added.'); await renderOrg(); } catch (e) { showError(e); }
  } } }, 'Add day')) : null;
  page('Organisation and calendar', 'Regions and business units are used to roll results up on the dashboard and in reports. The calendar lists company holidays and exceptional working days.',
    table('Regions', [{ label: 'Code', cell: (x) => x.code }, { label: 'Name', cell: (x) => x.name }], regions.items, 'No regions yet.'), addBox('region', '/v1/org/regions', 'Region'),
    table('Business units', [{ label: 'Code', cell: (x) => x.code }, { label: 'Name', cell: (x) => x.name }], units.items, 'No business units yet.'), addBox('business_unit', '/v1/org/business-units', 'Business unit'),
    table('Company calendar', [{ label: 'Day', cell: (x) => x.day }, { label: 'Name', cell: (x) => x.name }, { label: 'Kind', cell: (x) => (x.kind === 'HOLIDAY' ? 'Holiday' : 'Worked') },
      { label: '', cell: (x) => (can ? h('button', { type: 'button', class: 'ghost', on: { click: async () => { try { await api('DELETE', `/v1/org/calendar/${x.id}`); await renderOrg(); } catch (e) { showError(e); } } } }, 'Remove') : '') }], cal.days, 'No days entered.'),
    addDay, h('p', { class: 'hint' }, 'Movable holidays (for example Easter and the Islamic holidays) fall on different dates each year, so enter them for the year ahead; nothing is assumed.'));
}

// ---------- company setup checklist and staged imports (WP-1.8) ----------
// The checklist reads the real state of the records (nothing is ticked by hand).
async function renderSetup() {
  const steps = (await api('GET', '/v1/setup')).steps;
  const required = steps.filter((x) => !x.optional);
  const done = required.filter((x) => x.done).length;
  const groups = [...new Set(steps.map((x) => x.group))];
  const pct = Math.round((100 * done) / required.length);
  page('Company setup', 'What a new company needs in place before it can run a project. Each step is ticked automatically when the records exist; each says who does it and links to the page that does.',
    h('div', { class: 'panel' }, h('p', { class: 'big' }, `${done} of ${required.length} required steps done`),
      h('progress', { class: 'progress', value: String(done), max: String(required.length), 'aria-label': 'Setup progress' }, `${pct}%`)),
    ...groups.map((g) => h('section', {}, h('h2', { class: 'section' }, g),
      h('ul', { class: 'steps' }, steps.filter((x) => x.group === g).map((x) => h('li', { class: x.done ? 'done' : '' },
        h('span', { class: 'mark', 'aria-label': x.done ? 'Done' : 'Not done' }, x.done ? '✓' : '○'),
        h('div', {}, h('a', { href: x.href }, x.title), x.optional ? h('span', { class: 'hint' }, ' (optional)') : null,
          h('p', { class: 'hint' }, x.why), h('p', { class: 'hint' }, `Who: ${x.who}.${x.count ? ` ${x.count} on record.` : ''}`))))))));
}

async function renderImports(batchID) {
  if (batchID) return renderImportBatch(batchID);
  const list = (await api('GET', '/v1/imports')).imports;
  const kinds = [['supplier', 'Suppliers', 'supplier.manage'], ['customer', 'Customers', 'customer.manage'], ['stock_item', 'Stock items', 'stock.item.manage']].filter((k) => S.can.has(k[2]));
  const kind = select('kind', kinds.map(([value, label]) => ({ value, label })), { required: true });
  const ent = select('legal_entity_id', entityOpts(), { required: true });
  const file = input('file', { type: 'file', accept: '.csv,text/csv' });
  const text = h('textarea', { name: 'content', rows: '8', spellcheck: 'false', placeholder: 'Or paste CSV here, header row first' });
  const tpl = h('button', { type: 'button', class: 'ghost', on: { click: async () => {
    const t = await api('GET', `/v1/imports/templates/${kind.value}`);
    text.value = t.csv;
    say(t.help);
    help.textContent = t.help;
  } } }, 'Fill in the template');
  const help = h('p', { class: 'hint' }, 'Choose what you are importing, then fill in the template or choose your file. Nothing is created until a second person commits it.');
  file.addEventListener('change', async () => { if (file.files[0]) text.value = await file.files[0].text(); });
  const form = h('form', { class: 'panel', novalidate: true, on: { submit: async (ev) => {
    ev.preventDefault(); clearError();
    try {
      const res = await api('POST', '/v1/imports', { kind: kind.value, legal_entity_id: ent.value, filename: (file.files[0] && file.files[0].name) || `${kind.value}.csv`, content: text.value });
      say('File staged: review it before it is committed.');
      location.hash = `#/imports/${res.id}`;
    } catch (e) { showError(e); }
  } } }, h('div', { class: 'grid' }, field('What to import', kind), field('Company', ent), field('CSV file', file)), field('Contents', text), help,
    h('div', { class: 'actions' }, tpl, h('button', { type: 'submit' }, 'Stage for review')));
  page('Data imports', 'Load suppliers, customers or stock items from a spreadsheet saved as CSV. The file is checked row by row first; a different person then commits it, confirming the number of rows in the source. Records still go through their normal approval.',
    kinds.length ? form : h('p', { class: 'hint' }, 'You do not hold a permission to import data.'),
    table('Staged and past imports', [
      { label: 'File', cell: (x) => h('a', { href: `#/imports/${x.id}` }, x.filename) }, { label: 'Kind', cell: (x) => words(x.kind) },
      { label: 'Rows', num: true, cell: (x) => String(x.row_count) }, { label: 'Valid', num: true, cell: (x) => String(x.valid_count) }, { label: 'Errors', num: true, cell: (x) => String(x.invalid_count) },
      { label: 'Staged by', cell: (x) => x.created_by }, { label: 'Status', cell: (x) => chip(x.status) }], list, 'No imports yet.'));
}

async function renderImportBatch(id) {
  const b = await api('GET', `/v1/imports/${id}`);
  const cols = Object.keys((b.rows[0] || {}).data || {});
  const open = b.status === 'STAGED' || b.status === 'PARTIAL';
  const mine = b.created_by === S.me.user_id;
  const expected = input('expected_rows', { inputmode: 'numeric', required: true, placeholder: 'rows in your source file' });
  const reason = input('reason', { type: 'text', maxlength: '300', placeholder: 'why discard' });
  const commit = h('form', { class: 'panel', novalidate: true, on: { submit: async (ev) => {
    ev.preventDefault(); clearError();
    try { await api('POST', `/v1/imports/${id}/commit`, { expected_rows: Number(expected.value) }); say('Import committed.'); await renderImportBatch(id); } catch (e) { showError(e); }
  } } }, h('p', {}, mine ? 'You staged this file, so someone else must commit it.' : 'Count the data rows in the original source (not this screen) and enter that number. It must match the staged file.'),
    h('div', { class: 'grid' }, field('Rows in the source', expected)), h('div', { class: 'actions' }, h('button', { type: 'submit', disabled: mine || b.valid_count === 0 }, `Create the ${b.valid_count} valid record(s)`)));
  const discard = h('form', { class: 'panel', novalidate: true, on: { submit: async (ev) => {
    ev.preventDefault(); clearError();
    try { await api('POST', `/v1/imports/${id}/discard`, { reason: reason.value.trim() }); say('Import discarded.'); await renderImportBatch(id); } catch (e) { showError(e); }
  } } }, h('div', { class: 'grid' }, field('Reason', reason)), h('div', { class: 'actions' }, h('button', { type: 'submit', class: 'ghost' }, 'Discard this import')));
  page(`Import: ${b.filename}`, `${words(b.kind)}: ${b.row_count} row(s), ${b.valid_count} valid, ${b.invalid_count} with errors. Status ${b.status.toLowerCase()}. Staged by ${b.created_by}${b.committed_by ? `, committed by ${b.committed_by}` : ''}. Nothing is created until it is committed, and rows with errors are never created.`,
    table('Rows', [{ label: 'Line', num: true, cell: (r) => String(r.line) }, ...cols.map((c) => ({ label: words(c), cell: (r) => r.data[c] })),
      { label: 'Result', cell: (r) => (r.errors && r.errors.length ? h('span', { class: 'bad' }, r.errors.join('; ')) : r.status === 'CREATED' ? 'Created' : r.status === 'FAILED' ? h('span', { class: 'bad' }, r.error) : 'Valid') }], b.rows, 'No rows.'),
    open ? commit : null, open ? discard : null, h('p', {}, h('a', { href: '#/imports' }, 'All imports')));
}

// ---------- stock reservations (WP-1.6) ----------
async function renderReservations() {
  const status = select('status', [{ value: '', label: 'All' }, { value: 'ACTIVE', label: 'Active' }, { value: 'CONSUMED', label: 'Used by an issue' }, { value: 'RELEASED', label: 'Released' }]);
  status.value = 'ACTIVE';
  const out = h('div');
  const load = async () => {
    out.textContent = '';
    const r = (await api('GET', '/v1/stock/reservations?status=' + status.value)).reservations || [];
    out.append(table('Stock reservations', [
      { label: 'Item', cell: (x) => x.item_code }, { label: 'Store', cell: (x) => x.warehouse },
      { label: 'Quantity', num: true, cell: (x) => x.quantity }, { label: 'Purpose', cell: (x) => x.purpose },
      { label: 'Keep until', cell: (x) => when(x.expires_at) }, { label: 'By', cell: (x) => x.created_by },
      { label: 'Status', cell: (x) => chip(x.status) },
      { label: '', cell: (x) => (x.status === 'ACTIVE' ? h('button', { type: 'button', class: 'ghost', on: { click: async () => {
        const why = prompt('Why is this reservation released?');
        if (!why) return;
        try { await api('POST', `/v1/stock/reservations/${x.id}/release`, { reason: why }); say('Reservation released.'); await load(); } catch (e) { showError(e); }
      } } }, 'Release') : '') },
    ], r, 'No reservations.'));
  };
  status.addEventListener('change', () => load().catch(showError));
  page('Stock reservations', 'Material set aside for projects. Reserved stock cannot be issued to anyone else.',
    h('div', { class: 'toolbar' }, field('Show', status),
      S.can.has('stock.reserve') ? h('a', { class: 'btn primary', href: '#/new/reservation' }, 'Reserve stock') : null,
      S.can.has('stock.item.manage') ? h('a', { class: 'btn ghost', href: '#/new/reorderrule' }, 'Reorder rule') : null), out);
  await load();
}

async function renderAuthority() {
  const out = h('div');
  const cur = (await api('GET', '/v1/authority/matrix')).matrix;
  const lent = (await api('GET', '/v1/delegations')).delegations || [];
  const lines = (cur && cur.rules) || [];
  out.append(cur
    ? table(`Matrix in force: version ${cur.version}, approved by ${cur.approved_by}`, [
      { label: 'Document', cell: (r) => words(r.doc_type) },
      { label: 'From', num: true, cell: (r) => money(r.min_amount) },
      { label: 'Below', num: true, cell: (r) => (r.max_amount ? money(r.max_amount) : 'no limit') },
      { label: 'Currency', cell: (r) => r.currency },
      { label: 'Needs permission', cell: (r) => r.required_perm },
      { label: 'Fresh MFA', cell: (r) => (r.requires_mfa ? 'yes' : '') },
    ], lines, 'No rules.')
    : h('div', { class: 'panel' }, h('p', {}, 'No authority matrix is in force. Approvals are limited only by roles.')));
  out.append(table('Delegations you lent or received', [
    { label: 'From', cell: (r) => r.delegator }, { label: 'To', cell: (r) => r.delegate },
    { label: 'Permissions', cell: (r) => r.perms.join(', ') },
    { label: 'From date', cell: (r) => when(r.valid_from) }, { label: 'Until', cell: (r) => when(r.valid_to) },
    { label: 'Status', cell: (r) => chip(r.status) },
    { label: '', cell: (r) => h('a', { href: `#/doc/delegation/${r.id}` }, 'Open') },
  ], lent, 'No delegations.'));
  let conflicts = null;
  // Only people who may read the report ask for it (avoids a refused request).
  if (S.can.has('authority.manage') || S.can.has('authority.approve') || S.can.has('audit.read')) {
    try { conflicts = (await api('GET', '/v1/sod/report')).conflicts || []; } catch { conflicts = null; }
  }
  if (conflicts) {
    out.append(table('Duty conflicts in the role catalogue', [
      { label: 'Role', cell: (r) => r.subject }, { label: 'Control', cell: (r) => r.control },
      { label: 'Permissions held together', cell: (r) => `${r.perm_a} + ${r.perm_b}` },
    ], conflicts, 'None. No role holds permissions that should be kept apart.'));
  }
  const actions = h('div', { class: 'toolbar' },
    S.can.has('authority.manage') ? h('a', { class: 'btn primary', href: '#/new/authority_matrix' }, 'New matrix version') : null,
    S.can.has('delegation.create') ? h('a', { class: 'btn', href: '#/new/delegation' }, 'Delegate my authority') : null,
    S.can.has('authority.manage') ? h('a', { class: 'btn ghost', href: '#/new/sod_exception' }, 'Duty-conflict exception') : null);
  page('Authority and duties', 'Who may approve how much, who has lent authority to whom, and where duties must be kept apart.', actions, out);
}

async function renderBudget() {
  const proj = select('project_id', projectOpts());
  const out = h('div');
  const reqBtn = S.can.has('budget.transfer') ? h('a', { class: 'btn primary', href: '#/new/budget_transfer' }, 'Request transfer') : null;
  const revBtn = S.can.has('budget.revise') ? h('a', { class: 'btn', href: '#/new/budget_revision' }, 'New revision') : null;
  const drwBtn = S.can.has('budget.revise') ? h('a', { class: 'btn ghost', href: '#/new/contingency_drawdown' }, 'Draw down contingency') : null;
  const load = async () => {
    out.textContent = '';
    if (!proj.value) return;
    const res = await api('GET', `/v1/projects/${proj.value}/budget`);
    const rows = res.cost_codes || [];
    out.append(table('Budget overview by cost code', [
      { label: 'Cost code', cell: (r) => (r.is_contingency ? `${r.cost_code} (contingency)` : r.cost_code) },
      { label: 'Original', num: true, cell: (r) => money(r.original) },
      { label: 'Revisions', num: true, cell: (r) => money(r.approved_revisions) },
      { label: 'Transfers in', num: true, cell: (r) => money(r.transfers_in) },
      { label: 'Transfers out', num: true, cell: (r) => money(r.transfers_out) },
      { label: 'Drawdowns in', num: true, cell: (r) => money(r.drawdowns_in) },
      { label: 'Drawdowns out', num: true, cell: (r) => money(r.drawdowns_out) },
      { label: 'Current budget', num: true, cell: (r) => money(r.current) },
    ], rows, 'No budgeted cost codes.'));
  };
  proj.addEventListener('change', () => load().catch(showError));
  page('Project budget', 'Original budget, approved revisions, inter-code transfers and drawdowns.',
    h('div', { class: 'toolbar' }, field('Project', proj), reqBtn, revBtn, drwBtn), out);
  await load();
}

async function renderCost(projectID) {
  const proj = select('project_id', projectOpts());
  if (projectID && [...proj.options].some((o) => o.value === projectID)) proj.value = projectID; // #/cost/<project> from a dashboard drill-down
  const out = h('div');
  const load = async () => {
    out.textContent = '';
    if (!proj.value) return;
    const rows = (await api('GET', `/v1/projects/${proj.value}/cost-report`)).cost_codes;
    out.append(table('Budget, commitments and actuals by cost code', [
      { label: 'Cost code', cell: (r) => r.cost_code },
      { label: 'Original', num: true, cell: (r) => money(r.original || r.budget) },
      { label: 'Current budget', num: true, cell: (r) => money(r.current || r.budget) },
      { label: 'Committed', num: true, cell: (r) => money(r.committed) },
      { label: 'Reserved', num: true, cell: (r) => money(r.reserved) },
      { label: 'Received', num: true, cell: (r) => money(r.received) },
      { label: 'Invoiced', num: true, cell: (r) => money(r.invoiced) },
      { label: 'Actual (posted)', num: true, cell: (r) => money(r.actual_posted) },
      { label: 'Available', num: true, cell: (r) => money(r.available) }], rows, 'No budgeted cost codes.'));
  };
  proj.addEventListener('change', () => load().catch(showError));
  const projName = () => { const p = S.ref.projects.find((x) => x.id === proj.value); return p ? `${p.code} ${p.name}` : ''; };
  page('Project cost report', 'Approved BOQ budget against commitments (approved POs), receipts, invoices and posted actuals.',
    h('div', { class: 'toolbar' }, field('Project', proj), h('span', { class: 'spacer' }), exportBar(() => `/v1/projects/${proj.value}/cost-report`, () => `Project cost report — ${projName()}`)), out);
  await load();
}

// ---------- earned value ----------
async function renderEVM() {
  const proj = select('project_id', projectOpts());
  const at = input('status_date', { type: 'date', value: today() });
  const out = h('div');
  let seq = 0;
  const load = async () => {
    const mine = ++seq;
    out.textContent = '';
    if (!proj.value || !at.value) return;
    let r;
    try { r = await api('GET', `/v1/projects/${proj.value}/evm?status_date=${at.value}`); } catch (e) {
      if (mine !== seq) return;
      if (e.status === 404) { out.replaceChildren(h('p', { class: 'empty' }, 'The project has no baseline schedule.')); return; }
      throw e;
    }
    if (mine !== seq) return;
    out.textContent = '';
    const ratio = (x) => (x == null ? 'not defined' : Number(x).toFixed(4));
    out.append(table(`Earned value at ${at.value}${r.progress_date ? ' (progress reported ' + r.progress_date.slice(0, 10) + ')' : ' (no approved progress)'}`, [
      { label: 'Measure', cell: (x) => x[0] }, { label: 'Value', num: true, cell: (x) => x[1] }], [
      ['Budget at completion (BAC)', money(r.bac)], ['Planned value (PV)', money(r.pv)], ['Earned value (EV)', money(r.ev)],
      ['Actual cost (AC, posted)', money(r.ac)], ['Schedule variance (EV - PV)', money(r.sv)], ['Cost variance (EV - AC)', money(r.cv)],
      ['Schedule performance index (EV / PV)', ratio(r.spi)], ['Cost performance index (EV / AC)', ratio(r.cpi)]]));
  };
  for (const el of [proj, at]) el.addEventListener('change', () => load().catch(showError));
  page('Earned value', 'From the approved baseline (PV spread over each activity\'s working days), the latest approved progress (EV) and the posted ledger cost on the project (AC).',
    h('div', { class: 'toolbar' }, field('Project', proj), field('Status date', at), h('span', { class: 'spacer' }),
      exportBar(() => `/v1/projects/${proj.value}/evm?status_date=${at.value}`, () => { const p = S.ref.projects.find((x) => x.id === proj.value); return `Earned value — ${p ? p.code + ' ' + p.name : ''} at ${at.value}`; })), out);
  await load();
}

// ---------- field devices ----------
async function renderDevices() {
  const r = await api('GET', '/v1/site/devices');
  const out = h('div', { 'aria-live': 'polite' });
  const label = input('label', { type: 'text', maxlength: '100', required: true });
  const reg = h('form', { class: 'panel', novalidate: true }, field('Device name', label, 'Registering a device needs a recent multi-factor sign-in. Its key is shown once.'),
    h('div', { class: 'actions' }, h('button', { type: 'submit' }, 'Register a device')));
  reg.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    clearError();
    if (!label.value.trim()) { showError(new Error('Name the device.')); label.focus(); return; }
    try {
      const d = await api('POST', '/v1/site/devices', { label: label.value.trim() });
      out.replaceChildren(h('div', { class: 'notice', role: 'status' }, h('p', {}, `Device registered. Enter this key in the field app now; it is not shown again.`),
        h('pre', { class: 'secret' }, `device id  ${d.id}\ndevice key ${d.secret}`)));
      label.value = '';
    } catch (e) { showError(e); }
  });
  const revoke = (d) => {
    if (d.status !== 'ACTIVE') return '';
    const reason = input('reason', { type: 'text', maxlength: '500', 'aria-label': `Reason for revoking ${d.label}` });
    const btn = h('button', { type: 'button', class: 'danger' }, 'Revoke');
    btn.addEventListener('click', async () => {
      clearError();
      if (reason.value.trim().length < 5) { showError(new Error('Give a reason of at least 5 characters.')); reason.focus(); return; }
      btn.disabled = true;
      try { await api('POST', `/v1/site/devices/${d.id}/revoke`, { reason: reason.value.trim() }); say(`${d.label} revoked.`); await renderDevices(); } catch (e) { btn.disabled = false; showError(e); }
    });
    return h('span', { class: 'inline' }, reason, btn);
  };
  page('Field devices', 'Devices that may synchronise site records prepared offline. A revoked device can never synchronise again, including records it queued before the revocation.',
    h('div', { class: 'toolbar' }, h('span', { class: 'spacer' }), exportBar('/v1/site/devices', 'Field devices')),
    S.can.has('site.device.register') ? reg : null, out,
    table(`Devices (${r.items.length})`, [
      { label: 'Device', cell: (d) => d.label }, { label: 'Person', cell: (d) => d.user_id }, { label: 'Status', cell: (d) => chip(d.status) },
      { label: 'Last sync', cell: (d) => (d.last_sync_at ? when(d.last_sync_at) : 'never') }, { label: 'Commands', num: true, cell: (d) => String(d.last_seq) },
      { label: 'Registered', cell: (d) => when(d.registered_at) }, { label: 'Action', cell: revoke }], r.items, 'No devices registered.'));
}

// ---------- stock ----------
async function renderStock() {
  const ent = select('legal_entity_id', entityOpts());
  const out = h('div');
  const load = async () => {
    out.textContent = '';
    if (!ent.value) return;
    const lines = (await api('GET', `/v1/entities/${ent.value}/stock-on-hand`)).lines || [];
    out.append(table(`Stock on hand (${lines.length})`, [
      { label: 'Item', cell: (l) => `${l.item_code} ${l.item_name}` }, { label: 'Warehouse', cell: (l) => l.warehouse_code },
      { label: 'Valuation', cell: (l) => l.valuation_method }, { label: 'Quantity', num: true, cell: (l) => `${l.qty} ${l.unit}` },
      { label: 'Unit cost', num: true, cell: (l) => money(l.unit_cost) }, { label: 'Value', num: true, cell: (l) => money(l.value) }], lines, 'No stock on hand.'));
  };
  ent.addEventListener('change', () => load().catch(showError));
  const tools = h('div', { class: 'toolbar' }, field('Legal entity', ent),
    S.can.has('stock.issue') ? h('a', { class: 'btn', href: '#/new/stockissue' }, 'Issue stock') : null,
    S.can.has('stock.count') ? h('a', { class: 'btn ghost', href: '#/new/stockcount' }, 'Record a count') : null,
    h('span', { class: 'spacer' }), exportBar(() => `/v1/entities/${ent.value}/stock-on-hand`, () => `Stock on hand — ${(S.ref.legal_entities.find((e) => e.id === ent.value) || {}).name || ''}`, { entity: () => ent.value }));
  page('Stock on hand', 'Quantities and values by item and warehouse (the stores of your projects, or every warehouse for tenant-wide roles).', tools, out);
  await load();
}


// ---------- export (PDF / Word / Excel) ----------
// The server renders the file from the same authorised read the screen
// uses, stamped with the company, the person and the time.
function exportBar(src, title, opts = {}) {
  const val = (x) => (typeof x === 'function' ? x() : x);
  const wrap = h('div', { class: 'export', role: 'group', 'aria-label': 'Export' }, h('span', { class: 'lbl' }, 'Export'));
  for (const [fmt, label] of [['pdf', 'PDF'], ['docx', 'Word'], ['xlsx', 'Excel']]) {
    const b = h('button', { type: 'button', class: 'ghost', 'data-export': fmt, 'aria-label': `Export as ${label}` }, label);
    b.addEventListener('click', async () => {
      clearError();
      b.disabled = true;
      try {
        const qs = new URLSearchParams({ src: val(src), format: fmt, title: val(title) });
        if (opts.entity && opts.entity()) qs.set('entity', opts.entity());
        const r = await fetch('/v1/export?' + qs, { headers: { Authorization: 'Bearer ' + S.token } });
        if (!r.ok) {
          const d = await r.json().catch(() => ({}));
          const e = d.error || {};
          throw new ApiError(r.status, e.code || 'error', e.message || r.statusText, r.headers.get('WWW-Authenticate'));
        }
        const blob = await r.blob();
        const name = (/filename="([^"]+)"/.exec(r.headers.get('Content-Disposition') || '') || [])[1] || `serk-export.${fmt}`;
        const url = URL.createObjectURL(blob);
        const a = h('a', { href: url, download: name, hidden: true });
        document.body.append(a);
        a.click();
        a.remove();
        setTimeout(() => URL.revokeObjectURL(url), 60000);
        say(`${label} file ready: ${name}`);
      } catch (e) { showError(e); } finally { b.disabled = false; }
    });
    wrap.append(b);
  }
  return wrap;
}

// ---------- reports ----------
const REPORTS = [
  ['#/tb', 'Trial balance', 'Posted debits and credits by account at any date, with the balance check.', 'journal.read'],
  ['#/aging/ap', 'Payables aging', 'Approved supplier invoices still open, by days past due.', 'scope.tenant_wide'],
  ['#/aging/ar', 'Receivables aging', 'Approved customer invoices still open after receipts and credit notes.', 'scope.tenant_wide'],
  ['#/recon', 'Reconciliation', 'Every subledger against the general ledger: payables, bank, receivables, inventory.', 'journal.read'],
  ['#/cost', 'Project cost report', 'Budget, commitments, receipts, invoices and posted actuals by cost code.', 'p2p.read'],
  ['#/evm', 'Earned value', 'Planned value, earned value and actual cost from the baseline schedule.', 'p2p.read'],
  ['#/stock', 'Stock on hand', 'Quantities and values by item and warehouse.', 'p2p.read'],
  ['#/journals', 'Journals', 'Draft and approved journals awaiting decision.', 'journal.read'],
];
async function renderReports() {
  const grid = h('div', { class: 'reportgrid' });
  for (const [href, t, d, perm] of REPORTS) {
    if (!allowed(perm)) continue;
    grid.append(h('a', { class: 'card', href }, h('h3', {}, t), h('p', {}, d)));
  }
  // The report catalogue (WP-1.5): grouped, and limited to what this person may run.
  let cat = [];
  try { cat = (await api('GET', '/v1/reports/catalogue')).reports || []; } catch { cat = []; }
  const groups = {};
  for (const r of cat) (groups[r.group] = groups[r.group] || []).push(r);
  const sections = Object.keys(groups).sort().map((g) => h('section', {}, h('h2', { class: 'section' }, g),
    h('div', { class: 'reportgrid' }, groups[g].map((r) => h('a', { class: 'card', href: `#/report/${r.id}` }, h('h3', {}, r.title), h('p', {}, r.description))))));
  page('Reports', 'Every report and every list can be exported as PDF, Word or Excel with the company letterhead.', h('h2', { class: 'section' }, 'Core reports'), grid,
    ...sections, h('p', { class: 'foot-note' }, 'Exports contain only what you may see. Each export is recorded with your name and the time.'));
}

// ---------- catalogue report page (#/report/<id>; WP-1.5) ----------
// One generic page for every catalogue report: filters, the table with totals,
// the data-as-of time and the filters used, and PDF / Word / Excel export.
async function renderCatalogueReport(id) {
  const cat = (await api('GET', '/v1/reports/catalogue')).reports || [];
  const def = cat.find((r) => r.id === id);
  if (!def) { page('Report', 'This report does not exist or is not available to your role.'); return; }
  const has = (k) => def.filters.includes(k);
  const ent = select('entity', entityOpts());
  const proj = select('project', [{ value: '', label: def.id === 'budget-vs-actual' ? 'Choose a project' : 'All my projects' }, ...projectOpts()]);
  const from = input('from', { type: 'date' });
  const to = input('to', { type: 'date' });
  let subject = null;
  if (def.subject === 'supplier') subject = select('subject', [{ value: '', label: 'Choose a supplier' }, ...(await docOpts('supplier', 'ACTIVE'))]);
  if (def.subject === 'customer') subject = select('subject', [{ value: '', label: 'Choose a customer' }, ...(await docOpts('customer', 'ACTIVE'))]);
  if (def.subject === 'account') subject = select('subject', [{ value: '', label: 'All accounts' }, ...S.ref.accounts.map((a) => ({ value: a.id, label: `${a.code} ${a.name}` }))]);
  const out = h('div');
  const src = () => {
    const q = new URLSearchParams({ entity: ent.value });
    if (has('project') && proj.value) q.set('project', proj.value);
    if (subject && subject.value) q.set('subject', subject.value);
    if (has('from') && from.value) q.set('from', from.value);
    if (has('to') && to.value) q.set('to', to.value);
    return `/v1/reports/catalogue/${def.id}?${q}`;
  };
  const load = async () => {
    out.textContent = '';
    if (!ent.value) return;
    if ((def.subject === 'supplier' || def.subject === 'customer') && !subject.value) { out.append(h('p', { class: 'hint' }, `Choose a ${def.subject} to see this statement.`)); return; }
    if (def.id === 'budget-vs-actual' && !proj.value) { out.append(h('p', { class: 'hint' }, 'Choose a project to see this report.')); return; }
    const r = await api('GET', src());
    const cols = def.columns.map((c) => ({ label: c.label, num: c.type === 'money' || c.type === 'qty',
      cell: (row) => (c.type === 'money' ? money(row[c.key]) : c.type === 'qty' ? row[c.key] : row[c.key]) }));
    const rows = r.rows.slice();
    if (r.totals && rows.length) {
      const t = { _class: 'total' };
      def.columns.forEach((c, i) => { t[c.key] = i === 0 ? 'Total' : r.totals[c.key] !== undefined ? r.totals[c.key] : ''; });
      rows.push(t);
    }
    const used = Object.entries(r.filters || {}).map(([k, v]) => `${k}: ${v}`).join(' · ') || 'no filters';
    out.append(h('p', { class: 'hint', role: 'status' }, `${r.row_count} row(s). Data as of ${r.as_of}. Filters: ${used}.${r.cut ? ' Showing the first 5,000 rows only; narrow the dates or project.' : ''}`),
      table(def.title, cols, rows, 'No records match these filters.'));
  };
  for (const el of [ent, proj, from, to, subject].filter(Boolean)) el.addEventListener('change', () => load().catch(showError));
  const bar = h('div', { class: 'toolbar' }, field('Legal entity', ent), has('project') ? field('Project', proj) : null, subject ? field(def.subject.charAt(0).toUpperCase() + def.subject.slice(1), subject) : null,
    has('from') ? field('From', from) : null, has('to') ? field('To', to) : null, h('span', { class: 'spacer' }), exportBar(src, () => def.title, { entity: () => ent.value }));
  page(def.title, def.description, bar, out);
  await load();
}

function entityToolbar(ent, at, extra) {
  return h('div', { class: 'toolbar' }, field('Legal entity', ent), field('As at', at), h('span', { class: 'spacer' }), extra);
}

async function renderTB() {
  const ent = select('legal_entity_id', entityOpts());
  const at = input('as_of', { type: 'date', value: today() });
  const out = h('div');
  const src = () => `/v1/reports/trial-balance?entity=${ent.value}&as_of=${at.value}`;
  const load = async () => {
    out.textContent = '';
    if (!ent.value || !at.value) return;
    const r = await api('GET', src());
    const rows = r.accounts.filter((a) => a.debit !== '0' || a.credit !== '0');
    rows.push({ _class: 'total', account_code: '', account_name: 'Total', account_type: '', debit: r.total_debit, credit: r.total_credit, balance: r.difference });
    out.append(h('p', { class: r.balanced ? 'notice' : 'alert', role: 'status' }, r.balanced ? 'Debits equal credits: the ledger balances.' : `Out of balance by ${money(r.difference)}.`),
      table(`Trial balance at ${r.as_of}`, [
        { label: 'Code', cell: (a) => h('span', { class: 'ref' }, a.account_code) }, { label: 'Account', cell: (a) => a.account_name },
        { label: 'Type', cell: (a) => (a.account_type ? words(a.account_type.toLowerCase()) : '') },
        { label: 'Debit', num: true, cell: (a) => money(a.debit) }, { label: 'Credit', num: true, cell: (a) => money(a.credit) },
        { label: 'Balance', num: true, cell: (a) => money(a.balance) }], rows, 'No posted entries.'));
  };
  for (const el of [ent, at]) el.addEventListener('change', () => load().catch(showError));
  page('Trial balance', 'Posted journal lines summed by account up to the date chosen. Only posted entries count.',
    entityToolbar(ent, at, exportBar(src, () => `Trial balance at ${at.value}`, { entity: () => ent.value })), out);
  await load();
}

async function renderAging(kind) {
  const ar = kind === 'ar';
  const ent = select('legal_entity_id', entityOpts());
  const at = input('as_of', { type: 'date', value: today() });
  const out = h('div');
  const title = ar ? 'Receivables aging' : 'Payables aging';
  const src = () => `/v1/reports/${kind}-aging?entity=${ent.value}&as_of=${at.value}`;
  const buckets = [['not_due', 'Not due'], ['d1_30', '1–30'], ['d31_60', '31–60'], ['d61_90', '61–90'], ['over_90', 'Over 90'], ['open', 'Open']];
  const load = async () => {
    out.textContent = '';
    if (!ent.value || !at.value) return;
    const r = await api('GET', src());
    const parties = [...r.by_party, { ...r.total, _class: 'total' }];
    out.append(table(`${title} by ${ar ? 'customer' : 'supplier'} at ${r.as_of}`, [
      { label: ar ? 'Customer' : 'Supplier', cell: (p) => p.party },
      ...buckets.map(([k, l]) => ({ label: l, num: true, cell: (p) => money(p[k]) }))], parties, 'Nothing open.'),
    table(`Open invoices (${r.items.length})`, [
      { label: ar ? 'Customer' : 'Supplier', cell: (i) => i.party }, { label: 'Invoice', cell: (i) => h('span', { class: 'ref' }, i.document) },
      { label: 'Invoice date', cell: (i) => i.invoice_date }, { label: 'Due', cell: (i) => i.due_date },
      { label: 'Days overdue', num: true, cell: (i) => (i.days_overdue ? String(i.days_overdue) : '') },
      { label: 'Open', num: true, cell: (i) => `${money(i.open)} ${i.currency}` }], r.items, 'Nothing open.'));
  };
  for (const el of [ent, at]) el.addEventListener('change', () => load().catch(showError));
  page(title, ar ? 'Approved customer invoices less confirmed receipts and approved credit notes, by days past due.'
    : 'Approved supplier invoices less released payments, by days past due.',
  entityToolbar(ent, at, exportBar(src, () => `${title} at ${at.value}`, { entity: () => ent.value })), out);
  await load();
}

// ---------- notifications ----------
// The bell counts the items waiting for you (the same queues as "My work"),
// refreshed every minute while the tab is visible. New items can also raise
// a desktop notification if you allow it. Nothing is ever approved
// automatically: notifications only point you at work.
const BELL = { timer: null, items: [], server: [], total: null, open: false };
const seenKey = () => `serk-seen-${S.me ? S.me.tenant_id + '/' + S.me.user_id : ''}`;
function readSeen() { try { return JSON.parse(localStorage.getItem(seenKey()) || '{}'); } catch { return {}; } }
function writeSeen(v) { try { localStorage.setItem(seenKey(), JSON.stringify(v)); } catch { /* storage unavailable */ } }
function setBell(items) {
  const prev = BELL.total;
  BELL.items = items;
  BELL.total = items.reduce((n, q) => n + (q.n || 0), 0) + (BELL.server || []).filter((n) => !n.read_at).length;
  const c = $('bell-count');
  c.textContent = BELL.total > 99 ? '99+' : String(BELL.total);
  c.hidden = !BELL.total;
  $('bell').setAttribute('aria-label', `Notifications: ${BELL.total} item${BELL.total === 1 ? '' : 's'} waiting`);
  if (prev != null && BELL.total > prev && document.hidden && 'Notification' in window && Notification.permission === 'granted') {
    const grew = items.filter((q) => q.n).map((q) => q.label).slice(0, 3).join(', ');
    try { new Notification('SERK Construction', { body: `${BELL.total - prev} new item(s) waiting: ${grew}`, tag: 'serk-work' }); } catch { /* not allowed here */ }
  }
  if (BELL.open) renderDrawer();
}
async function pollBell() {
  if (!S.token || document.hidden) return;
  try {
    const [queues, server] = await Promise.all([workQueues(), api('GET', '/v1/notifications?limit=50').catch(() => ({ items: [] }))]);
    BELL.server = server.items || [];
    setBell(queues);
  } catch { /* next poll */ }
}
function startBell() {
  $('bell').hidden = false;
  stopBell(true);
  pollBell();
  BELL.timer = setInterval(pollBell, 60000);
}
function stopBell(keep) {
  if (BELL.timer) clearInterval(BELL.timer);
  BELL.timer = null;
  if (!keep) { $('bell').hidden = true; $('bell-count').hidden = true; BELL.total = null; closeDrawer(); }
}
function renderDrawer() {
  const d = $('drawer');
  const seen = readSeen();
  const list = BELL.items.filter((q) => q.n);
  const enable = 'Notification' in window && Notification.permission === 'default'
    ? h('button', { type: 'button', class: 'ghost' }, 'Enable desktop alerts') : null;
  if (enable) enable.addEventListener('click', async () => { try { await Notification.requestPermission(); } catch { /* ignored */ } renderDrawer(); });
  const server = (BELL.server || []).slice(0, 20);
  const serverList = server.length ? h('ul', {}, server.map((n) => h('li', {}, h('a', { href: n.href || '#/home', on: { click: async () => {
    closeDrawer();
    if (!n.read_at) { try { await api('POST', `/v1/notifications/${n.id}/read`); n.read_at = new Date().toISOString(); } catch { /* ignore */ } }
  } } },
    h('span', { class: n.read_at ? 'dot seen' : 'dot', 'aria-hidden': 'true' }),
    h('span', {}, h('span', { class: n.severity === 'critical' ? 'chip bad' : n.severity === 'warn' ? 'chip warn' : 'chip' }, n.severity), ' ', n.title,
      n.read_at ? null : h('span', { class: 'sr' }, ' (unread)')),
    h('span', { class: 'hint' }, when(n.created_at).split(',')[0]))))) : null;
  d.replaceChildren(
    h('header', {}, h('h2', { id: 'drawer-title' }, 'Notifications'), h('button', { type: 'button', class: 'ghost', 'aria-label': 'Close notifications', on: { click: closeDrawer } }, 'Close')),
    serverList ? h('h3', { class: 'drawer-sub' }, 'Alerts') : null, serverList,
    h('h3', { class: 'drawer-sub' }, 'Waiting for you'),
    list.length ? h('ul', {}, list.map((q) => h('li', {}, h('a', { href: q.href, on: { click: closeDrawer } },
      h('span', { class: (seen[q.label] || 0) >= q.n ? 'dot seen' : 'dot', 'aria-hidden': 'true' }), h('span', {}, q.label,
        (seen[q.label] || 0) < q.n ? h('span', { class: 'sr' }, ' (new)') : null), h('span', { class: 'count' }, String(q.n))))))
      : h('p', { class: 'empty' }, 'You are all caught up.'),
    h('div', { class: 'foot' }, h('a', { href: '#/home', on: { click: closeDrawer } }, 'Open my work'), h('span', { class: 'spacer' }), enable,
      'Notification' in window && Notification.permission === 'granted' ? h('span', { class: 'hint' }, 'Desktop alerts on') : null));
  d.setAttribute('aria-labelledby', 'drawer-title');
}
function openDrawer() {
  BELL.open = true;
  renderDrawer();
  $('drawer').hidden = false;
  $('bell').setAttribute('aria-expanded', 'true');
  const seen = {};
  for (const q of BELL.items) seen[q.label] = q.n || 0;
  setTimeout(() => writeSeen(seen), 1500);
  const first = $('drawer').querySelector('a, button');
  if (first) first.focus();
}
function closeDrawer() {
  if (!BELL.open) return;
  BELL.open = false;
  $('drawer').hidden = true;
  $('bell').setAttribute('aria-expanded', 'false');
}
$('bell').addEventListener('click', () => (BELL.open ? closeDrawer() : openDrawer()));
document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && BELL.open) { closeDrawer(); $('bell').focus(); } });
document.addEventListener('click', (e) => { if (BELL.open && !$('drawer').contains(e.target) && !$('bell').contains(e.target)) closeDrawer(); });
document.addEventListener('visibilitychange', () => { if (!document.hidden) pollBell(); });


// ---------- company branding ----------
// The vendor is SERK Construction OS; the construction company using it is
// the tenant. Its name leads once signed in.
function brandCompany() {
  const name = document.querySelector('.brand .name');
  const tag = document.querySelector('.brand .tag');
  if (S.company && S.company.display_name) {
    name.textContent = S.company.display_name;
    tag.textContent = 'SERK Construction OS';
  } else {
    name.textContent = 'SERK Construction OS';
    tag.textContent = 'Operations';
  }
}

// ---------- ask ----------
const SUGGESTED = ['Which projects are over budget?', 'Which activities are behind schedule?', 'Which purchase orders have not been delivered?',
  'Which invoices are awaiting approval?', 'What is the cash requirement for the next 90 days?', 'Which projects have deteriorating margins?',
  'What changed last week?', 'Are there duplicate invoices or suspicious payments?'];
async function renderAsk() {
  const q = h('input', { type: 'text', name: 'question', maxlength: '500', autocomplete: 'off', placeholder: 'Ask about your projects, money or risks' });
  const out = h('div', { 'aria-live': 'polite' });
  const ask = async (question) => {
    clearError();
    out.replaceChildren(h('p', { class: 'hint' }, 'Working it out from your records…'));
    try {
      const a = await api('POST', '/v1/assistant/ask', { question });
      const lines = a.text.split('\n');
      out.replaceChildren(h('div', { class: 'panel answer' },
        h('p', { class: 'eyebrow' }, `${words(a.intent)} · data as of ${a.freshness}${a.phrased_by_model ? ' · phrased by a language model from these facts' : ''}`),
        h('div', { class: 'answer-text' }, lines.map((l) => h('p', {}, l))),
        a.assumptions && a.assumptions.length ? h('details', {}, h('summary', {}, 'Assumptions and method'), h('ul', {}, a.assumptions.map((x) => h('li', {}, x)))) : null,
        a.missing && a.missing.length ? h('p', { class: 'notice' }, 'Missing or not assessable: ', a.missing.join('; ')) : null,
        a.sources && a.sources.length ? h('div', {}, h('h2', { class: 'section' }, 'Sources'), h('ul', { class: 'sources' }, a.sources.map((s) => h('li', {}, h('a', { href: s.href }, s.label))))) : null));
    } catch (e) { showError(e); out.textContent = ''; }
  };
  const form = h('form', { class: 'askbar', on: { submit: (ev) => { ev.preventDefault(); if (q.value.trim()) ask(q.value.trim()); } } },
    q, h('button', { type: 'submit' }, 'Ask'));
  page('Ask', 'Answers come from the company’s own records, with the formula, the sources and what is missing. Nothing is invented, and nothing is decided for you.',
    form, h('div', { class: 'chips' }, SUGGESTED.map((t) => h('button', { type: 'button', class: 'ghost', on: { click: () => { q.value = t; ask(t); } } }, t))), out);
  q.focus();
}

// ---------- risks ----------
const LEVEL = { high: 'chip bad', medium: 'chip warn', low: 'chip ok', unknown: 'chip' };
function findingCard(f) {
  return h('div', { class: 'panel finding' },
    h('div', { class: 'pagehead' }, h('div', {}, h('p', { class: 'eyebrow' }, f.area), h('h3', {}, f.title)), h('span', { class: LEVEL[f.level] || 'chip' }, f.level)),
    f.metric ? h('p', {}, h('strong', {}, words(f.metric)), f.value ? `: ${f.value}` : '', f.threshold ? ` (threshold ${f.threshold})` : '') : null,
    f.amounts ? h('dl', { class: 'kv' }, Object.entries(f.amounts).flatMap(([k, v]) => [h('dt', {}, words(k)), h('dd', {}, v)])) : null,
    f.advice ? h('p', { class: 'advice' }, f.advice) : null,
    f.missing && f.missing.length ? h('p', { class: 'hint' }, 'Missing: ', f.missing.join('; ')) : null,
    f.formula ? h('p', { class: 'hint' }, 'How: ', f.formula) : null,
    f.evidence && f.evidence.length ? h('p', {}, f.evidence.map((e, i) => [i ? ' · ' : '', h('a', { href: e.href }, e.label)])) : null);
}
async function renderRisk(projectId) {
  if (projectId) {
    const a = await api('GET', `/v1/intelligence/projects/${projectId}/risks`);
    const parts = [h('p', {}, h('span', { class: LEVEL[a.overall] }, `overall ${a.overall}`), ` · computed ${a.data_as_of} · ${a.algorithm}`)];
    if (a.forecast) {
      const f = a.forecast;
      parts.push(h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Forecast of final cost'),
        h('dl', { class: 'kv' }, ...[['Budget at completion (BAC)', f.bac], ['Planned value (PV)', f.pv], ['Earned value (EV)', f.ev], ['Actual cost (AC)', f.ac],
          ['SPI', f.spi || 'not defined'], ['CPI', f.cpi || 'not defined'], ['Estimate at completion (EAC)', f.eac || 'not computable'], ['Estimate to complete (ETC)', f.etc || '—'],
          ['Variance at completion (VAC)', f.vac || '—'], ['Percent complete (EV / BAC)', f.percent_complete || '—'], ['Method', f.method]].flatMap(([k, v]) => [h('dt', {}, k), h('dd', {}, v)])),
        h('details', {}, h('summary', {}, 'Assumptions'), h('ul', {}, (f.assumptions || []).map((x) => h('li', {}, x))))));
    }
    parts.push(...a.findings.map(findingCard));
    if (a.history && a.history.length) parts.push(h('p', { class: 'hint' }, 'Recorded assessments: ', a.history.map((p) => `${when(p.at).split(',')[0]} ${p.overall}`).join(' · ')));
    page(`${a.code} ${a.name} — risks`, 'Deterministic assessment from approved baselines, posted ledger cost and approved purchase orders. Every finding shows its formula and evidence.', ...parts);
    return;
  }
  const r = await api('GET', '/v1/intelligence/portfolio');
  const cards = h('div', { class: 'cards' });
  for (const a of r.projects) {
    const counts = {};
    for (const f of a.findings) counts[f.level] = (counts[f.level] || 0) + 1;
    cards.append(h('a', { class: a.overall === 'high' ? 'card hot' : 'card', href: `#/risk/${a.project_id}` },
      h('div', { class: 'pagehead' }, h('div', { class: 't' }, `${a.code} ${a.name}`), h('span', { class: LEVEL[a.overall] }, a.overall)),
      h('div', { class: 'hint' }, Object.entries(counts).map(([k, v]) => `${v} ${k}`).join(' · ')),
      a.forecast && a.forecast.eac ? h('div', { class: 'hint' }, `EAC ${a.forecast.eac} · VAC ${a.forecast.vac}`) : null));
  }
  page('Risks', `Each project assessed on schedule, cost, budget consumption and procurement (${r.algorithm}). Open a project for the formulas, evidence and advice.`,
    r.projects.length ? cards : h('p', { class: 'empty' }, 'No projects in view.'));
}

async function renderControls() {
  const ent = select('legal_entity_id', entityOpts());
  const out = h('div');
  const load = async () => {
    out.textContent = '';
    if (!ent.value) return;
    const [cash, ctl] = await Promise.all([api('GET', `/v1/intelligence/cash?entity=${ent.value}`), api('GET', `/v1/intelligence/controls?entity=${ent.value}`)]);
    out.append(h('div', { class: 'panel' }, h('div', { class: 'pagehead' }, h('h2', { class: 'section' }, '90-day cash outlook'), h('span', { class: LEVEL[cash.level] }, cash.level)),
      h('dl', { class: 'kv' }, ...[['Cash held (ledger)', cash.cash_held], ['Payables due in 90 days', `${cash.payables_due.total} (overdue ${cash.payables_due.overdue})`],
        ['Receivables due in 90 days', `${cash.receivables_due.total} (overdue ${cash.receivables_due.overdue})`], ['Net position', cash.net_90]].flatMap(([k, v]) => [h('dt', {}, k), h('dd', {}, v)])),
      h('p', { class: 'hint' }, cash.formula), h('details', {}, h('summary', {}, 'Assumptions'), h('ul', {}, cash.assumptions.map((x) => h('li', {}, x)))),
      cash.missing && cash.missing.length ? h('p', { class: 'notice' }, 'Missing: ' + cash.missing.join('; ')) : null));
    out.append(h('h2', { class: 'section' }, `Items for review (${ctl.items.length})`));
    if (!ctl.items.length) out.append(h('p', { class: 'notice' }, 'No duplicate invoice numbers and no payments just under the dual-approval threshold.'));
    for (const f of ctl.items) out.append(h('div', { class: 'panel finding' }, h('div', { class: 'pagehead' }, h('h3', {}, f.title), h('span', { class: LEVEL[f.level] }, f.level)),
      h('p', {}, f.detail), h('p', {}, f.evidence.map((e, i) => [i ? ' · ' : '', h('a', { href: e.href }, e.label)]))));
  };
  ent.addEventListener('change', () => load().catch(showError));
  page('Control review', 'Flags for a reviewer, never accusations: duplicate invoice numbers and payments just under the dual-approval threshold, plus the cash outlook.',
    h('div', { class: 'toolbar' }, field('Legal entity', ent)), out);
  await load();
}

// ---------- automation rules ----------
async function renderAutomation() {
  const [rules, cat] = await Promise.all([api('GET', '/v1/automation/rules'), api('GET', '/v1/automation/catalogue')]);
  const parts = [h('p', { class: 'notice' }, cat.note)];
  const canManage = S.can.has('automation.manage'), canActivate = S.can.has('automation.activate');
  if (canManage) {
    const code = input('code', { type: 'text', maxlength: '40', required: true, placeholder: 'AP-STALE' });
    const name = input('name', { type: 'text', maxlength: '120', required: true });
    const desc = h('textarea', { name: 'description', maxlength: '2000' });
    const kind = select('kind', cat.conditions.map((c) => ({ value: c.kind, label: c.kind })));
    const kdesc = h('p', { class: 'hint' });
    const params = h('div', { class: 'grid' });
    const role = input('notify_role', { type: 'text', maxlength: '40', placeholder: 'ap_supervisor' });
    const sev = select('severity', [{ value: 'info', label: 'info' }, { value: 'warn', label: 'warn' }, { value: 'critical', label: 'critical' }]);
    const title = input('title', { type: 'text', maxlength: '200' });
    const PARAMS = { document_stale: ['subject', 'status', 'hours'], invoice_due: ['days'], receivable_overdue: ['days'], po_overdue: [], budget_threshold: ['percent'],
      stock_below: ['item_code', 'quantity'], schedule_risk: ['ratio'], cost_risk: ['ratio'], export_volume: ['count'],
      notice_due: ['days'], obligation_due: ['days'], guarantee_expiring: ['days'] };
    const inputs = {};
    const paint = () => {
      params.textContent = '';
      kdesc.textContent = (cat.conditions.find((c) => c.kind === kind.value) || {}).description || '';
      for (const k of PARAMS[kind.value] || []) { inputs[k] = input(k, { type: 'text', maxlength: '40' }); params.append(field(words(k), inputs[k])); }
    };
    kind.addEventListener('change', paint);
    paint();
    const form = h('form', { class: 'panel', novalidate: true, on: { submit: async (ev) => {
      ev.preventDefault();
      clearError();
      const when = { kind: kind.value };
      for (const k of PARAMS[kind.value] || []) {
        const v = (inputs[k].value || '').trim();
        if (!v) { showError(new Error(`Fill in ${words(k)}.`)); inputs[k].focus(); return; }
        when[k] = /^(hours|days|percent|count)$/.test(k) ? Number(v) : v;
      }
      try {
        await api('POST', '/v1/automation/rules', { code: code.value.trim().toUpperCase(), name: name.value.trim(), description: desc.value.trim(),
          body: { when, then: { notify_role: role.value.trim(), severity: sev.value, title: title.value.trim() || undefined } } });
        say('Rule proposed; someone else activates it.');
        await renderAutomation();
      } catch (e) { showError(e); }
    } } },
    h('h2', { class: 'section' }, 'Propose a rule'),
    h('div', { class: 'grid' }, field('Code', code), field('Name', name), field('Condition', kind, kdesc)), params,
    h('div', { class: 'grid' }, field('Notify role', role, 'A role name, e.g. ap_supervisor, project_manager, treasury_officer.'), field('Severity', sev), field('Title (optional)', title)),
    field('Description', desc), h('div', { class: 'actions' }, h('button', { type: 'submit' }, 'Propose rule')));
    parts.push(form);
  }
  const act = (r) => {
    const box = h('span', { class: 'inline' });
    if (r.status === 'DRAFT' && canActivate && r.created_by !== S.me.user_id) {
      const b = h('button', { type: 'button' }, 'Activate');
      b.addEventListener('click', async () => { clearError(); b.disabled = true; try { await api('POST', `/v1/automation/rules/${r.id}/activate`); say(`${r.code} active.`); await renderAutomation(); } catch (e) { b.disabled = false; showError(e); } });
      box.append(b);
    }
    if (r.status !== 'RETIRED' && (canActivate || canManage)) {
      const reason = input('reason', { type: 'text', maxlength: '500', 'aria-label': `Reason for retiring ${r.code}` });
      const b = h('button', { type: 'button', class: 'danger' }, 'Retire');
      b.addEventListener('click', async () => { clearError(); if (reason.value.trim().length < 5) { showError(new Error('Give a reason of at least 5 characters.')); reason.focus(); return; }
        b.disabled = true; try { await api('POST', `/v1/automation/rules/${r.id}/retire`, { reason: reason.value.trim() }); say(`${r.code} retired.`); await renderAutomation(); } catch (e) { b.disabled = false; showError(e); } });
      box.append(reason, b);
    }
    return box;
  };
  parts.push(table(`Rules (${rules.items.length})`, [
    { label: 'Code', cell: (r) => h('span', { class: 'ref' }, r.code) }, { label: 'Name', cell: (r) => r.name },
    { label: 'Condition', cell: (r) => { const w = r.body.when || {}; return Object.entries(w).map(([k, v]) => `${k}=${v}`).join(' '); } },
    { label: 'Then', cell: (r) => { const t = r.body.then || {}; return `notify ${t.notify_role || t.notify_user} (${t.severity})`; } },
    { label: 'Status', cell: (r) => chip(r.status) }, { label: 'Proposed by', cell: (r) => r.created_by }, { label: 'Activated by', cell: (r) => r.activated_by || '' },
    { label: 'Last run', cell: (r) => (r.last_run_at ? `${when(r.last_run_at)} · ${r.last_run_findings} finding(s)` : 'not yet') }, { label: 'Action', cell: act }], rules.items, 'No rules yet.'));
  page('Automation rules', 'Rules watch the company’s records and tell the right people. A rule is proposed by one person and activated by another; it never approves, posts, pays or releases anything.', ...parts);
}

// ---------- company settings ----------
async function renderSettings() {
  const st = await api('GET', '/v1/tenant/settings');
  const can = S.can.has('tenant.settings.manage');
  const f = {
    display_name: input('display_name', { type: 'text', maxlength: '120', value: st.display_name, required: true }),
    short_code: input('short_code', { type: 'text', maxlength: '32', value: st.short_code || '' }),
    letterhead_name: input('letterhead_name', { type: 'text', maxlength: '160', value: st.letterhead_name || '' }),
    country: input('country', { type: 'text', maxlength: '2', value: st.country }),
    timezone: input('timezone', { type: 'text', maxlength: '64', value: st.timezone }),
    locale: select('locale', [{ value: 'en', label: 'English' }, { value: 'am', label: 'Amharic (አማርኛ)' }, { value: 'ti', label: 'Tigrinya (ትግርኛ)' }]),
    export_daily_limit: input('export_daily_limit', { type: 'number', min: '1', max: '100000', value: String(st.export_daily_limit) }),
    theme_accent: input('theme_accent', { type: 'text', maxlength: '7', value: st.theme_accent || '', placeholder: '#1b4a33' }),
  };
  f.locale.value = st.locale;
  if (!can) for (const el of Object.values(f)) el.disabled = true;
  const form = h('form', { class: 'panel', novalidate: true, on: { submit: async (ev) => {
    ev.preventDefault();
    clearError();
    try {
      const body = { version: st.version };
      for (const [k, el] of Object.entries(f)) body[k] = k === 'export_daily_limit' ? Number(el.value) : el.value.trim();
      S.company = await api('PUT', '/v1/tenant/settings', body);
      brandCompany();
      say('Company settings saved.');
      await renderSettings();
    } catch (e) { showError(e); }
  } } },
  h('div', { class: 'grid' }, field('Company name', f.display_name), field('Portal code', f.short_code, 'Lowercase letters, digits and hyphens.'),
    field('Letterhead on exports', f.letterhead_name, 'Printed on every PDF, Word and Excel export.'), field('Country', f.country), field('Time zone', f.timezone),
    field('Language', f.locale, 'Interface translation to Amharic and Tigrinya is in preparation; data is stored unchanged.'),
    field('Daily export limit per person', f.export_daily_limit, 'Beyond this, exports are refused for the day and security is told.'), field('Accent colour (optional)', f.theme_accent)),
  can ? h('div', { class: 'actions' }, h('button', { type: 'submit' }, 'Save settings')) : h('p', { class: 'hint' }, 'Only a security administrator can change these.'));
  page('Company settings', `${st.display_name} on SERK Construction OS. Version ${st.version}${st.updated_by ? `, last changed by ${st.updated_by} ${when(st.updated_at)}` : ''}.`, form,
    h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Data protection'), h('ul', {},
      h('li', {}, 'Every record is isolated per company by database row-level security; a session can only ever reach its own company.'),
      h('li', {}, 'Exports carry the company letterhead, the person’s name and the time, and are counted against a daily limit.'),
      h('li', {}, 'Bank account numbers are encrypted at rest and masked everywhere; revealing one needs a fresh multi-factor sign-in and is audited.'),
      h('li', {}, 'Every approval is a signed human decision verified by the database. Automation and the assistant can only read and notify.'))));
}

// ---------- people & access, my profile (0048) ----------
// The company directory as cards, a duty map (who prepares, approves,
// releases, governs), the role catalogue and the access history; and each
// person's own profile. The server decides every permission and refuses
// self-administration and duty conflicts; this page only previews them.
const STAGES = [
  ['read', 'Read only', 'Sees records'],
  ['prepare', 'Prepare', 'Creates and submits'],
  ['approve', 'Approve', 'Checks, approves, certifies'],
  ['release', 'Release & post', 'Posts, pays, closes'],
  ['govern', 'Govern', 'Sets the rules others work under'],
];
const STAGE_NAME = Object.fromEntries(STAGES.map(([k, n]) => [k, n]));
const stageChip = (st) => h('span', { class: `stagechip st-${st || 'none'}` }, st ? STAGE_NAME[st] : 'No access');
function initials(name) {
  const w = String(name || '?').replace(/^u-/, '').split(/[^\p{L}\p{N}]+/u).filter(Boolean);
  if (!w.length) return '?';
  return ((w[0][0] || '') + (w.length > 1 ? w[w.length - 1][0] : (w[0][1] || ''))).toUpperCase();
}
function avClass(id) { let n = 0; for (const c of String(id)) n = (n * 31 + c.charCodeAt(0)) >>> 0; return `av-${n % 6}`; }
const avatar = (id, name, size) => h('span', { class: `avatar ${avClass(id)} ${size || ''}`, 'aria-hidden': 'true' }, initials(name || id));
const personName = (x) => x.display_name || x.user_id;
function ago(s) {
  if (!s) return 'never';
  const d = (Date.now() - new Date(s)) / 1000;
  if (d < 90) return 'just now';
  if (d < 5400) return `${Math.round(d / 60)} min ago`;
  if (d < 129600) return `${Math.round(d / 3600)} h ago`;
  return `${Math.round(d / 86400)} days ago`;
}
const PEOPLE_EVENTS = {
  'people.person_added': 'was added to the directory', 'people.details_changed': 'had their details changed',
  'people.managed': 'had their access taken under management', 'people.disabled': 'was disabled and signed out everywhere',
  'people.enabled': 'was enabled again', 'people.role_granted': 'was given', 'people.role_removed': 'lost',
  'people.role_created': 'was created', 'people.role_changed': 'was changed', 'people.role_retired': 'was retired',
  'people.profile_changed': 'updated their own profile',
};
// Conflicting duties in a permission set, by the server's control catalogue.
function dutyConflicts(perms, controls) {
  const out = [];
  for (const c of controls || []) {
    const have = c.Perms.filter((p) => perms.has(p));
    if (have.length > 1) out.push({ control: c.Control, perms: have });
  }
  return out;
}

let PV = null; // the last /v1/people answer
let sheetReturn = null;
function sheetKey(e) { if (e.key === 'Escape') closeSheet(); }
function closeSheet() {
  const s = $('sheet');
  if (!s) return;
  s.remove();
  $('scrim')?.remove();
  document.removeEventListener('keydown', sheetKey);
  if (sheetReturn && sheetReturn.isConnected) sheetReturn.focus();
  sheetReturn = null;
}
function openSheet(label, head, ...body) {
  const back = $('sheet') ? sheetReturn : document.activeElement;
  closeSheet();
  sheetReturn = back;
  const x = h('button', { type: 'button', class: 'ghost x', on: { click: closeSheet } }, 'Close');
  const sheet = h('aside', { class: 'sheet', id: 'sheet', role: 'dialog', 'aria-modal': 'true', 'aria-label': label },
    h('div', { class: 'sheethead' }, head, x), h('div', { class: 'sheetbody' }, body));
  document.body.append(h('div', { class: 'scrim', id: 'scrim', on: { click: closeSheet } }), sheet);
  document.addEventListener('keydown', sheetKey);
  x.focus();
  return sheet;
}
const reasonInput = (placeholder) => input('reason', { type: 'text', minlength: '5', maxlength: '500', autocomplete: 'off', placeholder });
function needReason(el) {
  if (el.value.trim().length >= 5) return el.value.trim();
  el.setAttribute('aria-invalid', 'true');
  el.focus();
  throw new ApiError(422, 'validation', 'Give a reason of at least 5 characters; it is kept in the access history.');
}
function sheetError(where, e) {
  const box = where.querySelector('.sheeterr') || where.prepend(h('div', { class: 'callout bad sheeterr', role: 'alert' })) || where.querySelector('.sheeterr');
  box.textContent = '';
  box.append(h('strong', {}, errorTitle(e)), ' ', e.message || String(e));
  box.scrollIntoView({ block: 'nearest' });
}
const roleOf = (code) => (PV && PV.roles.find((r) => r.code === code)) || { code, name: words(code.replace(/^custom:/, '')), kind: 'seed', permissions: [], stage: '' };
const labelOf = (code) => (PV && PV.permissions.find((p) => p.code === code)) || { code, label: code, area: 'Other', stage: 'prepare' };

// Permissions grouped by area; tickable when `pick` is a Set to edit.
function permGroups(codes, pick, onChange) {
  const catalogue = pick ? PV.permissions : codes.map(labelOf);
  const areas = new Map();
  for (const p of catalogue) { if (!areas.has(p.area)) areas.set(p.area, []); areas.get(p.area).push(p); }
  const wrap = h('div', { class: 'permgroups' });
  for (const [area, list] of [...areas.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
    if (!pick) {
      wrap.append(h('div', { class: 'permgroup' }, h('h4', {}, area, h('span', { class: 'count' }, String(list.length))),
        h('ul', {}, list.map((p) => h('li', { class: `st-${p.stage}`, title: p.code }, h('span', { class: 'dot' }), p.label)))));
      continue;
    }
    const count = h('span', { class: 'count' });
    const paint = () => { count.textContent = `${list.filter((p) => pick.has(p.code)).length}/${list.length}`; };
    const fs = h('fieldset', { class: 'permgroup' }, h('legend', {}, area, count),
      list.map((p) => {
        const cb = h('input', { type: 'checkbox', value: p.code });
        cb.checked = pick.has(p.code);
        cb.addEventListener('change', () => { if (cb.checked) pick.add(p.code); else pick.delete(p.code); paint(); onChange(); });
        return h('label', { class: `st-${p.stage}`, 'data-perm': p.code, title: p.code }, cb, h('span', {}, p.label));
      }));
    paint();
    wrap.append(fs);
  }
  return wrap;
}

async function renderPeople(tab) {
  closeSheet();
  PV = await api('GET', '/v1/people');
  const v = PV;
  const me = v.me;
  const nameOf = (id) => { const x = v.people.find((p) => p.user_id === id); return x ? personName(x) : id; };
  const active = v.people.filter((p) => p.status === 'ACTIVE');
  const kpi = (n, k, warn) => h('div', { class: `kpi${warn && n ? ' warnk' : ''}` }, h('div', { class: 'v' }, String(n)), h('div', { class: 'k' }, k));
  const kpis = h('div', { class: 'kpis' },
    kpi(v.people.length, 'People in the directory'), kpi(active.length, 'Active'),
    kpi(v.people.filter((p) => p.managed).length, 'Access managed here'), kpi(v.people.filter((p) => !p.managed).length, 'Roles from sign-in'),
    kpi(v.people.length - active.length, 'Disabled', true), kpi(active.filter((p) => p.conflicts.length).length, 'Holding conflicting duties', true),
    kpi(v.roles.filter((r) => r.kind === 'company' && r.status === 'ACTIVE').length, 'Company roles'));

  const TABS = [['people', 'People', v.people.length], ['map', 'Duty map', null], ['roles', 'Roles', v.roles.filter((r) => r.status === 'ACTIVE').length], ['history', 'Access history', v.history.length]];
  let cur = TABS.some(([k]) => k === tab) ? tab : 'people';
  const content = h('div', { role: 'tabpanel' });
  const tabs = h('div', { class: 'tabs', role: 'tablist', 'aria-label': 'People and access' });
  const show = (k) => {
    cur = k;
    for (const b of tabs.children) b.setAttribute('aria-selected', String(b.dataset.k === k));
    history.replaceState(null, '', `#/people/${k}`);
    content.textContent = '';
    content.append(k === 'map' ? dutyMap(v, nameOf) : k === 'roles' ? rolesTab(v) : k === 'history' ? historyTab(v, nameOf) : peopleTab(v));
  };
  for (const [k, label, n] of TABS) {
    tabs.append(h('button', { type: 'button', role: 'tab', 'data-k': k, on: { click: () => show(k) } }, label, n == null ? null : h('span', { class: 'n' }, String(n))));
  }
  const actions = h('div', { class: 'toolbar' },
    me.can_manage_users ? h('button', { type: 'button', on: { click: () => addPersonSheet(v) } }, 'Add person') : null,
    me.can_manage_roles ? h('button', { type: 'button', class: 'ghost', on: { click: () => roleSheet(null) } }, 'New company role') : null,
    !me.can_manage_users && !me.can_manage_roles ? h('span', { class: 'hint' }, 'You can look but not change: only a user or role administrator changes access.') : null);
  page('People & access', 'Who works in this company, what each person may do and on which projects, and every change to that. Nobody changes their own access, and no one is given both halves of a control.',
    actions, kpis, tabs, content);
  show(cur);
}

function personCard(x, roles) {
  const stage = x.status === 'ACTIVE' ? (x.stage || 'none') : 'none';
  const shown = x.roles.slice(0, 3);
  return h('button', { type: 'button', class: `pcard st-${stage}${x.status !== 'ACTIVE' ? ' disabled-person' : ''}`, 'aria-label': `Open ${personName(x)}`, on: { click: () => personSheet(x.user_id) } },
    h('div', { class: 'row' }, avatar(x.user_id, personName(x)),
      h('div', { class: 'pwho' }, h('div', { class: 'nm' }, personName(x)), h('div', { class: 'sub' }, x.job_title || x.user_id))),
    h('div', { class: 'rolechips' }, shown.length ? shown.map((r) => h('span', { class: `rolechip${r.startsWith('custom:') ? ' company' : ''}` }, roleOf(r).name)) : h('span', { class: 'hint' }, 'No role'),
      x.roles.length > 3 ? h('span', { class: 'rolechip' }, `+${x.roles.length - 3}`) : null),
    h('div', { class: 'meta' }, x.status === 'ACTIVE' ? stageChip(x.stage) : chip('DISABLED'),
      h('span', {}, x.managed ? 'Managed here' : 'From sign-in'), h('span', {}, '·'), h('span', {}, `seen ${ago(x.last_seen)}`),
      x.conflicts.length ? h('span', { class: 'conflictflag' }, `⚠ ${x.conflicts.length} duty conflict${x.conflicts.length > 1 ? 's' : ''}`) : null));
}

function peopleTab(v) {
  const find = h('input', { type: 'search', class: 'findbox', placeholder: 'Find by name, sign-in id, job or role', 'aria-label': 'Find a person', autocomplete: 'off' });
  const FILTERS = [['all', 'Everyone', () => true], ['managed', 'Managed here', (x) => x.managed], ['signin', 'From sign-in', (x) => !x.managed],
    ['conflicts', 'Duty conflicts', (x) => x.conflicts.length > 0], ['disabled', 'Disabled', (x) => x.status !== 'ACTIVE']];
  let filter = 'all';
  let view = 'cards';
  const pills = h('div', { class: 'pills', role: 'group', 'aria-label': 'Show' });
  const viewp = h('div', { class: 'pills', role: 'group', 'aria-label': 'View' });
  const out = h('div');
  const draw = () => {
    for (const b of pills.children) b.setAttribute('aria-pressed', String(b.dataset.k === filter));
    for (const b of viewp.children) b.setAttribute('aria-pressed', String(b.dataset.k === view));
    const q = find.value.trim().toLowerCase();
    const test = FILTERS.find(([k]) => k === filter)[2];
    const rows = v.people.filter((x) => test(x) && (!q || [x.display_name, x.user_id, x.job_title, x.email, ...x.roles.map((r) => roleOf(r).name)].join(' ').toLowerCase().includes(q)));
    out.textContent = '';
    if (!rows.length) { out.append(h('p', { class: 'empty panel' }, 'Nobody matches.')); return; }
    if (view === 'cards') { out.append(h('div', { class: 'pgrid' }, rows.map((x) => personCard(x)))); return; }
    out.append(table(`${rows.length} people`, [
      { label: 'Person', cell: (x) => h('button', { type: 'button', class: 'linkbtn', on: { click: () => personSheet(x.user_id) } }, personName(x)) },
      { label: 'Sign-in id', cell: (x) => h('span', { class: 'ref' }, x.user_id) },
      { label: 'Job', cell: (x) => x.job_title },
      { label: 'Roles', cell: (x) => x.roles.map((r) => roleOf(r).name).join(', ') },
      { label: 'Furthest stage', cell: (x) => stageChip(x.stage) },
      { label: 'Roles from', cell: (x) => (x.managed ? 'Directory' : 'Sign-in') },
      { label: 'Status', cell: (x) => chip(x.status) },
      { label: 'Conflicts', num: true, cell: (x) => (x.conflicts.length ? h('span', { class: 'conflictflag' }, String(x.conflicts.length)) : '0') },
      { label: 'Last seen', cell: (x) => ago(x.last_seen) },
    ], rows));
  };
  for (const [k, label] of FILTERS) pills.append(h('button', { type: 'button', 'data-k': k, on: { click: () => { filter = k; draw(); } } }, label));
  for (const [k, label] of [['cards', 'Cards'], ['table', 'Table']]) viewp.append(h('button', { type: 'button', 'data-k': k, on: { click: () => { view = k; draw(); } } }, label));
  find.addEventListener('input', draw);
  draw();
  return h('div', {}, h('div', { class: 'toolbar' }, find, pills, h('span', { class: 'spacer' }), viewp), out);
}

function dutyMap(v, nameOf) {
  const roles = v.roles.filter((r) => r.status === 'ACTIVE' && r.holders > 0);
  const hl = select('highlight', [{ value: '', label: 'Highlight who holds… (any role)' }, ...roles.map((r) => ({ value: r.code, label: `${r.name} (${r.holders})` }))]);
  const active = v.people.filter((x) => x.status === 'ACTIVE' && x.stage);
  const lane = v.people.filter((x) => x.status !== 'ACTIVE' || !x.stage);
  const nodes = [];
  const node = (x) => {
    const n = h('button', { type: 'button', class: `dnode st-${x.status === 'ACTIVE' ? x.stage || 'none' : 'none'}`, 'data-roles': x.roles.join(' '), title: `${personName(x)} — ${x.roles.map((r) => roleOf(r).name).join(', ') || 'no role'}`, on: { click: () => personSheet(x.user_id) } },
      avatar(x.user_id, personName(x), 'sm'),
      h('span', { class: 'pwho' }, h('div', { class: 'nm' }, personName(x), x.conflicts.length ? h('span', { class: 'conflictflag' }, ' ⚠') : null),
        h('div', { class: 'sub' }, x.roles.length ? x.roles.map((r) => roleOf(r).name).join(', ') : 'No role')));
    nodes.push(n);
    return n;
  };
  const bar = h('div', { class: 'flowbar', 'aria-hidden': 'true' });
  const legend = h('div', { class: 'flowlegend' });
  for (const [k, name] of STAGES) {
    const n = active.filter((x) => x.stage === k).length;
    const seg = h('span', { class: `st-${k}`, title: `${name}: ${n}` });
    seg.style.flexGrow = String(n);
    bar.append(seg);
    legend.append(h('span', { class: `stagechip st-${k}` }, `${name} · ${n}`));
  }
  const map = h('div', { class: 'dmap', role: 'list', 'aria-label': 'People by the furthest stage of work they reach' },
    STAGES.map(([k, name, what]) => h('div', { class: `dcol st-${k}`, role: 'listitem' },
      h('div', { class: 'dhead' }, h('div', { class: 't' }, h('span', {}, `${name} (${active.filter((x) => x.stage === k).length})`)), h('div', { class: 'd' }, what)),
      active.filter((x) => x.stage === k).map(node))));
  hl.addEventListener('change', () => {
    for (const n of nodes) {
      const has = hl.value && n.dataset.roles.split(' ').includes(hl.value);
      n.classList.toggle('hit', !!has);
      n.classList.toggle('dim', !!hl.value && !has);
    }
  });
  return h('div', {},
    h('div', { class: 'panel' },
      h('p', { class: 'hint' }, 'Work moves left to right: one person prepares, another approves, a third releases or posts, and governors set the rules. Each person sits at the furthest stage their roles reach. ⚠ marks someone who holds both halves of a control.'),
      bar, legend),
    h('div', { class: 'toolbar' }, field('Highlight', hl)),
    h('div', { class: 'panel' }, map,
      lane.length ? h('div', { class: 'dlane' }, h('span', { class: 'lbl' }, 'Disabled or without access:'), lane.map(node)) : null));
}

function rolesTab(v) {
  const find = h('input', { type: 'search', class: 'findbox', placeholder: 'Find a role or a permission', 'aria-label': 'Find a role', autocomplete: 'off' });
  let kind = 'all';
  const pills = h('div', { class: 'pills', role: 'group', 'aria-label': 'Show' });
  const out = h('div', { class: 'rolecards' });
  const draw = () => {
    for (const b of pills.children) b.setAttribute('aria-pressed', String(b.dataset.k === kind));
    const q = find.value.trim().toLowerCase();
    out.textContent = '';
    const rows = v.roles.filter((r) => (kind === 'all' ? r.status === 'ACTIVE' : kind === 'retired' ? r.status === 'RETIRED' : r.kind === kind && r.status === 'ACTIVE') &&
      (!q || [r.name, r.code, r.description, ...r.permissions.map((p) => labelOf(p).label)].join(' ').toLowerCase().includes(q)));
    if (!rows.length) out.append(h('p', { class: 'empty' }, 'No role matches.'));
    for (const r of rows) {
      out.append(h('button', { type: 'button', class: `rolecard st-${r.stage || 'none'}`, on: { click: () => roleSheet(r.code) } },
        h('h4', {}, r.name), h('p', {}, r.kind === 'company' ? (r.description || 'Company role') : 'Product role'),
        h('div', { class: 'meta' }, stageChip(r.stage), h('span', {}, `${r.permissions.length} permissions`), h('span', {}, `${r.holders} ${r.holders === 1 ? 'person' : 'people'}`),
          r.kind === 'company' ? h('span', { class: 'rolechip company' }, 'company') : null)));
    }
  };
  for (const [k, label] of [['all', 'All roles'], ['company', 'Company roles'], ['seed', 'Product roles'], ['retired', 'Retired']]) {
    pills.append(h('button', { type: 'button', 'data-k': k, on: { click: () => { kind = k; draw(); } } }, label));
  }
  find.addEventListener('input', draw);
  draw();
  return h('div', {}, h('div', { class: 'toolbar' }, find, pills), out);
}

function historyTab(v, nameOf) {
  if (!v.history.length) return h('p', { class: 'panel empty' }, 'No access change has been made yet.');
  return h('div', { class: 'panel' }, h('ol', { class: 'timeline' }, v.history.map((e) => accessEventLine(e, nameOf))));
}
function accessEventLine(e, nameOf) {
  const d = e.detail || {};
  const isRole = String(e.subject).startsWith('custom:');
  const subject = isRole ? `Company role ${roleOf(e.subject).name}` : nameOf(e.subject);
  const what = PEOPLE_EVENTS[e.action] || words(e.action.replace(/^people\./, ''));
  return h('li', {}, h('div', {}, h('strong', {}, subject), ' ', what, d.role ? [' ', h('strong', {}, roleOf(d.role).name)] : null),
    h('div', { class: 'when' }, `${when(e.at)} · by ${nameOf(e.actor)}`), d.reason ? h('div', { class: 'why' }, `“${d.reason}”`) : null);
}

async function refreshAfter(fn, reopen) {
  const tab = (location.hash.split('/')[2]) || 'people';
  await fn();
  await renderPeople(tab);
  if (reopen) await reopen();
}

async function personSheet(id) {
  const [d] = await Promise.all([api('GET', `/v1/people/${encodeURIComponent(id)}`)]);
  const x = d.person;
  const me = PV.me;
  const self = x.user_id === me.user_id;
  const canEdit = me.can_manage_users && !self;
  const nameOf = (u) => { const p = PV.people.find((q) => q.user_id === u); return p ? personName(p) : u; };
  const reason = reasonInput('Why? For example: new site, HR ticket 41');
  const act = (fn) => async (ev) => {
    ev.preventDefault();
    try { const why = needReason(reason); await refreshAfter(() => fn(why), () => personSheet(id)); say('Saved.'); } catch (e) { sheetError($('sheet').querySelector('.sheetbody'), e); }
  };
  const tenantWide = x.permissions.includes('scope.tenant_wide');
  const parts = [];
  parts.push(h('p', { class: 'callout' }, x.managed
    ? 'Access is managed here: the roles and projects below are the ones in force on every request, whatever the sign-in token says.'
    : 'Roles and projects come from this person’s sign-in (the identity provider). Take their access under management to give and remove roles here.'));
  if (x.status !== 'ACTIVE') parts.push(h('p', { class: 'callout bad' }, `Disabled: ${x.status_reason || ''}. Every request is refused.`));
  if (x.conflicts.length) {
    parts.push(h('div', { class: 'callout bad' }, h('strong', {}, 'Holds both halves of a control: '),
      x.conflicts.map((c, i) => [i ? '; ' : '', `${c.Control} (${labelOf(c.A).label} + ${labelOf(c.B).label})`]),
      '. Remove a role, or record an approved duty-conflict exception.'));
  }
  if (self) parts.push(h('p', { class: 'callout' }, 'This is you. Another administrator must change your access.'));
  // Details
  const det = { display_name: input('display_name', { type: 'text', maxlength: '200', value: x.display_name }), email: input('email', { type: 'email', maxlength: '254', value: x.email }),
    job_title: input('job_title', { type: 'text', maxlength: '120', value: x.job_title }), phone: input('phone', { type: 'tel', maxlength: '40', value: x.phone }) };
  parts.push(h('section', {}, h('h3', {}, 'Details'),
    me.can_manage_users
      ? h('form', { novalidate: true, on: { submit: act((why) => api('PUT', `/v1/people/${encodeURIComponent(id)}`, { display_name: det.display_name.value, email: det.email.value, job_title: det.job_title.value, phone: det.phone.value, version: x.version, reason: why })) } },
        h('div', { class: 'grid' }, field('Name', det.display_name), field('Email', det.email), field('Job title', det.job_title), field('Phone', det.phone)),
        h('div', { class: 'actions' }, h('button', { type: 'submit', class: 'ghost' }, 'Save details')))
      : h('dl', { class: 'kv' }, h('dt', {}, 'Email'), h('dd', {}, x.email || '—'), h('dt', {}, 'Job'), h('dd', {}, x.job_title || '—'), h('dt', {}, 'Phone'), h('dd', {}, x.phone || '—')),
    h('dl', { class: 'kv' }, h('dt', {}, 'Sign-in id'), h('dd', { class: 'ref' }, x.user_id), h('dt', {}, 'First seen'), h('dd', {}, x.first_seen ? when(x.first_seen) : 'not signed in yet'),
      h('dt', {}, 'Last seen'), h('dd', {}, x.last_seen ? `${when(x.last_seen)} (${ago(x.last_seen)})` : '—'), h('dt', {}, 'Added by'), h('dd', {}, x.created_by === 'sign-in' ? 'their first sign-in' : nameOf(x.created_by)))));
  // Roles
  const held = new Set(x.permissions);
  const roleRows = x.managed
    ? x.grants.map((g) => h('div', { class: 'grantrow' }, h('div', { class: 'what' }, h('strong', {}, roleOf(g.role).name), ' ', stageChip(roleOf(g.role).stage),
      h('div', { class: 'why' }, `Given by ${nameOf(g.granted_by)} ${ago(g.granted_at)}: “${g.reason}”`)),
      canEdit ? h('button', { type: 'button', class: 'danger', on: { click: act((why) => api('POST', `/v1/role-grants/${g.id}/revoke`, { reason: why })) } }, 'Remove') : null))
    : x.roles.map((r) => h('div', { class: 'grantrow' }, h('div', { class: 'what' }, h('strong', {}, roleOf(r).name), ' ', stageChip(roleOf(r).stage), h('div', { class: 'why' }, 'From the sign-in token'))));
  const roleSec = h('section', {}, h('h3', {}, `Roles (${x.roles.length})`), roleRows.length ? roleRows : h('p', { class: 'hint' }, 'No role: this person can only sign in.'));
  if (canEdit && x.managed && x.status === 'ACTIVE') {
    const pickable = PV.roles.filter((r) => r.status === 'ACTIVE' && !x.roles.includes(r.code));
    const sel = h('select', { name: 'role' }, h('option', { value: '' }, 'Choose a role to give…'),
      [['company', 'Company roles'], ['seed', 'Product roles']].map(([k, lbl]) => {
        const rs = pickable.filter((r) => r.kind === k);
        return rs.length ? h('optgroup', { label: lbl }, rs.map((r) => h('option', { value: r.code }, `${r.name} — ${STAGE_NAME[r.stage] || ''}, ${r.permissions.length} permissions`))) : null;
      }));
    const preview = h('div', { 'aria-live': 'polite' });
    sel.addEventListener('change', () => {
      preview.textContent = '';
      if (!sel.value) return;
      const r = roleOf(sel.value);
      const after = new Set([...held, ...r.permissions]);
      const added = r.permissions.filter((p) => !held.has(p));
      const bad = dutyConflicts(after, PV.controls).filter((c) => c.perms.some((p) => !held.has(p)));
      preview.append(bad.length
        ? h('p', { class: 'callout bad' }, 'Will be refused: this would let them hold both halves of ', bad.map((c) => c.control).join('; '), '. An approved duty-conflict exception is needed first.')
        : h('p', { class: 'callout ok' }, `Adds ${added.length} permission${added.length === 1 ? '' : 's'}: `, added.slice(0, 6).map((p) => labelOf(p).label).join(', '), added.length > 6 ? ` and ${added.length - 6} more` : '', '.'));
    });
    roleSec.append(h('form', { novalidate: true, on: { submit: act((why) => { if (!sel.value) throw new ApiError(422, 'validation', 'Choose a role.'); return api('POST', `/v1/people/${encodeURIComponent(id)}/roles`, { role: sel.value, reason: why }); }) } },
      h('div', { class: 'inline' }, sel, h('button', { type: 'submit' }, 'Give role')), preview));
  }
  parts.push(roleSec);
  // Projects
  const projSec = h('section', {}, h('h3', {}, 'Projects'));
  if (tenantWide) projSec.append(h('p', { class: 'callout ok' }, 'Works on every project: a role gives company-wide scope.'));
  if (canEdit && x.managed && !tenantWide) {
    const boxes = PV.projects.map((p) => { const cb = h('input', { type: 'checkbox', value: p.id }); cb.checked = x.project_ids.includes(p.id); return h('label', { class: 'check' }, cb, `${p.code} — ${p.name}`); });
    projSec.append(h('form', { novalidate: true, on: { submit: act((why) => api('PUT', `/v1/people/${encodeURIComponent(id)}`, {
      display_name: x.display_name, email: x.email, job_title: x.job_title, phone: x.phone, version: x.version, reason: why,
      project_ids: boxes.map((l) => l.querySelector('input')).filter((c) => c.checked).map((c) => c.value) })) } },
      PV.projects.length ? boxes : h('p', { class: 'hint' }, 'No projects yet.'), h('div', { class: 'actions' }, h('button', { type: 'submit', class: 'ghost' }, 'Save projects'))));
  } else if (!tenantWide) {
    const names = x.project_ids.map((pid) => PV.projects.find((p) => p.id === pid)).filter(Boolean);
    projSec.append(names.length ? h('div', { class: 'rolechips' }, names.map((p) => h('span', { class: 'rolechip' }, `${p.code} — ${p.name}`))) : h('p', { class: 'hint' }, 'No project assigned.'));
  }
  parts.push(projSec);
  parts.push(h('section', {}, h('h3', {}, `What they may do (${x.permissions.length})`), x.permissions.length ? permGroups(x.permissions) : h('p', { class: 'hint' }, 'Nothing.')));
  // Manage
  if (canEdit) {
    const btns = [];
    if (!x.managed && x.status === 'ACTIVE') btns.push(h('button', { type: 'button', on: { click: act((why) => api('POST', `/v1/people/${encodeURIComponent(id)}/manage`, { reason: why })) } }, 'Manage access here'));
    if (x.status === 'ACTIVE') btns.push(h('button', { type: 'button', class: 'danger', on: { click: act((why) => api('POST', `/v1/people/${encodeURIComponent(id)}/disable`, { reason: why })) } }, 'Disable and sign out'));
    else btns.push(h('button', { type: 'button', on: { click: act((why) => api('POST', `/v1/people/${encodeURIComponent(id)}/enable`, { reason: why })) } }, 'Enable'));
    parts.splice(1, 0, h('section', { class: 'panel' }, h('h3', {}, 'Change access'), field('Reason (kept in the access history)', reason, 'Every button on this sheet uses it.'),
      h('div', { class: 'actions' }, btns)));
  }
  if (d.grant_history.length) {
    parts.push(h('section', {}, h('h3', {}, 'Role history'), h('ol', { class: 'timeline' }, d.grant_history.map((g) => h('li', {},
      h('div', {}, h('strong', {}, roleOf(g.role).name), g.revoked_at ? ' — removed' : ' — in force'),
      h('div', { class: 'when' }, `Given ${when(g.granted_at)} by ${nameOf(g.granted_by)}${g.revoked_at ? `; removed ${when(g.revoked_at)} by ${nameOf(g.revoked_by)}` : ''}`),
      h('div', { class: 'why' }, `“${g.revoke_reason || g.reason}”`))))));
  }
  if (d.history.length) parts.push(h('section', {}, h('h3', {}, 'Access history'), h('ol', { class: 'timeline' }, d.history.map((e) => accessEventLine(e, nameOf)))));
  openSheet(`Person ${personName(x)}`, [avatar(x.user_id, personName(x), 'lg'), h('div', {}, h('h2', {}, personName(x)),
    h('div', { class: 'sub' }, [x.job_title, x.user_id].filter(Boolean).join(' · ')),
    h('div', { class: 'meta inline' }, x.status === 'ACTIVE' ? stageChip(x.stage) : chip('DISABLED'), h('span', { class: 'chip' }, x.managed ? 'Managed here' : 'From sign-in')))], parts);
}

function roleSheet(code) {
  const me = PV.me;
  const r = code ? roleOf(code) : null;
  const editable = me.can_manage_roles && (!r || (r.kind === 'company' && r.status === 'ACTIVE'));
  const nameOf = (u) => { const p = PV.people.find((q) => q.user_id === u); return p ? personName(p) : u; };
  const parts = [];
  if (r) {
    const holders = PV.people.filter((x) => x.status === 'ACTIVE' && x.roles.includes(r.code));
    parts.push(h('p', { class: 'callout' }, r.kind === 'company' ? (r.description || 'A company role.') + (r.updated_by ? ` Last changed by ${nameOf(r.updated_by)} ${ago(r.updated_at)}.` : '') : 'A product role: fixed by SERK and tested never to hold both halves of a control. Give it to people; build a company role if it does not fit.'));
    parts.push(h('section', {}, h('h3', {}, `Held by (${holders.length})`), holders.length
      ? h('div', { class: 'rolechips' }, holders.map((x) => h('button', { type: 'button', class: 'ghost', on: { click: () => personSheet(x.user_id) } }, avatar(x.user_id, personName(x), 'sm'), personName(x))))
      : h('p', { class: 'hint' }, 'Nobody holds this role.')));
  }
  if (!editable) {
    parts.push(h('section', {}, h('h3', {}, `Permissions (${r.permissions.length})`), permGroups(r.permissions)));
  } else {
    const pick = new Set(r ? r.permissions : ['p2p.read']);
    const f = { code: input('code', { type: 'text', maxlength: '41', value: r ? r.code.replace(/^custom:/, '') : '', placeholder: 'site_buyer', disabled: !!r }),
      name: input('name', { type: 'text', maxlength: '80', value: r ? r.name : '', required: true }), description: input('description', { type: 'text', maxlength: '500', value: r ? r.description : '' }) };
    const reason = reasonInput('Why? For example: approved by the CFO, ticket ACC-12');
    const warn = h('div', { 'aria-live': 'polite' });
    const summary = h('p', { class: 'hint' });
    let groups;
    const check = () => {
      const bad = dutyConflicts(pick, PV.controls);
      warn.textContent = '';
      const badPerms = new Set(bad.flatMap((c) => c.perms));
      for (const l of groups.querySelectorAll('label[data-perm]')) l.classList.toggle('conflict', badPerms.has(l.dataset.perm));
      if (bad.length) warn.append(h('p', { class: 'callout bad' }, 'Not allowed: this role would hold both halves of ', bad.map((c) => c.control).join('; '), '. Untick one side.'));
      summary.textContent = `${pick.size} permission${pick.size === 1 ? '' : 's'} ticked.`;
    };
    groups = permGroups(null, pick, check);
    check();
    const form = h('form', { novalidate: true, on: { submit: async (ev) => {
      ev.preventDefault();
      try {
        const why = needReason(reason);
        const body = { code: f.code.value.trim(), name: f.name.value.trim(), description: f.description.value.trim(), permissions: [...pick].sort(), reason: why };
        let saved;
        await refreshAfter(async () => { saved = r ? await api('PUT', `/v1/roles/${encodeURIComponent(r.code)}`, { ...body, version: r.version }) : await api('POST', '/v1/roles', body); }, () => roleSheet(saved.code));
        say('Role saved.');
      } catch (e) { sheetError($('sheet').querySelector('.sheetbody'), e); }
    } } },
    h('div', { class: 'grid' }, field('Code', f.code, r ? 'Fixed once created.' : 'Lowercase letters, digits, underscores.'), field('Name', f.name), field('Description', f.description)),
    h('h3', {}, 'What people with this role may do'), summary, groups, warn,
    field('Reason (kept in the access history)', reason),
    h('div', { class: 'actions' }, h('button', { type: 'submit' }, r ? 'Save role' : 'Create role'),
      r ? h('button', { type: 'button', class: 'danger', on: { click: async () => {
        try { const why = needReason(reason); await refreshAfter(() => api('POST', `/v1/roles/${encodeURIComponent(r.code)}/retire`, { reason: why })); say('Role retired.'); } catch (e) { sheetError($('sheet').querySelector('.sheetbody'), e); }
      } } }, 'Retire role') : null));
    parts.push(form);
  }
  openSheet(r ? `Role ${r.name}` : 'New company role', [h('div', {}, h('div', { class: 'eyebrow' }, r ? (r.kind === 'company' ? 'Company role' : 'Product role') : 'New company role'),
    h('h2', {}, r ? r.name : 'Compose a role'), r ? h('div', { class: 'meta inline' }, stageChip(r.stage), h('span', { class: 'sub' }, `${r.permissions.length} permissions · ${r.code}`)) : null)], parts);
}

function addPersonSheet(v) {
  const f = { user_id: input('user_id', { type: 'text', maxlength: '200', required: true, placeholder: 'their identity-provider subject, e.g. selam@company.et' }),
    display_name: input('display_name', { type: 'text', maxlength: '200', required: true }), email: input('email', { type: 'email', maxlength: '254' }),
    job_title: input('job_title', { type: 'text', maxlength: '120' }), phone: input('phone', { type: 'tel', maxlength: '40' }) };
  const roles = new Set();
  const warn = h('div', { 'aria-live': 'polite' });
  const check = () => {
    const perms = new Set([...roles].flatMap((c) => roleOf(c).permissions));
    const bad = dutyConflicts(perms, v.controls);
    warn.textContent = '';
    warn.append(bad.length ? h('p', { class: 'callout bad' }, 'Will be refused: these roles together hold both halves of ', bad.map((c) => c.control).join('; '), '.')
      : h('p', { class: 'hint' }, `${roles.size} role${roles.size === 1 ? '' : 's'}, ${perms.size} permissions.`));
  };
  const roleBoxes = h('div', { class: 'permgroups' }, [['company', 'Company roles'], ['seed', 'Product roles']].map(([k, lbl]) => {
    const rs = v.roles.filter((r) => r.kind === k && r.status === 'ACTIVE');
    return rs.length ? h('fieldset', { class: 'permgroup' }, h('legend', {}, lbl), rs.map((r) => {
      const cb = h('input', { type: 'checkbox', value: r.code });
      cb.addEventListener('change', () => { if (cb.checked) roles.add(r.code); else roles.delete(r.code); check(); });
      return h('label', { class: `st-${r.stage}` }, cb, h('span', {}, r.name));
    })) : null;
  }));
  const projects = v.projects.map((p) => h('label', { class: 'check' }, h('input', { type: 'checkbox', value: p.id }), `${p.code} — ${p.name}`));
  const reason = reasonInput('Why? For example: new hire, HR ticket 41');
  check();
  openSheet('Add a person', [h('div', {}, h('div', { class: 'eyebrow' }, 'Directory'), h('h2', {}, 'Add a person'), h('div', { class: 'sub' }, 'Before their first sign-in; their access is managed here from the start.'))],
    h('form', { novalidate: true, on: { submit: async (ev) => {
      ev.preventDefault();
      try {
        const why = needReason(reason);
        const body = { user_id: f.user_id.value.trim(), display_name: f.display_name.value.trim(), email: f.email.value.trim(), job_title: f.job_title.value.trim(), phone: f.phone.value.trim(),
          roles: [...roles], project_ids: projects.map((l) => l.querySelector('input')).filter((c) => c.checked).map((c) => c.value), reason: why };
        await refreshAfter(() => api('POST', '/v1/people', body), () => personSheet(body.user_id));
        say('Person added.');
      } catch (e) { sheetError($('sheet').querySelector('.sheetbody'), e); }
    } } },
    h('div', { class: 'grid' }, field('Sign-in id', f.user_id, 'Exactly the subject their sign-in token carries.'), field('Name', f.display_name), field('Email', f.email), field('Job title', f.job_title), field('Phone', f.phone)),
    h('h3', {}, 'Roles'), roleBoxes, warn,
    h('h3', {}, 'Projects'), v.projects.length ? h('div', {}, projects) : h('p', { class: 'hint' }, 'No projects yet.'),
    field('Reason (kept in the access history)', reason),
    h('div', { class: 'actions' }, h('button', { type: 'submit' }, 'Add person'))));
}

function paintWho(name) {
  $('who-name').textContent = name;
  const av = $('who-av');
  av.textContent = initials(name);
}

// ---------- guarantees and price adjustment (0050) ----------
function svg(tag, attrs, ...kids) {
  const e = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) e.setAttribute(k, String(v));
  for (const k of kids) if (k != null) e.append(k);
  return e;
}
const GKIND = { BID: 'Bid', ADVANCE: 'Advance payment', PERFORMANCE: 'Performance', RETENTION: 'Retention' };
const GSTAND = { ACTIVE: 'In force', EXPIRING: 'Expires soon', EXPIRED: 'Expired', RELEASED: 'Released', CALLED: 'Called' };
const standChip = (s) => h('span', { class: `gstand gs-${s.toLowerCase()}` }, GSTAND[s] || words(s.toLowerCase()));
const monthName = (m) => { if (!m) return ''; const [y, mo] = m.split('-').map(Number); return new Date(Date.UTC(y, mo - 1, 1)).toLocaleDateString(undefined, { month: 'short', year: 'numeric', timeZone: 'UTC' }); };
const micro = (x) => Math.round(Number(x || 0) * 1e6);

async function renderBonds(tab) {
  closeSheet();
  const TABS = [['guarantees', 'Guarantees'], ['indices', 'Price indices'], ['escalation', 'Price adjustment']];
  let cur = TABS.some(([k]) => k === tab) ? tab : 'guarantees';
  const content = h('div', { role: 'tabpanel' });
  const tabs = h('div', { class: 'tabs', role: 'tablist', 'aria-label': 'Guarantees and price adjustment' });
  const show = async (k) => {
    cur = k;
    for (const b of tabs.children) b.setAttribute('aria-selected', String(b.dataset.k === k));
    history.replaceState(null, '', `#/bonds/${k}`);
    content.textContent = '';
    try {
      content.append(k === 'indices' ? await indicesTab() : k === 'escalation' ? await escalationTab() : await guaranteesTab());
    } catch (e) { showError(e); }
  };
  for (const [k, label] of TABS) tabs.append(h('button', { type: 'button', role: 'tab', 'data-k': k, on: { click: () => show(k) } }, label));
  page('Guarantees & price adjustment', 'The bank guarantees held against each contract, and the price adjustment the contract allows for changes in published price indices. Each value and decision is made by one person and approved by another.',
    tabs, content);
  await show(cur);
}

// Guarantees: register, standing, advance cover.
async function guaranteesTab() {
  const r = await api('GET', '/v1/guarantees');
  const gs = r.guarantees;
  const kpi = (n, k, warn) => h('div', { class: `kpi${warn && Number(n) ? ' warnk' : ''}` }, h('div', { class: 'v' }, String(n)), h('div', { class: 'k' }, k));
  const held = Object.entries(r.totals.held || {});
  const kpis = h('div', { class: 'kpis' }, kpi(r.totals.active, 'In force'), kpi(r.totals.expiring, `Expire within 30 days`, true), kpi(r.totals.expired, 'Expired, not released', true),
    held.length ? held.map(([ccy, v]) => kpi(money(v).replace(/\.00$/, ''), `Held (${ccy})`)) : kpi(0, 'Held'), kpi(r.totals.cover_shortfalls, 'Advance cover gaps', true));
  const cover = r.advance_cover.length ? h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Advance payment cover'),
    h('p', { class: 'hint' }, 'The advance the client paid, less what the certified payment certificates have recovered, should stay covered by an advance payment guarantee in force.'),
    r.advance_cover.map((a) => {
      const out = Number(a.advance_outstanding); const cov = Number(a.advance_cover);
      const pct = out > 0 ? Math.min(100, Math.round((cov / out) * 100)) : 100;
      const bar = h('div', { class: 'coverbar', role: 'img', 'aria-label': `${a.contract_number}: ${pct}% covered` }, h('span', { class: Number(a.shortfall) > 0 ? 'short' : 'full' }));
      bar.firstChild.style.width = `${pct}%`;
      return h('div', { class: 'coverrow' }, h('div', { class: 'ct' }, h('a', { href: `#/doc/contract/${a.contract_id}` }, a.contract_number)),
        h('div', { class: 'cf' }, `Received ${money(a.advance_received)} · recovered ${money(a.advance_recovered)} · outstanding `, h('strong', {}, money(a.advance_outstanding)),
          ` · guaranteed ${money(a.advance_cover)} ${a.currency}`), bar,
        Number(a.shortfall) > 0 ? h('div', { class: 'callout bad' }, `Shortfall ${money(a.shortfall)} ${a.currency}: ask the contractor's bank for an advance guarantee or extend the one in force.`) : null);
    })) : null;

  const find = h('input', { type: 'search', class: 'findbox', placeholder: 'Find by bank, reference, contract or number', 'aria-label': 'Find a guarantee', autocomplete: 'off' });
  const FILTERS = [['open', 'In force', (g) => g.status === 'ACTIVE'], ['EXPIRING', 'Expiring', (g) => g.standing === 'EXPIRING'], ['EXPIRED', 'Expired', (g) => g.standing === 'EXPIRED'],
    ['closed', 'Released or called', (g) => g.status !== 'ACTIVE'], ['all', 'All', () => true]];
  let filter = 'open';
  let view = 'cards';
  const kindSel = select('kind', [{ value: '', label: 'Every kind' }, ...Object.entries(GKIND).map(([value, label]) => ({ value, label }))]);
  const pills = h('div', { class: 'pills', role: 'group', 'aria-label': 'Show' });
  const viewp = h('div', { class: 'pills', role: 'group', 'aria-label': 'View' });
  const out = h('div');
  const draw = () => {
    for (const b of pills.children) b.setAttribute('aria-pressed', String(b.dataset.k === filter));
    for (const b of viewp.children) b.setAttribute('aria-pressed', String(b.dataset.k === view));
    const q = find.value.trim().toLowerCase();
    const test = FILTERS.find(([k]) => k === filter)[2];
    const rows = gs.filter((g) => test(g) && (!kindSel.value || g.kind === kindSel.value) && (!q || [g.number, g.issuer, g.reference, g.contract_number].join(' ').toLowerCase().includes(q)));
    out.textContent = '';
    if (!rows.length) { out.append(h('p', { class: 'empty panel' }, gs.length ? 'No guarantee matches.' : 'No guarantee is registered yet.')); return; }
    if (view === 'cards') { out.append(h('div', { class: 'pgrid' }, rows.map(guaranteeCard))); return; }
    out.append(table(`${rows.length} guarantees`, [
      { label: 'Number', cell: (g) => h('button', { type: 'button', class: 'linkbtn', on: { click: () => guaranteeSheet(g) } }, g.number) },
      { label: 'Kind', cell: (g) => GKIND[g.kind] },
      { label: 'Contract', cell: (g) => g.contract_number },
      { label: 'Bank and reference', cell: (g) => `${g.issuer} — ${g.reference}` },
      { label: 'Value', num: true, cell: (g) => `${money(g.amount)} ${g.currency}` },
      { label: 'Expires', cell: (g) => g.expires_on },
      { label: 'Days left', num: true, cell: (g) => (g.status === 'ACTIVE' ? String(g.days_left) : '') },
      { label: 'Standing', cell: (g) => standChip(g.standing) },
    ], rows));
  };
  for (const [k, label] of FILTERS) pills.append(h('button', { type: 'button', 'data-k': k, on: { click: () => { filter = k; draw(); } } }, label));
  for (const [k, label] of [['cards', 'Cards'], ['table', 'Table']]) viewp.append(h('button', { type: 'button', 'data-k': k, on: { click: () => { view = k; draw(); } } }, label));
  find.addEventListener('input', draw);
  kindSel.addEventListener('change', draw);
  draw();
  const tools = h('div', { class: 'toolbar' },
    S.can.has('bond.manage') ? h('button', { type: 'button', on: { click: registerGuaranteeSheet } }, 'Register a guarantee') : null,
    !S.can.has('bond.manage') && !S.can.has('bond.approve') ? h('span', { class: 'hint' }, 'Treasury registers guarantees; a commercial manager releases or records a call.') : null);
  return h('div', {}, tools, kpis, cover, h('div', { class: 'toolbar' }, find, pills, kindSel, h('span', { class: 'spacer' }), viewp), out);
}

function lifeBar(g) {
  const start = Date.parse(g.issued_on); const end = Date.parse(g.expires_on);
  const pct = Math.max(0, Math.min(100, Math.round(((Date.now() - start) / Math.max(1, end - start)) * 100)));
  const bar = h('div', { class: `lifebar gs-${g.standing.toLowerCase()}`, role: 'img', 'aria-label': `${pct}% of its term elapsed` }, h('span'));
  bar.firstChild.style.width = `${g.status === 'ACTIVE' ? pct : 100}%`;
  return bar;
}

function guaranteeCard(g) {
  const left = g.status !== 'ACTIVE' ? (g.status === 'CALLED' ? `Called ${money(g.called_amount)}` : 'Returned to the bank')
    : g.days_left < 0 ? `Expired ${-g.days_left} day${g.days_left === -1 ? '' : 's'} ago` : `${g.days_left} day${g.days_left === 1 ? '' : 's'} left`;
  return h('button', { type: 'button', class: `pcard gcard gs-${g.standing.toLowerCase()}`, 'aria-label': `Open guarantee ${g.number}`, on: { click: () => guaranteeSheet(g) } },
    h('div', { class: 'row spread' }, h('span', { class: 'rolechip' }, GKIND[g.kind]), standChip(g.standing)),
    h('div', {}, h('div', { class: 'gamt' }, money(g.amount), h('span', { class: 'ccy' }, ` ${g.currency}`)),
      g.amount !== g.original_amount ? h('div', { class: 'sub' }, `reduced from ${money(g.original_amount)}`) : null),
    h('div', { class: 'pwho' }, h('div', { class: 'nm' }, g.issuer), h('div', { class: 'sub' }, `${g.reference} · ${g.contract_number}`)),
    lifeBar(g),
    h('div', { class: 'meta' }, h('span', {}, `${g.issued_on} → ${g.expires_on}`), h('span', {}, '·'), h('strong', {}, left)));
}

async function registerGuaranteeSheet() {
  const ctr = select('contract_id', await docOpts('contract', 'ACTIVE'), { required: true });
  const kind = select('kind', Object.entries(GKIND).map(([value, label]) => ({ value, label })), { required: true });
  const issuer = input('issuer', { type: 'text', required: true, maxlength: '200', placeholder: 'The issuing bank' });
  const ref = input('reference', { type: 'text', required: true, maxlength: '100', placeholder: "The bank's reference" });
  const amount = input('amount', { inputmode: 'decimal', required: true, autocomplete: 'off' });
  const ccy = input('currency', { type: 'text', maxlength: '3', placeholder: "Contract's currency", autocomplete: 'off' });
  const issued = input('issued_on', { type: 'date', required: true, value: today() });
  const expires = input('expires_on', { type: 'date', required: true, value: plusDays(365) });
  const save = h('button', { type: 'button', class: 'primary' }, 'Register');
  const sheet = openSheet('Register a guarantee', h('div', {}, h('h2', {}, 'Register a guarantee'), h('p', { class: 'hint' }, 'As received from the bank. Its value can later only be reduced and its expiry only extended.')),
    h('div', { class: 'grid' }, field('Contract', ctr), field('Kind', kind), field('Bank', issuer), field('Bank reference', ref), field('Amount', amount), field('Currency', ccy, 'Leave empty for the contract currency'),
      field('Issued on', issued), field('Expires on', expires)), h('div', { class: 'actions' }, save));
  save.addEventListener('click', async () => {
    try {
      await api('POST', '/v1/guarantees', { contract_id: ctr.value, kind: kind.value, issuer: issuer.value.trim(), reference: ref.value.trim(), amount: amount.value.trim(),
        currency: ccy.value.trim(), issued_on: issued.value, expires_on: expires.value });
      closeSheet(); say('Guarantee registered.'); renderBonds('guarantees');
    } catch (e) { sheetError(sheet, e); }
  });
}

function guaranteeSheet(g) {
  const dl = h('dl', { class: 'kv' });
  for (const [k, v] of [['Contract', h('a', { href: `#/doc/contract/${g.contract_id}` }, g.contract_number)], ['Kind', GKIND[g.kind]], ['Bank', g.issuer], ['Bank reference', g.reference],
    ['Face value', `${money(g.original_amount)} ${g.currency}`], ['Current value', `${money(g.amount)} ${g.currency}`], ['Issued', g.issued_on], ['Expires', g.expires_on],
    ['Registered by', g.created_by], ['Changes', String(g.version - 1)],
    g.closed_by ? [g.status === 'CALLED' ? 'Called' : 'Released', `${g.closed_by} — ${g.close_reference}: ${g.close_reason}`] : null,
    g.called_amount ? ['Amount called', `${money(g.called_amount)} ${g.currency}`] : null].filter(Boolean)) dl.append(h('dt', {}, k), h('dd', {}, v));
  const parts = [lifeBar(g), dl];
  const sheet = openSheet(`Guarantee ${g.number}`, h('div', {}, h('h2', {}, `${GKIND[g.kind]} guarantee ${g.number}`), h('p', {}, standChip(g.standing))), ...parts,
    h('p', {}, h('a', { href: `#/doc/guarantee/${g.id}` }, 'Open as a document')));
  if (g.status !== 'ACTIVE') return;
  if (S.can.has('bond.manage')) {
    const amt = input('amount', { inputmode: 'decimal', autocomplete: 'off', placeholder: `Now ${money(g.amount)}` });
    const exp = input('expires_on', { type: 'date', min: g.expires_on });
    const why = reasonInput('e.g. Bank amendment no. 2');
    const b = h('button', { type: 'button' }, 'Record the change');
    b.addEventListener('click', async () => {
      try {
        const body = { reason: needReason(why) };
        if (amt.value.trim()) body.amount = amt.value.trim();
        if (exp.value) body.expires_on = exp.value;
        await api('POST', `/v1/guarantees/${g.id}/change`, body);
        closeSheet(); say('Guarantee changed.'); renderBonds('guarantees');
      } catch (e) { sheetError(sheet, e); }
    });
    sheet.querySelector('.sheetbody').append(h('div', { class: 'panel' }, h('h3', {}, 'Reduce or extend'), h('p', { class: 'hint' }, 'A lower value or a later expiry, from the bank’s amendment.'),
      h('div', { class: 'grid' }, field('New value', amt), field('New expiry', exp), field('Reason', why)), h('div', { class: 'actions' }, b)));
  }
  if (S.can.has('bond.approve')) {
    const outcome = select('outcome', [{ value: 'RELEASED', label: 'Released to the bank' }, { value: 'CALLED', label: 'Called by the client' }]);
    const ref = input('reference', { type: 'text', maxlength: '100', placeholder: 'Letter reference' });
    const called = input('called_amount', { inputmode: 'decimal', autocomplete: 'off', placeholder: `Up to ${money(g.amount)}` });
    const calledField = field('Amount called', called);
    const why = reasonInput('e.g. Taking-over certificate issued');
    const sync = () => { calledField.hidden = outcome.value !== 'CALLED'; };
    outcome.addEventListener('change', sync); sync();
    const b = h('button', { type: 'button', class: 'primary' }, 'Record');
    b.addEventListener('click', async () => {
      try {
        const body = { outcome: outcome.value, reference: ref.value.trim(), reason: needReason(why) };
        if (outcome.value === 'CALLED') body.called_amount = called.value.trim();
        await api('POST', `/v1/guarantees/${g.id}/close`, body);
        closeSheet(); say(outcome.value === 'CALLED' ? 'Call recorded.' : 'Guarantee released.'); renderBonds('guarantees');
      } catch (e) { sheetError(sheet, e); }
    });
    sheet.querySelector('.sheetbody').append(h('div', { class: 'panel' }, h('h3', {}, 'Release or record a call'),
      h('p', { class: 'hint' }, g.created_by === S.me.user_id ? 'You registered this guarantee, so another person must decide it.' : 'Needs a sign-in with a second factor in the last 10 minutes. It cannot be undone.'),
      h('div', { class: 'grid' }, field('What happened', outcome), field('Reference', ref), calledField, field('Reason', why)), h('div', { class: 'actions' }, b)));
  }
}

// Price indices: series, approved values, values waiting for approval.
function sparkline(values) {
  const pts = values.filter((v) => v.status === 'APPROVED').slice().sort((a, b) => a.period.localeCompare(b.period));
  if (pts.length < 2) return h('div', { class: 'spark empty' }, pts.length ? 'One approved value' : 'No approved value yet');
  const W = 240; const H = 56; const nums = pts.map((p) => Number(p.value));
  const lo = Math.min(...nums); const hi = Math.max(...nums); const span = hi - lo || 1;
  const xy = nums.map((n, i) => [4 + (i * (W - 8)) / (nums.length - 1), H - 6 - ((n - lo) / span) * (H - 14)]);
  const line = xy.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(' ');
  const [lx, ly] = xy[xy.length - 1];
  return svg('svg', { viewBox: `0 0 ${W} ${H}`, class: 'spark', role: 'img', 'aria-label': `${pts.length} approved values from ${monthName(pts[0].period)} (${pts[0].value}) to ${monthName(pts[pts.length - 1].period)} (${pts[pts.length - 1].value})` },
    svg('polyline', { points: `4,${H - 2} ${line} ${W - 4},${H - 2}`, class: 'area' }), svg('polyline', { points: line, class: 'ln' }), svg('circle', { cx: lx, cy: ly, r: 3.5, class: 'pt' }));
}

async function indicesTab() {
  const r = await api('GET', '/v1/price-indices');
  const manage = S.can.has('escalation.manage'); const approve = S.can.has('escalation.approve');
  const pending = r.indices.flatMap((x) => x.values.filter((v) => v.status === 'SUBMITTED' || v.status === 'DRAFT').map((v) => ({ ...v, code: x.code, name: x.name })))
    .sort((a, b) => a.code.localeCompare(b.code) || a.period.localeCompare(b.period));
  const decide = async (v, ok) => {
    try {
      let reason = '';
      if (!ok) { reason = (prompt(`Why is ${v.code} ${monthName(v.period)} rejected?`) || '').trim(); if (reason.length < 5) throw new ApiError(422, 'validation', 'A rejection needs a reason of at least 5 characters.'); }
      await api('POST', `/v1/price-index-values/${v.id}/${ok ? 'approve' : 'reject'}`, { reason });
      say(ok ? 'Index value approved.' : 'Index value rejected.'); renderBonds('indices');
    } catch (e) { showError(e); }
  };
  const submit = async (v) => { try { await api('POST', `/v1/price-index-values/${v.id}/submit`, {}); say('Submitted for approval.'); renderBonds('indices'); } catch (e) { showError(e); } };
  const queue = pending.length ? h('div', { class: 'panel' }, h('h2', { class: 'section' }, `Waiting (${pending.length})`),
    table('Index values not yet approved', [
      { label: 'Index', cell: (v) => `${v.code} — ${v.name}` }, { label: 'Month', cell: (v) => monthName(v.period) }, { label: 'Value', num: true, cell: (v) => v.value },
      { label: 'Source', cell: (v) => v.source_ref }, { label: 'Proposed by', cell: (v) => v.created_by }, { label: 'Status', cell: (v) => chip(v.status) },
      { label: '', cell: (v) => h('div', { class: 'rowacts' },
        v.status === 'DRAFT' && manage && v.created_by === S.me.user_id ? h('button', { type: 'button', 'aria-label': `Submit ${v.code} ${monthName(v.period)}`, on: { click: () => submit(v) } }, 'Submit') : null,
        v.status === 'SUBMITTED' && approve && v.created_by !== S.me.user_id ? [h('button', { type: 'button', class: 'primary', 'aria-label': `Approve ${v.code} ${monthName(v.period)}`, on: { click: () => decide(v, true) } }, 'Approve'),
          h('button', { type: 'button', class: 'ghost', 'aria-label': `Reject ${v.code} ${monthName(v.period)}`, on: { click: () => decide(v, false) } }, 'Reject')] : null) },
    ], pending)) : null;
  const cards = r.indices.length ? h('div', { class: 'pgrid' }, r.indices.map((x) => {
    const ok = x.values.filter((v) => v.status === 'APPROVED');
    const last = ok[0]; const prev = ok[1];
    const chg = last && prev ? ((Number(last.value) / Number(prev.value)) - 1) * 100 : null;
    return h('div', { class: 'pcard icard' },
      h('div', { class: 'row spread' }, h('span', { class: 'rolechip' }, x.code), h('span', { class: 'sub' }, x.source)),
      h('div', { class: 'nm' }, x.name),
      h('div', { class: 'row' }, h('div', { class: 'gamt' }, last ? last.value : '—'),
        h('div', { class: 'sub' }, last ? monthName(last.period) : 'no approved value', chg != null ? h('span', { class: `delta ${chg >= 0 ? 'up' : 'down'}` }, ` ${chg >= 0 ? '▲' : '▼'} ${Math.abs(chg).toFixed(2)}%`) : null)),
      sparkline(x.values),
      h('div', { class: 'meta' }, `${ok.length} approved month${ok.length === 1 ? '' : 's'}`));
  })) : h('p', { class: 'empty panel' }, 'No price index yet. A quantity surveyor adds the indices the contracts name (for example cement, steel, fuel, labour).');
  const tools = h('div', { class: 'toolbar' },
    manage ? h('button', { type: 'button', on: { click: () => indexValueSheet(r.indices) } }, 'Propose a monthly value') : null,
    manage ? h('button', { type: 'button', class: 'ghost', on: { click: newIndexSheet } }, 'Add an index') : null,
    !manage ? h('span', { class: 'hint' }, approve ? 'You approve the values others propose.' : 'Values are proposed by a quantity surveyor and approved by a commercial manager.') : null);
  return h('div', {}, tools, queue, cards);
}

function newIndexSheet() {
  const code = input('code', { type: 'text', required: true, maxlength: '30', placeholder: 'e.g. CEM', autocomplete: 'off' });
  const name = input('name', { type: 'text', required: true, maxlength: '200', placeholder: 'e.g. Cement price index' });
  const src = input('source', { type: 'text', required: true, maxlength: '200', placeholder: 'Who publishes it' });
  const b = h('button', { type: 'button', class: 'primary' }, 'Add index');
  const sheet = openSheet('Add a price index', h('h2', {}, 'Add a price index'), h('div', { class: 'grid' }, field('Code', code, 'Capital letters and digits'), field('Name', name), field('Published by', src)), h('div', { class: 'actions' }, b));
  b.addEventListener('click', async () => {
    try { await api('POST', '/v1/price-indices', { code: code.value.trim().toUpperCase(), name: name.value.trim(), source: src.value.trim() }); closeSheet(); say('Index added.'); renderBonds('indices'); } catch (e) { sheetError(sheet, e); }
  });
}

function indexValueSheet(indices) {
  if (!indices.length) { showError(new ApiError(422, 'validation', 'Add an index first.')); return; }
  const idx = select('index_code', indices.map((x) => ({ value: x.code, label: `${x.code} — ${x.name}` })));
  const lastMonth = new Date(); lastMonth.setDate(1); lastMonth.setMonth(lastMonth.getMonth() - 1);
  const period = input('period', { type: 'month', required: true, value: lastMonth.toISOString().slice(0, 7), max: new Date().toISOString().slice(0, 7) });
  const value = input('value', { inputmode: 'decimal', required: true, autocomplete: 'off' });
  const src = input('source_ref', { type: 'text', required: true, maxlength: '200', placeholder: 'e.g. CSA bulletin no. 412' });
  const b = h('button', { type: 'button', class: 'primary' }, 'Propose and submit');
  const sheet = openSheet('Propose an index value', h('div', {}, h('h2', {}, 'Propose an index value'), h('p', { class: 'hint' }, 'From the published bulletin. Another person approves it; once approved it never changes, and a month has one value.')),
    h('div', { class: 'grid' }, field('Index', idx), field('Month', period), field('Value', value), field('Source', src)), h('div', { class: 'actions' }, b));
  b.addEventListener('click', async () => {
    try {
      const res = await api('POST', '/v1/price-index-values', { index_code: idx.value, period: period.value, value: value.value.trim(), source_ref: src.value.trim() });
      await api('POST', `/v1/price-index-values/${res.id}/submit`, {});
      closeSheet(); say('Value proposed and submitted for approval.'); renderBonds('indices');
    } catch (e) { sheetError(sheet, e); }
  });
}

// Price adjustment: a contract's formula, its certificates and adjustments.
let ESC_CONTRACT = '';
async function escalationTab() {
  const opts = await docOpts('contract', 'ACTIVE');
  if (!opts.length) return h('p', { class: 'empty panel' }, 'No active contract.');
  if (!opts.some((o) => o.value === ESC_CONTRACT)) ESC_CONTRACT = opts[0].value;
  const ctr = select('contract', opts);
  ctr.value = ESC_CONTRACT;
  const body = h('div');
  const load = async () => {
    ESC_CONTRACT = ctr.value;
    body.textContent = '';
    try { body.append(await escalationBody(ctr.value)); } catch (e) { showError(e); }
  };
  ctr.addEventListener('change', load);
  await load();
  return h('div', {}, h('div', { class: 'toolbar' }, field('Contract', ctr)), body);
}

const IDX_TONES = 6;
function formulaView(f) {
  const terms = f.terms || [];
  const eq = h('p', { class: 'formula', 'aria-label': 'Price adjustment formula' }, h('var', {}, 'P'), h('sub', {}, 'n'), ' = ', h('b', {}, f.fixed_portion),
    terms.map((t) => [' + ', h('b', {}, t.weight), ' × ', h('var', {}, t.index_code), h('sub', {}, 'n'), ' / ', h('var', {}, t.index_code), h('sub', {}, '0')]));
  const bar = h('div', { class: 'wbar', role: 'img', 'aria-label': `Fixed ${f.fixed_portion}; ${terms.map((t) => `${t.index_code} ${t.weight}`).join(', ')}` });
  const legend = h('div', { class: 'flowlegend' });
  const seg = (w, cls, label) => { const s = h('span', { class: cls, title: label }); s.style.flexGrow = String(micro(w)); bar.append(s); legend.append(h('span', { class: `wkey ${cls}` }, label)); };
  seg(f.fixed_portion, 'w-fixed', `Fixed ${(Number(f.fixed_portion) * 100).toFixed(1)}%`);
  terms.forEach((t, i) => seg(t.weight, `w-${i % IDX_TONES}`, `${t.index_code} ${(Number(t.weight) * 100).toFixed(1)}%${t.name ? ' — ' + t.name : ''}`));
  return h('div', {}, eq, bar, legend);
}

async function escalationBody(contract) {
  const [e, ix] = await Promise.all([api('GET', `/v1/escalation?contract=${contract}`), api('GET', '/v1/price-indices')]);
  const manage = S.can.has('escalation.manage');
  const f = e.formula;
  const parts = [];
  if (f) {
    parts.push(h('div', { class: 'panel' }, h('div', { class: 'row spread' }, h('h2', { class: 'section' }, `Formula ${f.number}`), chip(f.status)),
      formulaView(f),
      h('p', { class: 'hint' }, `Clause ${f.clause_ref} · base month ${monthName(f.base_period)} · prepared by ${f.created_by}${f.approved_by ? ' · approved by ' + f.approved_by : ''}`),
      f.status !== 'APPROVED' ? h('p', {}, h('a', { href: `#/doc/escalation_formula/${f.id}` }, f.status === 'DRAFT' ? 'Open it to submit for approval' : 'Open it to approve or reject')) : null));
  } else if (manage) {
    parts.push(formulaComposer(contract, ix.indices));
  } else {
    parts.push(h('p', { class: 'empty panel' }, 'This contract has no price adjustment formula. A quantity surveyor sets it from the contract’s clause; a commercial manager approves it.'));
  }
  const kpi = (v, k) => h('div', { class: 'kpi' }, h('div', { class: 'v' }, v), h('div', { class: 'k' }, k));
  parts.push(h('div', { class: 'kpis' }, kpi(String(e.certificates.length), 'Certified certificates'), kpi(String(e.certificates.filter((c) => c.adjusted).length), 'With an adjustment'),
    kpi(money(e.approved_total).replace(/\.00$/, ''), 'Approved adjustment')));
  const ready = f && f.status === 'APPROVED' && manage;
  const preview = h('div', { 'aria-live': 'polite' });
  parts.push(h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Certified payment certificates'),
    table('Work certified in each period, the base of its adjustment', [
      { label: 'Certificate', cell: (c) => h('a', { href: `#/doc/ipc/${c.id}` }, c.number) }, { label: 'Period to', cell: (c) => c.period_end },
      { label: 'Work this period', num: true, cell: (c) => money(c.this_gross) }, { label: 'Status', cell: (c) => chip(c.status) },
      { label: 'Adjustment', cell: (c) => (c.adjusted ? 'Prepared' : ready ? adjustControls(c, f, preview, contract) : h('span', { class: 'hint' }, f && f.status === 'APPROVED' ? 'Not yet' : 'Needs an approved formula')) },
    ], e.certificates, 'No certified certificate yet.'), preview));
  if (e.adjustments.length) {
    parts.push(h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Price adjustments'),
      table(`${e.adjustments.length} adjustment(s)`, [
        { label: 'Number', cell: (a) => h('a', { href: `#/doc/price_adjustment/${a.id}` }, a.number) }, { label: 'Certificate', cell: (a) => a.ipc_number },
        { label: 'Indices of', cell: (a) => monthName(a.current_period) }, { label: 'Base', num: true, cell: (a) => money(a.base_value) },
        { label: 'Factor', num: true, cell: (a) => a.factor }, { label: 'Adjustment', num: true, cell: (a) => h('strong', {}, money(a.amount)) },
        { label: 'Status', cell: (a) => chip(a.status) },
      ], e.adjustments)));
  }
  return h('div', {}, parts);
}

function adjustControls(c, f, preview, contract) {
  const m = input('period', { type: 'month', value: c.period_end.slice(0, 7), min: f.base_period, max: c.period_end.slice(0, 7), 'aria-label': `Index month for ${c.number}` });
  const b = h('button', { type: 'button' }, 'Compute');
  b.addEventListener('click', async () => {
    try {
      clearError();
      const p = await api('GET', `/v1/price-adjustments/preview?ipc=${c.id}&period=${m.value}`);
      preview.textContent = '';
      const go = h('button', { type: 'button', class: 'primary' }, 'Prepare and submit for approval');
      go.addEventListener('click', async () => {
        try {
          const res = await api('POST', '/v1/price-adjustments', { ipc_id: c.id, period: m.value });
          await api('POST', `/v1/price-adjustments/${res.id}/submit`, {});
          say('Price adjustment prepared and submitted for approval.'); renderBonds('escalation');
        } catch (err) { showError(err); }
      });
      preview.append(h('div', { class: 'adjpreview' }, h('h3', {}, `${c.number} with indices of ${monthName(p.current_period)}`),
        table('What each index contributes', [
          { label: 'Index', cell: (t) => t.index }, { label: 'Weight b', num: true, cell: (t) => t.weight }, { label: 'Base L₀', num: true, cell: (t) => t.base_value },
          { label: 'Current Lₙ', num: true, cell: (t) => t.current_value }, { label: 'Lₙ / L₀', num: true, cell: (t) => t.ratio },
          { label: 'b × Lₙ / L₀', num: true, cell: (t) => (Number(t.weight) * Number(t.current_value) / Number(t.base_value)).toFixed(6) },
        ], p.terms),
        h('div', { class: 'adjsum' }, h('div', {}, h('span', { class: 'k' }, 'Fixed a'), h('span', { class: 'v' }, f.fixed_portion)),
          h('div', {}, h('span', { class: 'k' }, 'Factor Pₙ'), h('span', { class: 'v' }, p.factor)),
          h('div', {}, h('span', { class: 'k' }, 'Work this period'), h('span', { class: 'v' }, money(p.base_value))),
          h('div', { class: Number(p.amount) < 0 ? 'neg' : 'pos' }, h('span', { class: 'k' }, 'Adjustment = work × (Pₙ − 1)'), h('span', { class: 'v' }, `${money(p.amount)} ${p.currency}`))),
        h('p', { class: 'hint' }, 'Computed by the database from approved index values only; it is computed again when it is approved.'), h('div', { class: 'actions' }, go)));
      preview.scrollIntoView({ block: 'nearest' });
    } catch (err) { showError(err); }
  });
  return h('div', { class: 'rowacts' }, m, b);
}

function formulaComposer(contract, indices) {
  const clause = input('clause_ref', { type: 'text', maxlength: '100', value: '13.8', 'aria-label': 'Clause' });
  const fixed = input('fixed_portion', { inputmode: 'decimal', value: '0.15', autocomplete: 'off', 'aria-label': 'Fixed portion a' });
  const base = input('base_period', { type: 'month', 'aria-label': 'Base month', max: new Date().toISOString().slice(0, 7) });
  const rows = h('div', { class: 'termrows' });
  const total = h('div', { class: 'callout', role: 'status' });
  const live = h('div');
  const terms = () => [...rows.children].map((r) => ({ index_code: r.querySelector('select').value, weight: r.querySelector('input').value.trim() })).filter((t) => t.index_code && t.weight);
  const paint = () => {
    const sum = micro(fixed.value) + terms().reduce((s, t) => s + micro(t.weight), 0);
    const ok = sum === 1e6;
    total.className = `callout ${ok ? 'ok' : 'bad'}`;
    total.textContent = `a + Σb = ${(sum / 1e6).toFixed(6)} ${ok ? '— adds up to exactly 1' : '— must add up to exactly 1'}`;
    live.textContent = '';
    live.append(formulaView({ fixed_portion: fixed.value || '0', terms: terms().map((t) => ({ ...t, name: (indices.find((x) => x.code === t.index_code) || {}).name })) }));
  };
  const addRow = (code, w) => {
    const sel = select('index', [{ value: '', label: 'Choose an index' }, ...indices.map((x) => ({ value: x.code, label: `${x.code} — ${x.name}` }))]);
    sel.value = code || '';
    const wt = input('weight', { inputmode: 'decimal', value: w || '', autocomplete: 'off', placeholder: 'Weight b', 'aria-label': 'Weight' });
    const rm = h('button', { type: 'button', class: 'ghost', 'aria-label': 'Remove this index' }, 'Remove');
    const row = h('div', { class: 'termrow' }, sel, wt, rm);
    rm.addEventListener('click', () => { row.remove(); paint(); });
    sel.addEventListener('change', paint); wt.addEventListener('input', paint);
    rows.append(row);
  };
  fixed.addEventListener('input', paint);
  if (!indices.length) return h('p', { class: 'empty panel' }, 'Add the price indices the contract names first (Price indices tab), then set the formula here.');
  addRow(indices[0].code, '');
  const more = h('button', { type: 'button', class: 'ghost', on: { click: () => { addRow('', ''); paint(); } } }, 'Add an index');
  const save = h('button', { type: 'button', class: 'primary' }, 'Save formula');
  const box = h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Set the price adjustment formula'),
    h('p', { class: 'hint' }, 'From the contract’s price adjustment clause: Pₙ = a + Σ b × Lₙ / L₀. The fixed portion a and the weights b must add up to exactly 1, and every index needs an approved value for the base month (usually the month before tenders were due).'),
    h('div', { class: 'grid' }, field('Clause', clause), field('Fixed portion a', fixed), field('Base month', base)), rows, more, total, live, h('div', { class: 'actions' }, save));
  save.addEventListener('click', async () => {
    try {
      clearError();
      const res = await api('POST', '/v1/escalation/formulas', { contract_id: contract, clause_ref: clause.value.trim(), fixed_portion: fixed.value.trim(), base_period: base.value, terms: terms() });
      say('Formula saved as a draft. Open it to submit for approval.');
      location.hash = `#/doc/escalation_formula/${res.id}`;
    } catch (e) { showError(e); }
  });
  paint();
  return box;
}

// ---------- contract time, liquidated damages, obligations (0051) ----------
const OSTAND = { OPEN: 'Open', DUE_SOON: 'Due within 7 days', OVERDUE: 'Overdue', DONE: 'Done', WAIVED: 'Waived' };
const OCLS = { OPEN: 'active', DUE_SOON: 'expiring', OVERDUE: 'expired', DONE: 'released', WAIVED: 'released' };
const PARTY = { CONTRACTOR: 'Contractor', CLIENT: 'Client', ENGINEER: 'Engineer' };
const obligChip = (s) => h('span', { class: `gstand gs-${OCLS[s] || 'active'}` }, OSTAND[s] || words(s.toLowerCase()));

function obligationsTable(list, after) {
  return table(`${list.length} obligation(s)`, [
    { label: 'Number', cell: (o) => o.number }, { label: 'Contract', cell: (o) => h('a', { href: `#/doc/contract/${o.contract_id}` }, o.contract_number) },
    { label: 'Clause', cell: (o) => o.clause_ref }, { label: 'What', cell: (o) => o.description }, { label: 'Party', cell: (o) => PARTY[o.party] },
    { label: 'Owner', cell: (o) => o.owner }, { label: 'Due', cell: (o) => o.due_on },
    { label: 'Standing', cell: (o) => [obligChip(o.standing), o.status === 'OPEN' ? h('span', { class: 'hint' }, ` ${o.days_left < 0 ? -o.days_left + ' days late' : o.days_left + ' days'}`) : h('span', { class: 'hint' }, ` ${o.closed_on} — ${o.close_note}`)] },
    { label: '', cell: (o) => (o.status === 'OPEN' && S.can.has('claim.manage') ? h('button', { type: 'button', class: 'ghost', 'aria-label': `Close ${o.number}`, on: { click: () => closeObligationSheet(o, after) } }, 'Close…') : null) },
  ], list, 'No obligation recorded.');
}

function closeObligationSheet(o, after) {
  const st = select('status', [{ value: 'DONE', label: 'Done' }, { value: 'WAIVED', label: 'Waived' }]);
  const on = input('closed_on', { type: 'date', value: today(), max: today() });
  const note = input('note', { type: 'text', maxlength: '500', placeholder: 'How it was met, or why it is waived' });
  const b = h('button', { type: 'button', class: 'primary' }, 'Close obligation');
  const sheet = openSheet(`Close ${o.number}`, h('div', {}, h('h2', {}, `Close ${o.number}`), h('p', { class: 'hint' }, o.description)),
    h('div', { class: 'grid' }, field('Outcome', st), field('On', on), field('Note', note, 'A waiver needs its reason (5+ characters)')), h('div', { class: 'actions' }, b));
  b.addEventListener('click', async () => {
    try { await api('POST', `/v1/obligations/${o.id}/close`, { status: st.value, closed_on: on.value, note: note.value.trim() }); closeSheet(); say('Obligation closed.'); after(); } catch (e) { sheetError(sheet, e); }
  });
}

function addObligationSheet(contracts, after) {
  const ctr = select('contract_id', contracts, { required: true });
  const clause = input('clause_ref', { type: 'text', maxlength: '100', placeholder: 'e.g. 8.3' });
  const desc = input('description', { type: 'text', maxlength: '500', placeholder: 'e.g. Submit the revised programme' });
  const party = select('party', Object.entries(PARTY).map(([value, label]) => ({ value, label })));
  const owner = input('owner', { type: 'text', maxlength: '200', placeholder: 'Sign-in id (empty: you)', autocomplete: 'off' });
  const due = input('due_on', { type: 'date', value: plusDays(14) });
  const b = h('button', { type: 'button', class: 'primary' }, 'Add obligation');
  const sheet = openSheet('Add an obligation', h('div', {}, h('h2', {}, 'Add an obligation'), h('p', { class: 'hint' }, 'What the contract requires, of whom, by when. Alert rules can warn the owner at 14, 7 and 2 days.')),
    h('div', { class: 'grid' }, field('Contract', ctr), field('Clause', clause), field('What', desc), field('Whose obligation', party), field('Owner here', owner), field('Due on', due)), h('div', { class: 'actions' }, b));
  b.addEventListener('click', async () => {
    try {
      await api('POST', '/v1/obligations', { contract_id: ctr.value, clause_ref: clause.value.trim(), description: desc.value.trim(), party: party.value, owner: owner.value.trim(), due_on: due.value });
      closeSheet(); say('Obligation added.'); after();
    } catch (e) { sheetError(sheet, e); }
  });
}

async function renderObligations() {
  closeSheet();
  const r = await api('GET', '/v1/obligations');
  const list = r.obligations;
  const kpi = (n, k, warn) => h('div', { class: `kpi${warn && n ? ' warnk' : ''}` }, h('div', { class: 'v' }, String(n)), h('div', { class: 'k' }, k));
  const open = list.filter((o) => o.status === 'OPEN');
  const FILTERS = [['open', 'Open', (o) => o.status === 'OPEN'], ['soon', 'Due within 7 days', (o) => o.standing === 'DUE_SOON'], ['late', 'Overdue', (o) => o.standing === 'OVERDUE'], ['closed', 'Closed', (o) => o.status !== 'OPEN'], ['all', 'All', () => true]];
  let filter = 'open';
  const pills = h('div', { class: 'pills', role: 'group', 'aria-label': 'Show' });
  const out = h('div');
  const draw = () => {
    for (const b of pills.children) b.setAttribute('aria-pressed', String(b.dataset.k === filter));
    out.textContent = '';
    out.append(obligationsTable(list.filter(FILTERS.find(([k]) => k === filter)[2]), renderObligations));
  };
  for (const [k, label] of FILTERS) pills.append(h('button', { type: 'button', 'data-k': k, on: { click: () => { filter = k; draw(); } } }, label));
  draw();
  const add = S.can.has('claim.manage') ? h('button', { type: 'button', on: { click: async () => addObligationSheet(await docOpts('contract', 'ACTIVE'), renderObligations) } }, 'Add an obligation') : null;
  page('Contract obligations', 'What each contract requires of the contractor, the client and the engineer, by when. Closed once, as done or waived; a waiver needs its reason.',
    h('div', { class: 'toolbar' }, add), h('div', { class: 'kpis' }, kpi(open.length, 'Open'), kpi(open.filter((o) => o.standing === 'DUE_SOON').length, 'Due within 7 days', true),
      kpi(open.filter((o) => o.standing === 'OVERDUE').length, 'Overdue', true), kpi(list.length - open.length, 'Closed')),
    h('div', { class: 'toolbar' }, pills), out);
}

async function timePanel(contract) {
  const t = await api('GET', `/v1/contracts/${contract}/time`);
  const box = h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Time for completion and liquidated damages'));
  const reload = async () => { box.replaceWith(await timePanel(contract)); };
  const terms = t.terms;
  if (!terms) {
    if (!S.can.has('claim.manage')) { box.append(h('p', { class: 'hint' }, 'No damages terms are set for this contract.')); }
    else {
      const clause = input('clause_ref', { type: 'text', maxlength: '100', value: '8.8' });
      const done = input('completion_date', { type: 'date' });
      const rate = input('rate_per_day', { inputmode: 'decimal', autocomplete: 'off', placeholder: 'per day of delay' });
      const cap = input('cap_amount', { inputmode: 'decimal', autocomplete: 'off', placeholder: 'e.g. 10% of the contract value' });
      const b = h('button', { type: 'button', class: 'primary' }, 'Save and submit for approval');
      b.addEventListener('click', async () => {
        try {
          clearError();
          const res = await api('POST', '/v1/ld-terms', { contract_id: contract, clause_ref: clause.value.trim(), completion_date: done.value, rate_per_day: rate.value.trim(), cap_amount: cap.value.trim() });
          await api('POST', `/v1/ld-terms/${res.id}/submit`, {});
          say('Damages terms submitted for approval.'); await reload();
        } catch (e) { showError(e); }
      });
      box.append(h('p', { class: 'hint' }, 'From the contract: the time for completion, the damages per day of delay and their cap. Another person approves them; they then never change.'),
        h('div', { class: 'grid' }, field('Clause', clause), field('Time for completion', done), field('Damages per day', rate), field('Cap', cap)), h('div', { class: 'actions' }, b));
    }
  } else if (terms.status !== 'APPROVED') {
    box.append(h('p', {}, chip(terms.status), ` ${terms.number}: complete by ${terms.completion_date}, ${money(terms.rate_per_day)} a day, cap ${money(terms.cap_amount)}. `,
      h('a', { href: `#/doc/ld_terms/${terms.id}` }, terms.status === 'DRAFT' ? 'Open to submit' : 'Open to approve or reject')));
  } else {
    const kpi = (v, k, warn) => h('div', { class: `kpi${warn ? ' warnk' : ''}${/^\d{4}-\d\d-\d\d$/.test(v) ? ' datek' : ''}` }, h('div', { class: 'v' }, v), h('div', { class: 'k' }, k));
    const used = Number(t.cap) ? Math.min(100, Math.round((Number(t.damages_to_date) / Number(t.cap)) * 100)) : 0;
    const bar = h('div', { class: 'coverbar', role: 'img', 'aria-label': `${used}% of the damages cap reached` }, h('span', { class: 'short' }));
    bar.firstChild.style.width = `${used}%`;
    box.append(h('div', { class: 'kpis' }, kpi(t.completion_date, 'Time for completion'), kpi(`+${t.extension_days} d`, 'Extensions agreed'), kpi(t.revised_completion, 'Revised completion'),
      kpi(t.schedule_finish || '—', 'Baseline schedule finish', t.forecast_delay_days > 0), kpi(String(t.days_late), 'Days late today', t.days_late > 0),
      kpi(money(t.damages_to_date).replace(/\.00$/, ''), `Damages to date (${t.currency})`, Number(t.damages_to_date) > 0)),
      h('p', { class: 'hint' }, `${money(t.rate_per_day)} a day, capped at ${money(t.cap)} (clause ${terms.clause_ref}). `,
        t.schedule_finish ? (t.forecast_delay_days > 0 ? `The baseline schedule finishes ${t.forecast_delay_days} days after the revised completion: forecast damages ${money(t.damages_forecast)}.` : 'The baseline schedule finishes on time.') : 'No baseline schedule yet, so no damages forecast.',
        t.cap_reached ? ' The cap is reached.' : ''), bar,
      t.extensions.length ? table('Extensions of time agreed by the client', [{ label: 'Claim', cell: (x) => h('a', { href: `#/doc/contract_claim/${x.claim_id}` }, x.number) },
        { label: 'Title', cell: (x) => x.title }, { label: 'Days', num: true, cell: (x) => String(x.days) }, { label: 'Agreed on', cell: (x) => x.agreed_on }, { label: 'Client reference', cell: (x) => x.client_reference }], t.extensions)
        : h('p', { class: 'hint' }, 'No extension agreed yet. Days the client agrees on a claim move the completion date here automatically.'));
  }
  box.append(h('h3', {}, 'Obligations'), obligationsTable(t.obligations, reload));
  if (S.can.has('claim.manage')) box.append(h('div', { class: 'actions' }, h('button', { type: 'button', class: 'ghost', on: { click: () => addObligationSheet([{ value: contract, label: t.contract_number }], reload) } }, 'Add an obligation')));
  return box;
}

async function renderMe() {
  const pr = await api('GET', '/v1/me/profile');
  const name = pr.display_name || pr.user_id;
  paintWho(name);
  const company = (S.company && S.company.display_name) || 'this company';
  const area = {};
  for (const p of pr.permissions) area[p.area] = (area[p.area] || 0) + 1;
  const kpi = (n, k) => h('div', { class: 'kpi' }, h('div', { class: 'v' }, String(n)), h('div', { class: 'k' }, k));
  const hero = h('div', { class: `profilehero st-${pr.stage || 'none'}` }, avatar(pr.user_id, name, 'lg'),
    h('div', {}, h('div', { class: 'eyebrow' }, company), h('h2', {}, name), h('div', { class: 'sub' }, [pr.job_title, pr.user_id].filter(Boolean).join(' · ')),
      h('div', { class: 'badges' }, stageChip(pr.stage), h('span', { class: pr.mfa ? 'chip ok' : 'chip warn' }, pr.mfa ? 'Multi-factor sign-in' : 'No multi-factor sign-in'),
        h('span', { class: 'chip' }, pr.role_source === 'directory' ? 'Access managed by the company' : 'Access from your sign-in'),
        pr.kind && pr.kind !== 'human' ? h('span', { class: 'chip warn' }, `${pr.kind} principal`) : null)));
  const kpis = h('div', { class: 'kpis' }, kpi(pr.roles.length, 'Roles'), kpi(pr.permissions.length, 'Things you may do'),
    kpi(pr.tenant_wide ? 'All' : pr.projects.length, 'Projects'), kpi(pr.lent_to_me.length, 'Lent to you'), kpi(Object.keys(area).length, 'Business areas'));
  const roles = h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Your roles'),
    pr.roles.length ? h('div', { class: 'rolecards' }, pr.roles.map((r) => h('span', { class: `rolecard st-${r.stage || 'none'}` }, h('h4', {}, r.name),
      h('p', {}, r.kind === 'company' ? (r.description || 'Company role') : 'Product role'),
      h('div', { class: 'meta' }, stageChip(r.stage), h('span', {}, `${r.permissions.length} permissions`)))))
      : h('p', { class: 'hint' }, 'You hold no role in this company.'));
  const can = h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'What you may do'),
    h('p', { class: 'hint' }, 'Everything below comes from your roles' + (pr.lent_to_me.length ? ' and authority lent to you' : '') + '. The system also checks separation of duties, project scope, amount limits and fresh multi-factor sign-in on each action.'),
    pr.permissions.length ? permGroupsFrom(pr.permissions) : h('p', { class: 'hint' }, 'Nothing yet.'));
  const activity = h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Your recent activity'),
    pr.recent_activity.length ? h('ol', { class: 'timeline' }, pr.recent_activity.map((e) => h('li', {}, h('div', {}, words(e.action.replace(/[._]/g, ' '))),
      h('div', { class: 'when' }, `${when(e.at)} · ${ago(e.at)}`)))) : h('p', { class: 'hint' }, 'Nothing recorded yet.'));
  const f = { display_name: input('display_name', { type: 'text', maxlength: '200', value: pr.display_name || '', required: true, autocomplete: 'name' }),
    job_title: input('job_title', { type: 'text', maxlength: '120', value: pr.job_title || '', autocomplete: 'organization-title' }),
    phone: input('phone', { type: 'tel', maxlength: '40', value: pr.phone || '', autocomplete: 'tel' }) };
  const details = h('form', { class: 'panel', novalidate: true, on: { submit: async (ev) => {
    ev.preventDefault();
    clearError();
    try { await api('PUT', '/v1/me/profile', { display_name: f.display_name.value, job_title: f.job_title.value, phone: f.phone.value }); say('Profile saved.'); await renderMe(); } catch (e) { showError(e); }
  } } }, h('h2', { class: 'section' }, 'Your details'), field('Name', f.display_name), field('Job title', f.job_title), field('Phone', f.phone),
  h('p', { class: 'hint' }, pr.email ? `Email ${pr.email} is kept by your administrator.` : 'Your email and access are kept by your administrator.'),
  h('div', { class: 'actions' }, h('button', { type: 'submit' }, 'Save')));
  const where = h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Where you work'),
    pr.tenant_wide ? h('p', { class: 'callout ok' }, 'Every project of the company.') : pr.projects.length
      ? h('div', { class: 'rolechips' }, pr.projects.map((p) => h('span', { class: 'rolechip' }, `${p.code} — ${p.name}`))) : h('p', { class: 'hint' }, 'No project assigned yet.'));
  const lent = pr.lent_to_me.length ? h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Authority lent to you'),
    h('ul', {}, pr.lent_to_me.map((l) => h('li', {}, l.permission.label, h('span', { class: 'hint' }, ` — from ${l.from.join(', ')}`))))) : null;
  const exc = pr.duty_exceptions.length ? h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'Approved duty exceptions'), h('ul', {}, pr.duty_exceptions.map((x) => h('li', {}, x)))) : null;
  const session = h('div', { class: 'panel' }, h('h2', { class: 'section' }, 'This sign-in'),
    h('dl', { class: 'kv' }, h('dt', {}, 'Sign-in id'), h('dd', { class: 'ref' }, pr.user_id),
      h('dt', {}, 'Signed in'), h('dd', {}, pr.auth_time ? `${when(pr.auth_time)} (${ago(pr.auth_time)})` : '—'),
      h('dt', {}, 'Multi-factor'), h('dd', {}, pr.mfa ? 'yes — approvals need it within the last 10 minutes' : 'no'),
      h('dt', {}, 'First seen here'), h('dd', {}, pr.first_seen ? when(pr.first_seen) : '—')),
    h('div', { class: 'actions' }, h('button', { type: 'button', class: 'ghost', on: { click: () => signOut() } }, 'Sign out'),
      h('button', { type: 'button', class: 'danger', on: { click: async () => {
        try { await api('POST', '/v1/identity/sessions/revoke', { subject: pr.user_id, reason: 'Signed out everywhere from my profile' }); signOut('You were signed out on every device.'); } catch (e) { showError(e); }
      } } }, 'Sign out everywhere')));
  page('My profile', 'Who you are in this company, what you may do and why, and what you did last.', hero, kpis,
    h('div', { class: 'twocol' }, h('div', {}, roles, can, activity), h('div', {}, details, where, lent, exc, session)));
}
function permGroupsFrom(infos) {
  const areas = new Map();
  for (const p of infos) { if (!areas.has(p.area)) areas.set(p.area, []); areas.get(p.area).push(p); }
  return h('div', { class: 'permgroups' }, [...areas.entries()].map(([a, list]) => h('div', { class: 'permgroup' }, h('h4', {}, a, h('span', { class: 'count' }, String(list.length))),
    h('ul', {}, list.map((p) => h('li', { class: `st-${p.stage}`, title: p.code }, h('span', { class: 'dot' }), p.label))))));
}

// ---------- theme: day (green on cream) / night (cream on green) ----------
const THEME_KEY = 'serk-theme';
const SVGNS = 'http://www.w3.org/2000/svg';
function icon(paths) {
  const svg = document.createElementNS(SVGNS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('aria-hidden', 'true');
  for (const d of paths) { const p = document.createElementNS(SVGNS, 'path'); p.setAttribute('d', d); svg.append(p); }
  return svg;
}
const MOON = ['M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z'];
const SUN = ['M12 8a4 4 0 1 0 0 8a4 4 0 1 0 0-8z', 'M12 2v2', 'M12 20v2', 'M4.9 4.9l1.4 1.4', 'M17.7 17.7l1.4 1.4', 'M2 12h2', 'M20 12h2', 'M4.9 19.1l1.4-1.4', 'M17.7 6.3l1.4-1.4'];
const isNight = () => (document.documentElement.dataset.theme || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light')) === 'dark';
function paintThemeButton() {
  const b = $('theme');
  b.replaceChildren(icon(isNight() ? SUN : MOON));
  b.setAttribute('aria-label', isNight() ? 'Switch to day theme' : 'Switch to night theme');
  b.setAttribute('aria-pressed', String(isNight()));
}
(function initTheme() {
  let t = null;
  try { t = localStorage.getItem(THEME_KEY); } catch { /* storage unavailable */ }
  if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t;
  paintThemeButton();
  $('theme').addEventListener('click', () => {
    const next = isNight() ? 'light' : 'dark';
    document.documentElement.dataset.theme = next;
    try { localStorage.setItem(THEME_KEY, next); } catch { /* storage unavailable */ }
    paintThemeButton();
    say(next === 'dark' ? 'Night theme' : 'Day theme');
  });
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', paintThemeButton);
}());

// ---------- boot ----------
$('signout').addEventListener('click', () => signOut());
window.addEventListener('hashchange', route);
route();

// The sidebar sticks just below the top band; keep its offset equal to the
// band's real height (the band wraps on narrow screens).
{
  const band = document.querySelector('.top');
  const setTop = () => document.documentElement.style.setProperty('--top-h', `${band.offsetHeight}px`);
  setTop();
  if (typeof ResizeObserver === 'function') new ResizeObserver(setTop).observe(band);
}
