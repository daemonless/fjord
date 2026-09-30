// The day-one path: install an app from the store with nothing changed but
// the name, on each engine, and open it. No network picked, no Options: the
// engine's default network. This is the path #44 broke on AppJail for three
// days while every other test picked a LAN network.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
async function install(page, name, engine, port) {
  await page.goto(BASE + '/?t=' + Date.now() + '#/store', { waitUntil: 'networkidle' });
  await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
  await sleep(800);
  await page.getByRole('button', { name: /^Install/ }).first().click();
  const install = page.getByRole('button', { name: 'Install', exact: true });
  await install.waitFor();
  await page.getByLabel('Name').fill(name);
  // Only what two copies on one host need: a free port. Everything else stays
  // as the wizard offers it.
  if (!(await page.locator('#WEB_PORT').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
  await page.locator('#WEB_PORT').fill(port);
  if (engine === 'appjail') {
    const sel = page.locator('select').filter({ hasText: 'appjail' }).first();
    if (!(await sel.isVisible().catch(() => false))) await page.getByText('Advanced', { exact: true }).click();
    await sel.selectOption('appjail');
    await sleep(1500);
  }
  await page.screenshot({ path: `/out/default-install-${engine}-wizard.png`, fullPage: true });
  if (await install.isDisabled()) {
    const why = (await page.locator('.text-fjord-warning, .text-fjord-danger').allInnerTexts()).join(' | ');
    note(`${engine}: Install offered as-is`, false, `Install disabled: "${why.trim()}"`);
    return null;
  }
  await install.click();
  let d;
  // Done when it runs or fjordd recorded a failure; "unknown" is podman not
  // answering yet, not an outcome.
  for (let i = 0; i < 80; i++) { d = await (await page.request.get(BASE + '/api/stacks/' + name)).json().catch(() => ({})); if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break; await sleep(3000); }
  const f = (d?.state || {}).last_failure;
  note(`${engine}: installed on the default network, running`, d?.status?.state === 'running' && !f, `state ${d?.status?.state}${f ? ', failed: ' + f.message : ''}`);
  await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + name, { waitUntil: 'networkidle' });
  await sleep(1500);
  const open = await page.getByRole('link', { name: /Open/ }).first().getAttribute('href').catch(() => '');
  await page.screenshot({ path: `/out/default-install-${engine}-stack.png`, fullPage: true });
  note(`${engine}: Open link points at this host`, !!open && !/\/\/10\./.test(open), open || 'no Open link');
  return open;
}
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(30000);
  try {
    await install(page, 't-def-podman', 'podman', '3120');
    await install(page, 't-def-appjail', 'appjail', '3121');
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/default-install-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
