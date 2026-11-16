// Browser end-to-end test of controlled documents and RFIs (migration 0052)
// in the SERK console, started by
// internal/httpapi/console_browser_documents_test.go (which verifies the
// outcome through the API afterwards). A site engineer registers a drawing
// and uploads revision A, a project manager publishes it from the worklist,
// revision B supersedes it, the file downloads with the bytes that were
// uploaded, and an RFI about the drawing is raised, answered by a planner and
// closed by its author. Accessibility is audited on every new page; phone
// width and dark theme too.
import crypto from 'node:crypto';
import fs from 'node:fs';
import { as, nav, act, selectByText, snap, audit, idFromHash, log, fail, failures, browser, pages } from './console_lib.mjs';

const pdf = (body) => Buffer.from('%PDF-1.7\n' + body);
const sha = (b) => crypto.createHash('sha256').update(b).digest('hex');

async function upload(p, rev, body, note) {
  await p.getByLabel('Revision', { exact: true }).fill(rev);
  await p.getByLabel('File').setInputFiles({ name: `A-101 ${rev}.pdf`, mimeType: 'application/pdf', buffer: pdf(body) });
  await p.getByLabel('Change note').fill(note);
}

try {
  // ---- 1. Engineer registers a drawing with revision A and submits it -----------------
  const eng = await as('eng');
  await nav(eng, '#/documents', 'Document register');
  await audit(eng, 'document register (empty)');
  await eng.getByRole('link', { name: 'Register a document' }).click();
  await eng.getByRole('heading', { level: 1, name: 'Register a document' }).waitFor();
  await selectByText(eng.getByLabel('Project'), 'P-API');
  await eng.getByLabel('Reference').fill('A-101');
  await selectByText(eng.getByLabel('Type'), 'Drawing');
  await eng.getByLabel('Title').fill('General arrangement plan, level 1');
  await eng.getByLabel('Discipline').fill('Architecture');
  await upload(eng, 'A', 'revision A', 'Issued for construction');
  await audit(eng, 'document form');
  await eng.getByRole('button', { name: 'Register and upload' }).click();
  await eng.getByRole('heading', { level: 1, name: /^Document revision DOC-.*\/A$/ }).waitFor();
  const revA = await idFromHash(eng);
  let view = await eng.locator('main').textContent();
  if (!view.includes('Work in progress') || !view.includes(sha(pdf('revision A')))) fail('the draft revision does not show its state and SHA-256');
  await audit(eng, 'revision draft');
  await act(eng, 'Submit for approval', 'Submit for approval: done');
  if (await eng.getByRole('button', { name: 'Approve', exact: true }).count()) fail('the uploader is offered "Approve"');
  await snap(eng, 'revision-submitted');
  log('revision A uploaded and submitted');

  // ---- 2. Project manager publishes it from the worklist ------------------------------
  const pm = await as('pm');
  await nav(pm, '#/home', 'My work');
  await pm.getByRole('link', { name: /Document revisions to publish/ }).waitFor();
  await nav(pm, `#/doc/document_revision/${revA}`, /^Document revision DOC-/);
  await act(pm, 'Approve', 'Approve: done');
  await pm.locator('main').filter({ hasText: 'Published (current)' }).waitFor();
  await audit(pm, 'revision published');
  log('revision A published by the project manager');

  // ---- 3. Revision B supersedes A --------------------------------------------------------
  await nav(eng, '#/documents', 'Document register');
  await eng.getByRole('link', { name: 'A-101', exact: true }).click();
  await eng.getByRole('heading', { level: 1, name: /^A-101/ }).waitFor();
  const docID = await idFromHash(eng);
  await audit(eng, 'document with one revision');
  await eng.getByRole('link', { name: 'Upload a new revision' }).click();
  await eng.getByRole('heading', { level: 1, name: 'Upload a new revision' }).waitFor();
  await upload(eng, 'B', 'revision B', 'Grid line C added');
  await eng.getByRole('button', { name: 'Upload revision' }).click();
  await eng.getByRole('heading', { level: 1, name: /^Document revision DOC-.*\/B$/ }).waitFor();
  const revB = await idFromHash(eng);
  await act(eng, 'Submit for approval', 'Submit for approval: done');
  await nav(pm, `#/doc/document_revision/${revB}`, /^Document revision DOC-/);
  await act(pm, 'Approve', 'Approve: done');
  await nav(eng, `#/documents/${docID}`, /^A-101/);
  const rows = await eng.getByRole('table', { name: /Revisions/ }).locator('tbody tr').allTextContents();
  if (rows.length !== 2 || !rows[0].includes('Published (current)') || !rows[1].includes('Superseded (not current)')) fail(`revisions after publishing B: ${rows.join(' || ')}`);
  if (await eng.getByRole('link', { name: 'Upload a new revision' }).count() !== 1) fail('a new revision cannot be started after B was published');
  await audit(eng, 'document with a superseded revision');
  await snap(eng, 'document-revisions');
  await nav(eng, `#/doc/document_revision/${revA}`, /^Document revision DOC-/);
  if (!(await eng.locator('main').textContent()).includes('must not be built from')) fail('the superseded revision is not marked as such');
  log('revision B published; A superseded');

  // ---- 4. The register offers the current revision; the download is the uploaded file ---
  await nav(eng, '#/documents', 'Document register');
  const reg = await eng.getByRole('table', { name: 'Documents' }).locator('tbody tr').allTextContents();
  if (reg.length !== 1 || !/A-101.*Drawing.*P-API.*B/.test(reg[0])) fail(`register row: ${reg.join(' || ')}`);
  const [dl] = await Promise.all([eng.waitForEvent('download'), eng.getByRole('button', { name: 'Download A-101' }).click()]);
  const got = fs.readFileSync(await dl.path());
  if (dl.suggestedFilename() !== 'A-101 B.pdf' || sha(got) !== sha(pdf('revision B'))) fail(`download: ${dl.suggestedFilename()} ${sha(got)}`);
  await audit(eng, 'document register');
  await snap(eng, 'document-register');
  log('current revision downloaded and matches the upload');

  // ---- 5. RFI: raised by the engineer, answered by the planner, closed by the engineer ---
  // Regression (run 35): the form asks for the document list when it opens and again when the project changes.
  // Hold the FIRST answer back so that it arrives last; it must not undo the choice made meanwhile.
  let heldFirst = false;
  let firstAnswered;
  const firstDone = new Promise((r) => { firstAnswered = r; });
  await eng.route(/\/v1\/documents\?project=/, async (route) => {
    if (heldFirst) return route.continue();
    heldFirst = true;
    await new Promise((r) => setTimeout(r, 600));
    await route.continue();
    firstAnswered();
  });
  await nav(eng, '#/new/rfi', 'Raise a request for information');
  await selectByText(eng.getByLabel('Project'), 'P-API');
  await eng.getByLabel('About').locator('option', { hasText: 'A-101 rev B' }).waitFor({ state: 'attached' });
  await selectByText(eng.getByLabel('About'), 'A-101 rev B');
  if (heldFirst) await firstDone;
  // The held answer has now been delivered to the page. Watch the choice while the page handles it.
  const seen = await eng.getByLabel('About').evaluate(async (el) => {
    const texts = new Set();
    for (let i = 0; i < 20; i++) { texts.add(el.options[el.selectedIndex].text); await new Promise((r) => setTimeout(r, 25)); }
    return [...texts];
  });
  await eng.unroute(/\/v1\/documents\?project=/);
  if (seen.length !== 1 || !/A-101 rev B/.test(seen[0])) fail(`a late answer to an earlier request reset the RFI's document: saw ${JSON.stringify(seen)}`);
  await eng.getByLabel('Subject').fill('Slab thickness at grid C-4');
  await eng.getByLabel('Question').fill('Section 2 shows 200 mm, the schedule shows 250 mm. Which governs?');
  await eng.getByLabel('Expected to answer').fill('u-doc-planner');
  await audit(eng, 'rfi form');
  await eng.getByRole('button', { name: 'Raise the RFI' }).click();
  await eng.getByRole('heading', { level: 1, name: /^Request for information RFI-/ }).waitFor();
  const rfi = await idFromHash(eng);
  if (await eng.getByRole('button', { name: 'Record the answer' }).count()) fail('the author is offered to answer their own RFI');
  await audit(eng, 'rfi open');
  const planner = await as('planner');
  await nav(planner, `#/doc/rfi/${rfi}`, /^Request for information RFI-/);
  await planner.getByLabel('Answer').fill('The schedule governs: 250 mm. Section 2 will be corrected in revision C.');
  await planner.getByRole('button', { name: 'Record the answer' }).click();
  await planner.locator('main').filter({ hasText: 'ANSWERED' }).waitFor();
  if (await planner.getByRole('button', { name: 'Close the RFI' }).count()) fail('someone other than the author is offered to close the RFI');
  await audit(planner, 'rfi answered');
  await nav(eng, `#/doc/rfi/${rfi}`, /^Request for information RFI-/);
  await eng.getByRole('button', { name: 'Close the RFI' }).click();
  await eng.locator('main').filter({ hasText: 'CLOSED' }).waitFor();
  await snap(eng, 'rfi-closed');
  log('RFI raised, answered and closed');

  // ---- 5b. Transmittal of the approved drawing; a submittal reviewed by someone else (0077) ---
  await nav(eng, '#/new/transmittal', 'Send a transmittal');
  await selectByText(eng.getByLabel('Project', { exact: true }), 'P-API');
  await eng.getByRole('checkbox', { name: /^A-101 rev B/ }).check();
  await eng.getByLabel('Subject').fill('General arrangement for construction');
  await eng.getByLabel('To', { exact: true }).fill('Abay Construction PLC');
  await audit(eng, 'transmittal form');
  await eng.getByRole('button', { name: 'Send transmittal' }).click();
  await eng.getByRole('heading', { level: 1, name: /^Transmittal TRM-/ }).waitFor();
  log('transmittal sent');
  await nav(eng, '#/new/submittal', 'New submittal');
  await selectByText(eng.getByLabel('Project', { exact: true }), 'P-API');
  await eng.getByLabel('Specification section').fill('03 30 00');
  await eng.getByLabel('Title').fill('Concrete mix design C30');
  await eng.getByRole('button', { name: 'Submit for review' }).click();
  await eng.getByRole('heading', { level: 1, name: /^Submittal SBM-/ }).waitFor();
  const sbm = await idFromHash(eng);
  if (await eng.getByRole('button', { name: 'Record the review' }).count()) fail('the submitter is offered to review their own submittal');
  await nav(pm, `#/doc/submittal/${sbm}`, /^Submittal SBM-/);
  await audit(pm, 'submittal review');
  await selectByText(pm.getByLabel('Review code'), 'C —');
  await pm.getByLabel('Note').fill('Increase the cement content to meet the 28-day strength');
  await pm.getByRole('button', { name: 'Record the review' }).click();
  await pm.getByRole('button', { name: 'Record the review' }).waitFor({ state: 'detached' });
  await pm.locator('main').filter({ hasText: 'REVIEWED' }).first().waitFor();
  log('submittal reviewed (C)');

  // ---- 5b'. Review cycle (0090): the engineer resubmits; the cycle shows both submissions --------
  await nav(eng, `#/doc/submittal/${sbm}`, /^Submittal SBM-/);
  await eng.getByRole('link', { name: 'Resubmit' }).click();
  await eng.getByRole('heading', { level: 1, name: 'Resubmit' }).waitFor();
  if ((await eng.getByLabel('Specification section').inputValue()) !== '03 30 00') fail('the resubmission did not keep the specification section');
  await audit(eng, 'resubmission form');
  await eng.getByRole('button', { name: 'Submit for review' }).click();
  await eng.getByRole('heading', { level: 1, name: /^Submittal SBM-/ }).waitFor();
  await eng.getByRole('table').filter({ hasText: 'Concrete mix design C30' }).filter({ hasText: '2' }).first().waitFor();
  if ((await eng.getByRole('table', { name: /Submissions/ }).getByRole('row').count()) < 3) fail('the review cycle does not list both submissions');
  log('submittal resubmitted: cycle 2');

  // ---- 5b''. A change event raised from the answered RFI shows on the RFI (0090) ---------------
  await nav(pm, '#/new/change_event', 'Raise a change event');
  await selectByText(pm.getByLabel('Project', { exact: true }), 'P-API');
  await pm.getByLabel('Title').fill('Slab thickness per RFI answer');
  await selectByText(pm.getByLabel('RFI (when raised from one)'), 'RFI-');
  await pm.getByLabel('Description').fill('250 mm governs, as answered');
  await pm.getByLabel('Schedule impact (days; signed)').fill('2');
  await pm.getByRole('button', { name: 'Create change event' }).click();
  await pm.getByRole('heading', { level: 1, name: /^Change event CE-/ }).waitFor();
  await nav(pm, `#/doc/rfi/${rfi}`, /^Request for information RFI-/);
  await pm.getByRole('table').filter({ hasText: 'CE-' }).first().waitFor();
  await audit(pm, 'rfi with its change events');
  log('change event raised from the RFI and shown on it');

  // ---- 5c. Correspondence: an incoming letter needing an answer, then its reply (0085) ---------
  const inWeek = new Date(Date.now() + 7 * 864e5).toISOString().slice(0, 10);
  await nav(eng, '#/new/correspondence', 'Record correspondence');
  await selectByText(eng.getByLabel('Project', { exact: true }), 'P-API');
  await eng.getByLabel('Counterparty').fill('Ethiopian Roads Administration');
  await eng.getByLabel('Subject').fill('Notice of delayed access to the site');
  await eng.getByLabel('Reference').fill('ERA/RE/2026/233');
  await eng.getByLabel('Answer due (optional)').fill(inWeek);
  await audit(eng, 'correspondence form');
  await eng.getByRole('button', { name: 'Record', exact: true }).click();
  await eng.getByRole('heading', { level: 1, name: /^Correspondence item COR-/ }).waitFor();
  const cor = await idFromHash(eng);
  await eng.locator('main').filter({ hasText: 'AWAITING_REPLY' }).first().waitFor();
  await nav(eng, '#/new/correspondence', 'Record correspondence');
  await selectByText(eng.getByLabel('Project', { exact: true }), 'P-API');
  await selectByText(eng.getByLabel('Direction'), 'Outgoing');
  await eng.getByLabel('Counterparty').fill('Ethiopian Roads Administration');
  await eng.getByLabel('Subject').fill('Re: delayed access — revised sequence enclosed');
  await selectByText(eng.getByLabel('Reply to (optional)'), 'Notice of delayed access');
  await eng.getByRole('button', { name: 'Record', exact: true }).click();
  await eng.getByRole('heading', { level: 1, name: /^Correspondence item COR-/ }).waitFor();
  await nav(eng, `#/doc/correspondence/${cor}`, /^Correspondence item COR-/);
  await eng.locator('main').filter({ hasText: 'ANSWERED' }).first().waitFor();
  await snap(eng, 'correspondence-answered');
  log('correspondence recorded and answered');

  // ---- 6. Phone width, dark theme ------------------------------------------------------------
  const phone = await as('pmPhone', { viewport: { width: 390, height: 844 }, colorScheme: 'dark' });
  await nav(phone, `#/documents/${docID}`, /^A-101/);
  await audit(phone, 'document at 390px (dark)');
  await snap(phone, 'mobile-document-dark');
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
console.log('\nCONSOLE DOCUMENTS: PASS');
