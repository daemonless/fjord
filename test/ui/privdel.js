// A stack's private network goes when the stack is deleted, through the pages.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(20000);
  const nets = async () => (await (await page.request.get(BASE + '/api/networks')).json()).map((n) => n.name);
  try {
    await page.goto(BASE + '/', { waitUntil: 'networkidle' });
    await page.getByText('App Store', { exact: true }).first().click();
    await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
    await page.getByRole('button', { name: /^Install/ }).first().click();
    await page.getByRole('button', { name: 'Install', exact: true }).waitFor();
    await page.getByLabel('Name').fill('t-pd');
    if (!(await page.locator('#type-openspeedtest').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
    await page.locator('#WEB_PORT').fill('3107');
    await page.locator('table select').first().selectOption('bridge');
    await page.getByRole('button', { name: '+ interface' }).first().click();
    await sleep(300);
    await page.locator('table select').nth(1).selectOption('private');
    await page.getByRole('button', { name: 'Install', exact: true }).click();
    for (let i = 0; i < 60; i++) {
      const d = await (await page.request.get(BASE + '/api/stacks/t-pd')).json().catch(() => ({}));
      if (d?.status?.state === 'running') break;
      await sleep(3000);
    }
    const before = await nets();
    note('install made t-pd_priv', before.includes('t-pd_priv'), before.filter((n) => n.startsWith('t-pd')).join(', ') || 'none');
    await page.goto(BASE + '/#/stacks/t-pd', { waitUntil: 'networkidle' });
    await sleep(1500);
    await page.getByTitle('More Actions').click();
    await page.getByRole('button', { name: /Delete…/ }).click();
    await page.getByRole('button', { name: 'Delete', exact: true }).click();
    for (let i = 0; i < 40; i++) {
      const r = await page.request.get(BASE + '/api/stacks/t-pd');
      if (r.status() === 404) break;
      await sleep(2000);
    }
    await sleep(2000);
    const after = await nets();
    note('deleting t-pd from its page removes t-pd_priv', !after.includes('t-pd_priv'), after.includes('t-pd_priv') ? 'still listed' : 'gone');
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/privdel-error.png', fullPage: true });
  }
  await b.close();
})();
