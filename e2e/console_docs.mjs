// Browser end-to-end test of document control and RFIs (migration 0066) and a quality plan (0065), started by
// internal/httpapi/console_browser_docs_test.go. Accessibility audited; phone width and dark theme too.
import { as, nav, act, selectByText, snap, audit, log, fail, failures, browser, pages } from './console_lib.mjs';

try {
  const dc = await as('dc');
  await nav(dc, '#/docs/documents', 'Documents and RFIs');
  await audit(dc, 'documents (empty)');
  await dc.getByRole('button', { name: 'Add a document' }).click();
  const dlg = dc.getByRole('dialog');
  await dlg.getByLabel('Number').fill('A-102');
  await dlg.getByLabel('Title').fill('Ground floor plan');
  await dlg.getByLabel('Discipline').fill('Architecture');
  await dlg.getByLabel('File').setInputFiles({ name: 'A-102.pdf', mimeType: 'application/pdf', buffer: Buffer.from('%PDF-1.4 drawing A-102 revision A') });
  await audit(dc, 'upload sheet');
  await dlg.getByRole('button', { name: 'Upload' }).click();
  await dc.locator('p.notice').filter({ hasText: 'Document uploaded' }).first().waitFor();
  log('document uploaded');

  const pm = await as('pm');
  await nav(pm, '#/docs/documents', 'Documents and RFIs');
  await pm.getByRole('button', { name: 'Publish' }).first().click();
  await pm.locator('p.notice').filter({ hasText: 'Revision published' }).first().waitFor();
  await audit(pm, 'documents (published)');
  log('document published by a second person');

  const eng = await as('eng');
  await nav(eng, '#/docs/documents', 'Documents and RFIs');
  await eng.getByRole('button', { name: /Rev 1 published/ }).waitFor();
  const dl = eng.waitForEvent('download');
  await eng.getByRole('button', { name: /Rev 1 published/ }).click();
  const file = await dl;
  if (file.suggestedFilename() !== 'A-102.pdf') fail(`download named ${file.suggestedFilename()}`);
  log('document downloaded');
  await nav(eng, '#/docs/rfis', 'Documents and RFIs');
  await audit(eng, 'rfis');
  await eng.getByLabel('Subject').fill('Slab level at gridline C');
  await eng.getByLabel('Question').fill('Drawing A-102 and the structural drawing disagree on the slab level. Which governs?');
  await act(eng, 'Raise RFI', 'RFI raised');
  await nav(pm, '#/docs/rfis', 'Documents and RFIs');
  await pm.getByRole('button', { name: 'Answer…' }).first().click();
  await pm.getByRole('dialog').getByLabel('Answer').fill('The architectural level governs; revision B issued.');
  await pm.getByRole('dialog').getByRole('button', { name: 'Send answer' }).click();
  await pm.locator('p.notice').filter({ hasText: 'RFI answered' }).first().waitFor();
  await nav(eng, '#/docs/rfis', 'Documents and RFIs');
  await eng.getByRole('button', { name: 'Close', exact: true }).first().click();
  await eng.locator('p.notice').filter({ hasText: 'RFI closed' }).first().waitFor();
  log('RFI raised, answered and closed');

  const q = await as('quality');
  await nav(q, '#/quality/plans', 'Quality');
  await audit(q, 'quality plans');
  await q.getByRole('button', { name: 'Prepare a plan' }).click();
  const qd = q.getByRole('dialog');
  await qd.getByLabel('Title').fill('Foundation concrete');
  await qd.getByLabel('Activity code').fill('B');
  await qd.getByLabel('What is inspected, line 1').fill('Rebar before pouring');
  await selectByText(qd.getByLabel('Kind, line 1'), 'Hold');
  await qd.getByRole('button', { name: 'Save and submit' }).click();
  await q.locator('p.notice').filter({ hasText: 'Plan submitted' }).first().waitFor();
  await nav(pm, '#/quality/plans', 'Quality');
  await pm.getByRole('button', { name: 'Approve', exact: true }).first().click();
  await pm.locator('p.notice').filter({ hasText: 'Plan approved' }).first().waitFor();
  await snap(pm, 'plan-approved');
  log('inspection plan prepared and approved');

  const phone = await as('engPhone', { viewport: { width: 390, height: 844 }, colorScheme: 'dark' });
  await nav(phone, '#/docs/documents', 'Documents and RFIs');
  await audit(phone, 'documents at 390px (dark)');
  await nav(phone, '#/credit/rules', 'Progress credit');
  await audit(phone, 'credit at 390px (dark)');
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
console.log('\nCONSOLE DOCS: PASS');
