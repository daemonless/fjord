// The wizard's summary says what the install keeps on this host. An app with
// a data folder names it; one with none (openspeedtest: nothing to keep) says
// so, since no folder is made for it.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);

(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }))).newPage();
  page.setDefaultTimeout(30000);
  const dataRow = async (app) => {
    await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
    await page.getByPlaceholder(/Search apps/).fill(app);
    await sleep(800);
    const card = page.getByRole('button', { name: /^Install/ }).first();
    if (!(await card.isVisible().catch(() => false))) await page.screenshot({ path: `/out/wizard-data-${app}-store.png`, fullPage: true }).catch(() => {});
    await card.click();
    await page.getByRole('button', { name: 'Install', exact: true }).waitFor();
    await sleep(500);
    const dd = page.locator('dt', { hasText: /^Data$/ }).locator('xpath=following-sibling::dd[1]');
    const text = (await dd.innerText().catch(() => '')).trim();
    await page.screenshot({ path: `/out/wizard-data-${app}.png`, fullPage: true }).catch(() => {});
    await page.keyboard.press('Escape');
    return text;
  };
  try {
    const none = await dataRow('OpenSpeedTest');
    note('an app with no host folder says so', /keeps nothing on this host/.test(none), none || 'no Data row');
    const some = await dataRow('Syncthing');
    note('an app with a data folder names it', /^\/.+\/t?[a-z0-9-]+/.test(some) && !/nothing/.test(some), some || 'no Data row');
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/wizard-data-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
