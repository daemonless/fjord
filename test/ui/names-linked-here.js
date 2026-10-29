// A proxy installed here from a catalog that does not declare it a names
// provider, then linked by code: its own page offers names for the apps
// that have none, as an installed provider's page does. STEP=install puts
// the proxy in through the store; STEP=check (after names-linked-here.sh
// links it) looks at its page.
const { chromium } = require('playwright-core');
const { signedIn } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const STACK = 't-rpl';
const STEP = process.env.STEP || 'install';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const shot = (page, n) => page.screenshot({ path: `/out/names-linked-here-${n}.png`, fullPage: true });

(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const ctx = await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }));
  const page = await ctx.newPage();
  page.setDefaultTimeout(30000);
  try {
    if (STEP === 'install') {
      await page.goto(BASE + '/?t=' + Date.now() + '#/store', { waitUntil: 'load' });
      await sleep(2000);
      await page.getByPlaceholder(/Search apps/).fill('Reverse Proxy');
      await sleep(800);
      await page.getByRole('button', { name: /^Install/ }).first().click();
      const install = page.getByRole('button', { name: 'Install', exact: true });
      await install.waitFor();
      await page.getByLabel('Name', { exact: true }).fill(STACK);
      await install.click();
      let d;
      for (let i = 0; i < 100; i++) {
        d = await (await page.request.get(BASE + '/api/stacks/' + STACK)).json().catch(() => ({}));
        if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break;
        await sleep(3000);
      }
      note('proxy installed from the store, running', d?.status?.state === 'running', `state ${d?.status?.state}`);
      const n = await (await page.request.get(BASE + '/api/names')).json();
      note('not a names provider by itself (as published)', n.provider !== STACK, `provider ${n.provider || 'none'}`);
    } else {
      const n = await (await page.request.get(BASE + '/api/names')).json();
      note('fjord is linked to it', !!n.linked && n.ready, `linked ${n.linked}, ready ${n.ready}, ${n.error || ''}`);
      let shown = false;
      for (let i = 0; i < 10 && !shown; i++) {
        await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + STACK, { waitUntil: 'load' });
        await sleep(2500);
        shown = await page.getByRole('button', { name: /^Add names?…$/ }).isVisible().catch(() => false);
      }
      await shot(page, 'proxy-page');
      const line = await page.getByText(/no name yet/).first().innerText().catch(() => '');
      note("the proxy's page offers names for the apps that have none", shown, line || 'no Add names');
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await shot(page, 'error').catch(() => {});
  }
  await b.close();
})();
