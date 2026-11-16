// Sign-in, branding and workflow map journey (BRS-03, XCT-UI), driven by
// TestConsoleBrowserSignin: e-mail + 6-digit PIN with the one-time PIN
// replaced at first sign-in, the branding editor's live preview and contrast
// warnings, the logo, the workflow map and the person's own security panel.
import { BASE, FX, browser, failures, log, fail, snap, audit, nav } from './console_lib.mjs';

const watch = (p, who) => {
  p.on('pageerror', (e) => fail(`[${who}] uncaught script error: ${e.message}`));
  p.on('console', (m) => { if (m.type() === 'error' && /Content Security Policy|Refused to/.test(m.text())) fail(`[${who}] CSP violation: ${m.text()}`); });
};
const status = (p, text) => p.waitForFunction((t) => document.getElementById('status').textContent.includes(t), text, { timeout: 15000 });
const signIn = (p) => p.getByRole('button', { name: 'Sign in', exact: true }).click();

const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: 'light' });
const p = await ctx.newPage();
watch(p, 'admin');
try {
  // ---- 1. The two-panel page ------------------------------------------------------
  await p.goto(BASE);
  await p.getByRole('heading', { level: 1, name: 'Sign in to your space' }).waitFor();
  await p.getByText('Less noise. More done.').waitFor();
  await p.getByText('Protected sessions · role-scoped data · audit records').waitFor();
  if (!(await p.getByLabel('Email address').evaluate((e) => e === document.activeElement))) fail('the e-mail field is not focused on arrival');
  await p.getByLabel('Authenticator code (if enabled)').waitFor();
  if (await p.getByLabel('Access token').isVisible()) fail('the token sign-in is shown although e-mail and PIN sign-in is offered');
  await audit(p, 'sign-in page');
  await snap(p, 'signin');

  // ---- 2. Wrong PIN counts down; the eye shows and hides --------------------------
  await p.getByLabel('Email address').fill(FX.email);
  const pin = p.getByLabel('Password', { exact: true });
  await pin.fill('730518');
  await p.getByRole('button', { name: 'Show password' }).click();
  if ((await pin.getAttribute('type')) !== 'text') fail('the eye did not show the PIN');
  await p.getByRole('button', { name: 'Hide password' }).click();
  if ((await pin.getAttribute('type')) !== 'password') fail('the eye did not hide the PIN again');
  await signIn(p);
  await p.locator('.formmsg').filter({ hasText: '14 attempts left' }).waitFor();
  log('wrong PIN: 14 attempts left reported');

  // ---- 3. The one-time PIN must be replaced; a weak one is refused ----------------
  await pin.fill(FX.pin);
  await signIn(p);
  await p.getByText('Choose your own PIN').waitFor();
  await p.getByLabel('New PIN', { exact: true }).fill('123456');
  await p.getByLabel('New PIN again', { exact: true }).fill('123456');
  await signIn(p);
  await p.locator('.formmsg.bad').waitFor();
  if (await p.getByRole('heading', { level: 1, name: 'My work' }).count()) fail('a weak new PIN was accepted');
  await p.getByLabel('New PIN', { exact: true }).fill('582047');
  await p.getByLabel('New PIN again', { exact: true }).fill('582047');
  await signIn(p);
  await p.getByRole('heading', { level: 1, name: 'My work' }).waitFor();
  log('first sign-in replaced the one-time PIN (weak PIN refused)');

  // ---- 4. Branding: logo, words, colours with live preview ------------------------
  await nav(p, '#/branding', 'Brand & appearance');
  await p.locator('input[type=file][name=logo]').setInputFiles({ name: 'logo.png', mimeType: 'image/png', buffer: Buffer.from(FX.logo, 'base64') });
  await p.locator('.brandpreview .pvlogo img').waitFor();
  await p.getByText(/Chosen: logo\.png/).waitFor();
  await p.getByRole('button', { name: 'Upload logo' }).click();
  await p.getByText(/Current logo: PNG, 64×48 px/).waitFor();
  await p.locator('.brand .bandlogo').waitFor();
  log('logo previewed, uploaded and shown in the top band');
  await p.getByLabel('Motto (headline)').fill('Built to last.');
  await p.locator('.brandpreview .motto').filter({ hasText: 'Built to last.' }).waitFor();
  await p.getByLabel('Address').fill('Bole Road, Addis Ababa');
  const accent = p.getByLabel('Accent (buttons, links) hex value');
  await accent.fill('#eeeeee');
  await p.locator('.callout.bad').filter({ hasText: 'Hard to read in light mode' }).waitFor();
  await p.getByRole('button', { name: 'Save branding' }).click();
  await p.locator('#alert').filter({ hasText: 'hard to read' }).waitFor();
  await accent.fill('#7a1f1f');
  await p.locator('.callout.ok').filter({ hasText: 'readable' }).waitFor();
  const btn = await p.locator('.brandpreview .pvbtn').evaluate((e) => getComputedStyle(e).backgroundColor);
  if (btn !== 'rgb(122, 31, 31)') fail(`the preview button is ${btn}, not the chosen accent`);
  await p.getByRole('button', { name: 'Night (dark) mode' }).click();
  if ((await p.locator('.brandpreview').getAttribute('data-mode')) !== 'dark') fail('the preview did not switch to night mode');
  await p.getByRole('button', { name: 'Day (light) mode' }).click();
  await audit(p, 'branding page');
  await snap(p, 'branding');
  await p.getByRole('button', { name: 'Save branding' }).click();
  await status(p, 'Branding saved');
  const live = await p.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--accent').trim());
  if (live !== '#7a1f1f') fail(`the console accent is ${live} after saving`);
  log('branding saved; the console applies the accent');

  // ---- 5. Workflow map with authorities --------------------------------------------
  await nav(p, '#/workflows', 'Workflow map');
  const po = p.locator('article.flowcard').filter({ has: p.getByRole('heading', { name: 'Purchase orders' }) });
  await po.locator('.flowstep.v-approve .rolechip').filter({ hasText: /^CEO$/ }).waitFor();
  await po.locator('.flowstep.v-create').getByText('po.manage').waitFor();
  await audit(p, 'workflow map');
  await p.getByLabel('Show the steps of a role').selectOption('board_chairman');
  await p.getByText('No workflow matches.').waitFor();
  await p.getByLabel('Show the steps of a role').selectOption('ceo');
  await po.locator('.flowstep.v-approve.mine').waitFor();
  log('workflow map: the CEO approves purchase orders; the board chairman takes no step');

  // ---- 6. My security: PIN change and the authenticator ------------------------------
  await nav(p, '#/me', 'My profile');
  await p.getByRole('heading', { name: 'Sign-in & security' }).waitFor();
  await p.getByLabel('Current PIN', { exact: true }).fill('582047');
  await p.getByLabel('New PIN', { exact: true }).fill('604913');
  await p.getByLabel('New PIN again', { exact: true }).fill('604913');
  await p.getByRole('button', { name: 'Change PIN' }).click();
  await status(p, 'Your PIN was changed');
  await p.getByRole('heading', { name: 'Sign-in & security' }).waitFor();
  await p.getByRole('button', { name: 'Set up an authenticator' }).click();
  await p.getByLabel('Code from the app').waitFor();
  await audit(p, 'my profile');
  log('PIN changed from my profile; authenticator set-up offered');

  // ---- 7. People: the sign-ins tab and the person's sign-in ------------------------
  await nav(p, '#/people', 'People & access');
  await p.getByRole('tab', { name: 'Sign-ins' }).click();
  await p.getByRole('cell', { name: FX.email }).waitFor();
  await p.getByText('PIN changed').first().waitFor();
  await audit(p, 'people sign-ins');

  // ---- 8. Signed out, the sign-in page shows the company's brand ---------------------
  await p.getByRole('button', { name: 'Account menu' }).click();
  await p.getByRole('menuitem', { name: 'Sign out' }).click();
  await p.getByRole('heading', { level: 1, name: 'Sign in to your space' }).waitFor();
  await p.getByText('Built to last.').waitFor();
  await p.getByText('Bole Road, Addis Ababa').waitFor();
  const w = await p.locator('img.brandlogo').evaluate((img) => new Promise((ok) => (img.complete ? ok(img.naturalWidth) : img.addEventListener('load', () => ok(img.naturalWidth)))));
  if (w !== 64) fail(`the sign-in logo is ${w} px wide, not the uploaded 64`);
  await p.getByLabel('Email address').fill(FX.email);
  await p.getByLabel('Password', { exact: true }).fill('604913');
  await signIn(p);
  await p.getByRole('heading', { level: 1, name: 'My work' }).waitFor();
  log('signed out: the sign-in page shows the motto, address and logo; the changed PIN signs in');

  // ---- 9. Night mode and phone width ---------------------------------------------------
  const dark = await (await browser.newContext({ viewport: { width: 1280, height: 900 }, colorScheme: 'dark' })).newPage();
  watch(dark, 'night');
  await dark.goto(BASE);
  await dark.getByText('Built to last.').waitFor();
  await audit(dark, 'sign-in page at night');
  const phone = await (await browser.newContext({ viewport: { width: 390, height: 844 } })).newPage();
  watch(phone, 'phone');
  await phone.goto(BASE);
  await phone.getByText('Built to last.').waitFor();
  const sw = await phone.evaluate(() => document.documentElement.scrollWidth);
  if (sw > 390) fail(`the sign-in page scrolls sideways at 390px (${sw})`);
  await audit(phone, 'sign-in page at 390px');
  await snap(phone, 'signin-phone');
} catch (e) {
  fail(`journey aborted: ${e.message.split('\n').slice(0, 4).join(' | ')}`);
  try { console.log('DEBUG', await p.evaluate(() => [...document.querySelectorAll('.callout, #alert, .tokenrow[data-token=accent]')].map((e) => `${e.className}: ${e.textContent.slice(0, 160)} ${e.querySelector && e.querySelector('input.hex') ? e.querySelector('input.hex').value : ''}`).join(' || '))); await snap(p, 'failure'); } catch { /* page may be gone */ }
}
await browser.close();
if (failures.length) { console.log(`${failures.length} failure(s)`); process.exit(1); }
console.log('sign-in: all checks passed');
