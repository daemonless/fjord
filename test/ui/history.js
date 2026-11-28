// What happened to a stack shows on its History tab, and on the System
// page for every stack: an install, a stop and a start made on the page,
// newest first.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-hist';
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
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
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
    await page.locator('#WEB_PORT').fill('3114');
    const netSel = page.locator('table select').filter({ has: page.locator('option[value="bridge"]') });
    if ((await netSel.count()) === 0) await page.getByRole('button', { name: /\+ interface/ }).first().click();
    await netSel.first().selectOption('bridge');
    await install.click();
    let d = await settle(page, 'running');
    note('installed and running', d?.status?.state === 'running', `state ${d?.status?.state}`);

    await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/' + NAME);
    await sleep(1500);
    await page.getByRole('button', { name: 'Stop…' }).click();
    await page.getByRole('button', { name: 'Stop', exact: true }).click();
    d = await settle(page, 'stopped');
    await page.getByRole('button', { name: 'Start', exact: true }).click();
    d = await settle(page, 'running');
    note('stopped and started on the page', d?.status?.state === 'running', `state ${d?.status?.state}`);

    // The stack's History tab.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/' + NAME);
    await sleep(1500);
    await page.locator('[data-tab="history"]').click();
    await page.locator('[data-history-entry]').first().waitFor();
    const lines = await page.locator('[data-history-entry]').allInnerTexts();
    await page.screenshot({ path: '/out/history-tab.png', fullPage: true });
    const titles = lines.map((l) => l.split('\n').find((x) => /Installed|Stopped|Started|failed/.test(x)) || '');
    note('the tab lists start, stop, install, newest first', titles.length >= 3 && /Started/.test(titles[0]) && /Stopped/.test(titles[1]) && /Installed/.test(titles[2]), JSON.stringify(titles.slice(0, 4)));
    note('nothing reads as failed', !lines.some((l) => /failed/.test(l)), JSON.stringify(lines.filter((l) => /failed/.test(l))));
    note('an action made on the page names no one else', !lines.slice(0, 3).some((l) => /command line|automatically|recovery|host started/.test(l)), JSON.stringify(lines.slice(0, 3)));
    await page.reload();
    await page.locator('[data-history-entry]').first().waitFor().catch(() => {});
    note('a reload stays on the tab', /\/history$/.test(page.url()) && (await page.locator('[data-history-entry]').count()) > 0, page.url());
    // A stop and a start made while the tab is open show up without a reload.
    const topIs = async (re) => {
      for (let i = 0; i < 20; i++) {
        if (re.test(await page.locator('[data-history-entry]').first().innerText())) return true;
        await sleep(1000);
      }
      return false;
    };
    await page.getByRole('button', { name: 'Stop…' }).click();
    await page.getByRole('button', { name: 'Stop', exact: true }).click();
    await settle(page, 'stopped');
    note('a stop shows on the open tab', await topIs(/Stopped/), await page.locator('[data-history-entry]').first().innerText());
    await page.getByRole('button', { name: 'Start', exact: true }).click();
    await settle(page, 'running');
    note('a start shows on the open tab', await topIs(/Started/), await page.locator('[data-history-entry]').first().innerText());

    // The System page, every stack.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/system/history');
    const host = page.locator('[data-host-history]');
    await host.locator('[data-history-entry]').first().waitFor();
    const first = await host.locator('[data-history-entry]').first().innerText();
    await page.screenshot({ path: '/out/history-system.png', fullPage: true });
    note('the System page lists it with the stack name', new RegExp(NAME + ': Started').test(first), JSON.stringify(first));
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
  } finally {
    await b.close();
  }
})();
