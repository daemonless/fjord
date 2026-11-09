// A failed action says so on its Stacks row: Start clicked on the list for a
// stack whose image tag does not exist ends in a red "Start failed" pill with
// fjord's reason as its tooltip, and it is gone once the next Start is clean.
// The stack is t-rowfail.
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const H = { Origin: BASE, 'Content-Type': 'application/json' };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const compose = (tag) => `services:\n  web:\n    image: ghcr.io/daemonless/openspeedtest:${tag}\n`;
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 900 } }))).newPage();
  page.setDefaultTimeout(30000);
  const save = (tag) => page.request.post(BASE + '/api/stacks/t-rowfail/save', { headers: H, data: { compose: compose(tag), env: '' } });
  try {
    if (!(await save('no-such-tag-fjord-test')).ok()) throw new Error('saving t-rowfail');
    await openPage(page, BASE + '/#/stacks');
    await page.getByRole('button', { name: /^List$/ }).click().catch(() => {});
    await sleep(1500);
    const row = page.locator('div.cursor-pointer', { hasText: 't-rowfail' }).first();
    const pill = row.getByText('Start failed');
    await row.locator('button[title="Start"]').click();
    await pill.waitFor({ timeout: 120000 }).catch(() => {});
    await page.screenshot({ path: '/out/row-failed.png' });
    const tip = (await pill.getAttribute('title').catch(() => '')) || '';
    note('the row says Start failed, with the reason as its tooltip', (await pill.isVisible().catch(() => false)) && /no such version/.test(tip), `tooltip: "${tip.slice(0, 160)}"`);
    await openPage(page, BASE + '/#/stacks');
    await sleep(1500);
    note('still said after a reload', await row.getByText('Start failed').isVisible().catch(() => false), (await row.innerText().catch(() => '')).replace(/\s+/g, ' '));
    if (!(await save('latest')).ok()) throw new Error('saving latest');
    await row.locator('button[title="Start"]').click();
    await row.getByText('Running').first().waitFor({ timeout: 180000 }).catch(() => {});
    // The outcome is recorded when the Start's stay-up check ends, not when it runs.
    const t0 = Date.now();
    await row.getByText('Start failed').waitFor({ state: 'detached', timeout: 180000 }).catch(() => {});
    console.log(`cleared ${Math.round((Date.now() - t0) / 1000)} s after Running`);
    const after = (await row.innerText().catch(() => '')).replace(/\s+/g, ' ');
    await page.screenshot({ path: '/out/row-failed-cleared.png' });
    note('a clean Start clears it', /Running/.test(after) && !/failed/i.test(after), after);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/row-failed-error.png' }).catch(() => {});
  }
  await page.request.delete(BASE + '/api/stacks/t-rowfail?data=1', { headers: H, timeout: 120000 }).catch(() => {});
  await b.close();
})();
