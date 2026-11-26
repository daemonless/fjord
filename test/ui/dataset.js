// A new install whose App data location is a ZFS dataset's mountpoint gets
// a dataset of its own; the delete dialog names it, and a delete with its
// data destroys it. dataset.sh makes /containers a dataset first and checks
// the host afterwards.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-ds';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/' + NAME)).json();
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(30000);
  try {
    await openPage(page, BASE + '/');
    await page.getByText('App Store', { exact: true }).first().click();
    await page.getByPlaceholder(/Search apps/).fill('syncthing');
    await page.getByRole('button', { name: /^Install/ }).first().click();
    const install = page.getByRole('button', { name: 'Install', exact: true });
    await install.waitFor();
    await page.getByLabel('Name', { exact: true }).fill(NAME);
    if (!(await page.locator('#WEB_PORT').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
    if (await page.locator('#WEB_PORT').isVisible().catch(() => false)) await page.locator('#WEB_PORT').fill('3116');
    await install.click();
    let d;
    for (let i = 0; i < 60; i++) { d = await detail(page).catch(() => ({})); if (d?.name && !d.busy) break; await sleep(3000); }
    note('installed', d?.status?.state === 'running', `state ${d?.status?.state}`);
    note('fjord recorded its own dataset', d?.state?.dataset === 'zroot/containers/' + NAME, `dataset "${d?.state?.dataset || ''}"`);

    await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/' + NAME);
    await sleep(1500);
    await page.getByTitle('More Actions').click();
    await page.getByRole('button', { name: /Delete…/ }).click();
    await page.getByText(/Its app data/).waitFor();
    const dialog = await page.getByText(/Its app data/).locator('xpath=..').innerText();
    await page.screenshot({ path: '/out/dataset-delete.png', fullPage: true });
    const snapTitle = await page.getByText('with its snapshots').getAttribute('title').catch(() => '');
    note('the delete dialog says its snapshots go with it, the dataset named on hover', dialog.includes('with its snapshots') && (snapTitle || '').includes('zroot/containers/' + NAME), JSON.stringify(dialog) + ' title ' + JSON.stringify(snapTitle));
    await page.getByLabel(/Also delete its app data/).check();
    await page.getByRole('button', { name: 'Delete stack and data', exact: true }).click();
    let gone = false;
    for (let i = 0; i < 40; i++) {
      const r = await page.request.get(BASE + '/api/stacks/' + NAME);
      if (r.status() === 404) { gone = true; break; }
      await sleep(2000);
    }
    note('deleted with its data', gone, gone ? 'stack gone' : 'still listed');
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/dataset-error.png', fullPage: true }).catch(() => {});
  } finally {
    await b.close();
  }
})();
