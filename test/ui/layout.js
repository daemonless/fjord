// The app page: Open, Start or Stop and the more menu in the header; tabs
// Services, Compose, History, Settings; Revert and Save only while there is
// an edit, on every tab; no sideways scroll on a phone.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-layout';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/' + NAME)).json();
const settle = async (page, want) => {
  let d;
  for (let i = 0; i < 60; i++) {
    d = await detail(page).catch(() => ({}));
    if (d?.name && !d.busy && (!want || d.status?.state === want)) return d;
    await sleep(3000);
  }
  return d;
};
const shown = async (loc) => (await loc.count()) > 0 && (await loc.first().isVisible());
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const ctx = await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }));
  const page = await ctx.newPage();
  page.setDefaultTimeout(30000);
  try {
    await openPage(page, BASE + '/');
    await page.getByText('App Store', { exact: true }).first().click();
    await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
    await page.getByRole('button', { name: /^Install/ }).first().click();
    const install = page.getByRole('button', { name: 'Install', exact: true });
    await install.waitFor();
    await page.getByLabel('Name', { exact: true }).fill(NAME);
    if (!(await page.locator('#type-openspeedtest').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
    const type = page.locator('#type-openspeedtest');
    if ((await type.isVisible().catch(() => false)) && (await type.inputValue()) !== 'networks') await type.selectOption('networks');
    await page.locator('#WEB_PORT').fill('3116');
    const netSel = page.locator('table select').filter({ has: page.locator('option[value="bridge"]') });
    if ((await netSel.count()) === 0) await page.getByRole('button', { name: /\+ interface/ }).first().click();
    await netSel.first().selectOption('bridge');
    await install.click();
    let d = await settle(page, 'running');
    note('installed and running', d?.status?.state === 'running', `state ${d?.status?.state}`);

    await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/' + NAME);
    await sleep(1500);
    const btn = (name) => page.getByRole('button', { name, exact: true });
    const tabs = await page.locator('[data-tab="services"], [data-tab="files"], [data-tab="history"], [data-tab="settings"]').allInnerTexts();
    note('the tabs are Services, Compose, History, Settings', JSON.stringify(tabs.map((t) => t.trim())) === '["Services","Compose","History","Settings"]', JSON.stringify(tabs));
    note('running: Stop, not Start', (await shown(btn('Stop…'))) && !(await shown(btn('Start'))), `Stop ${await shown(btn('Stop…'))}, Start ${await shown(btn('Start'))}`);
    note('Open is a link button in the header', await shown(page.getByRole('link', { name: 'Open', exact: true })), 'Open');
    note('nothing to save on a plain visit', !(await shown(btn('Save'))) && !(await shown(btn('Revert'))), `Save ${await shown(btn('Save'))}`);
    note('Services shows no file pills', !(await shown(btn('compose.yaml'))), 'compose.yaml pill');
    const engines = ((await (await page.request.get(BASE + '/api/engine')).json()).engines || []).filter((e) => e.available && e.enabled).length;
    const chip = await shown(page.locator('[title="Runtime engine"]'));
    note('the engine chip only where more than one engine runs', chip === engines > 1, `${engines} engine(s), chip ${chip}`);
    await page.screenshot({ path: '/out/layout-services.png', fullPage: true });

    // Compose: the raw files as pills; an edit brings Revert and Save, on every tab.
    await page.locator('[data-tab="files"]').click();
    note('Compose opens on compose.yaml with .env beside it', (await shown(page.locator('[data-tab="compose"]'))) && (await shown(page.locator('[data-tab="env"]'))) && (await page.locator('.cm-content').count()) > 0, 'pills');
    await page.locator('.cm-content').first().click();
    await page.keyboard.press('Control+End');
    await page.keyboard.type('\n# edit');
    note('an edit brings Revert and Save', (await shown(btn('Save'))) && (await shown(btn('Revert'))), 'after an edit');
    await page.locator('[data-tab="settings"]').click();
    await sleep(500);
    const locked = await page.locator('[data-settings-locked]').count();
    note('Settings with an edit: says save or revert, with both on screen', locked > 0 && (await shown(btn('Save'))) && (await shown(btn('Revert'))), `locked line ${locked}`);
    await page.screenshot({ path: '/out/layout-settings-dirty.png', fullPage: true });
    await btn('Revert').click();
    await sleep(500);
    note('Revert: both gone again', !(await shown(btn('Save'))) && !(await shown(btn('Revert'))), 'after Revert');

    // Stopped: Start, not Stop.
    await btn('Stop…').click();
    await btn('Stop').click();
    d = await settle(page, 'stopped');
    await sleep(1500);
    note('stopped: Start, not Stop', (await shown(btn('Start'))) && !(await shown(btn('Stop…'))), `Start ${await shown(btn('Start'))}, Stop ${await shown(btn('Stop…'))}`);
    await btn('Start').click();
    d = await settle(page, 'running');

    // A phone: no sideways scroll on Services or Settings.
    const phone = await (await b.newContext(signedIn({ viewport: { width: 375, height: 800 } }))).newPage();
    for (const tab of ['', '/settings']) {
      await openPage(phone, BASE + '/?t=' + Date.now() + '#/stacks/' + NAME + tab);
      await sleep(2000);
      const w = await phone.evaluate(() => [document.documentElement.scrollWidth, window.innerWidth]);
      await phone.screenshot({ path: `/out/layout-phone${tab.replace('/', '-') || '-services'}.png`, fullPage: true });
      note(`a phone: ${tab ? 'Settings' : 'Services'} does not scroll sideways`, w[0] <= w[1], `content ${w[0]} px in ${w[1]} px`);
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
  }
  await b.close();
})();
