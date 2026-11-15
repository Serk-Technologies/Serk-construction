// Browser end-to-end test of tax (0063) and consolidation (0062) in the SERK console, started by
// internal/httpapi/console_browser_finance_test.go. The accountant designates tax accounts and prepares a
// VAT return; the CFO approves it; the accountant files it. The controller opens the consolidation page and the
// prepared run. Accessibility audited; phone width and dark theme too.
import { as, nav, act, selectByText, snap, audit, log, fail, failures, browser, pages, FX } from './console_lib.mjs';

try {
  const acc = await as('accountant');
  await nav(acc, '#/tax/accounts', 'Tax');
  await audit(acc, 'tax accounts');
  await selectByText(acc.locator('select[name=account_id]'), '2300');
  await selectByText(acc.locator('select[name=kind]'), 'Output VAT');
  await act(acc, 'Designate', 'Account designated');
  await selectByText(acc.locator('select[name=account_id]'), '1310');
  await selectByText(acc.locator('select[name=kind]'), 'Input VAT');
  await act(acc, 'Designate', 'Account designated');
  await acc.getByRole('cell', { name: /1310/ }).first().waitFor();
  // A designation is used only after someone other than its author approves it (0081).
  if (await acc.getByRole('button', { name: 'Approve', exact: true }).count()) fail('the accountant is offered to approve their own designation');
  const cfoTax = await as('cfo');
  for (let i = 0; i < 2; i++) {
    await nav(cfoTax, '#/tax/accounts', 'Tax');
    await cfoTax.getByRole('button', { name: 'Approve', exact: true }).first().click();
    await cfoTax.locator('p.notice').filter({ hasText: 'Designation approved' }).first().waitFor();
  }
  if (await cfoTax.getByRole('button', { name: 'Approve', exact: true }).count()) fail('a designation is still offered for approval after both were approved');
  log('tax designations approved by the CFO');
  await nav(acc, '#/tax/returns', 'Tax');
  await acc.getByRole('button', { name: 'Prepare return' }).waitFor();
  await selectByText(acc.locator('select[name=legal_entity_id]'), 'SERK ET');
  await act(acc, 'Prepare return', 'Return prepared');
  await audit(acc, 'tax return sheet');
  await snap(acc, 'tax-return');
  await acc.getByRole('button', { name: 'Close' }).click();
  log('VAT return prepared');

  const cfo = cfoTax;
  await nav(cfo, '#/tax/returns', 'Tax');
  if (await cfo.getByRole('button', { name: 'Prepare return' }).count()) fail('the CFO is offered "Prepare return"');
  await cfo.getByRole('link', { name: /^TAX-/ }).click();
  await act(cfo, 'Approve', 'Return approved');
  log('VAT return approved');

  await nav(acc, '#/tax/returns', 'Tax');
  await acc.getByRole('link', { name: /^TAX-/ }).click();
  await acc.getByLabel('Filing reference').fill('ERCA-2026-0001');
  await act(acc, 'Record filing', 'Return recorded as filed');
  log('VAT return filed');

  const ctrl = await as('ctrl');
  await nav(ctrl, '#/consolidation/runs', 'Consolidation');
  await audit(ctrl, 'consolidation runs');
  await ctrl.getByRole('link', { name: /^CON-/ }).click();
  await ctrl.getByRole('table').filter({ hasText: 'Consolidated trial balance' }).waitFor().catch(() => {});
  await audit(ctrl, 'consolidation run sheet');
  await snap(ctrl, 'consolidation-run');
  await nav(ctrl, '#/consolidation/groups', 'Consolidation');
  await audit(ctrl, 'consolidation groups');
  await nav(ctrl, '#/consolidation/intercompany', 'Consolidation');
  await audit(ctrl, 'intercompany accounts');

  // CAP-01.5.c: a balance-sheet line opens its account's journal lines.
  await nav(ctrl, '#/statements', 'Financial statements');
  await ctrl.getByLabel('Company').selectOption(FX.entity);
  await ctrl.getByLabel('From').fill('2020-01-01');
  await ctrl.getByLabel('From').dispatchEvent('change');
  const assets = ctrl.getByRole('table').filter({ hasText: 'Total assets' });
  await assets.waitFor();
  await audit(ctrl, 'financial statements');
  const first = assets.getByRole('link').first();
  await first.waitFor(); // the two filter changes each reload the page; wait for the newest answer's link
  const code = (await first.textContent()).trim();
  await first.click();
  await ctrl.getByRole('heading', { level: 1, name: 'General ledger detail' }).waitFor();
  await ctrl.getByRole('table').filter({ hasText: code }).waitFor();
  log(`statement line ${code} opened its journal lines`);

  const phone = await as('ctrlPhone', { viewport: { width: 390, height: 844 }, colorScheme: 'dark' });
  await nav(phone, '#/tax/returns', 'Tax');
  await audit(phone, 'tax at 390px (dark)');
  await snap(phone, 'mobile-tax-dark');
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
console.log('\nCONSOLE FINANCE: PASS');
