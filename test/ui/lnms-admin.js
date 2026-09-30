// The admin password given at install is the one that works, however weak:
// install LibreNMS through the wizard with ADMIN_PASSWORD=admin, then log in
// with it.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-lnms';
const ADMIN_PASSWORD = process.env.ADMIN_PASSWORD || 'admin';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const shot = (page, n) => page.screenshot({ path: `/out/${n}.png`, fullPage: true }).catch(() => {});
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(30000);
  try {
    await page.goto(BASE + '/#/store', { waitUntil: 'networkidle' });
    await page.getByPlaceholder(/Search apps/).fill('librenms');
    await sleep(1000);
    await page.getByRole('button', { name: /^Install/ }).first().click();
    const install = page.getByRole('button', { name: 'Install', exact: true });
    await install.waitFor();
    await page.getByLabel('Name').fill(NAME);
    await page.locator('#DB_PASSWORD').fill('lnms-db-t3st');
    await page.locator('#ADMIN_PASSWORD').fill(ADMIN_PASSWORD);
    // The folders wait under Options. Their defaults are the app's, not this
    // stack's (/containers/librenms/...), which is the other copy's data.
    await page.getByRole('button', { name: /^Options/ }).first().click();
    await page.locator('#CONFIG_LOCATION').fill('/containers/t-lnms/config');
    await page.locator('#DB_DATA_LOCATION').fill('/containers/t-lnms/mariadb');
    await shot(page, 'lnms-admin-wizard');
    await install.click();
    let d;
    for (let i = 0; i < 160; i++) {
      d = await (await page.request.get(BASE + '/api/stacks/' + NAME)).json().catch(() => ({}));
      if (d?.status?.state === 'running' && !d.busy) break;
      await sleep(5000);
    }
    note('stack running', d?.status?.state === 'running', `state ${d?.status?.state}`);

    // The login form, then the login. First start runs migrations, so the
    // page gets a few minutes.
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + NAME, { waitUntil: 'networkidle' });
    await sleep(2000);
    const open = await page.getByRole('link', { name: /Open/ }).first().getAttribute('href').catch(() => '');
    note('Open link', !!open, open || 'none');
    const url = open.replace(/\/$/, '') + '/login';
    const app = await (await b.newContext()).newPage();
    let loaded = false;
    for (let i = 0; i < 40 && !loaded; i++) {
      try {
        await app.goto(url, { waitUntil: 'load', timeout: 15000 });
        loaded = (await app.locator('input[name="username"]').count()) > 0;
      } catch {}
      if (!loaded) await sleep(10000);
    }
    note('LibreNMS login page', loaded, loaded ? url : 'never showed a login form');
    if (loaded) {
      await sleep(15000); // the admin is created by the init step; give it a moment after the page answers
      await app.locator('input[name="username"]').fill('admin');
      await app.locator('input[name="password"]').fill(ADMIN_PASSWORD);
      await app.locator('button[type="submit"], input[type="submit"]').first().click();
      await app.waitForLoadState('load');
      await sleep(3000);
      const inside = !(await app.locator('input[name="password"]').count()) && !/login/.test(app.url());
      await app.screenshot({ path: '/out/lnms-admin-after-login.png', fullPage: true });
      note(`logs in as admin with "${ADMIN_PASSWORD}"`, inside, app.url());
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await shot(page, 'lnms-admin-error');
  }
  await b.close();
})();
