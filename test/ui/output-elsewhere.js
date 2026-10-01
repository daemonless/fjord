// An action this page did not start still shows its output: the companion
// starts an update over the API (the image removed first, so it pulls), and
// a fresh page opened on the stack mid-action shows what fjordd is doing in
// the Output pane, then the end of it, instead of "Waiting for output…".
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-out';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/' + NAME)).json();
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(30000);
  try {
    if (process.env.STEP === 'install') {
      await page.goto(BASE + '/?t=' + Date.now() + '#/store', { waitUntil: 'networkidle' });
      await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
      await sleep(800);
      await page.getByRole('button', { name: /^Install/ }).first().click();
      const install = page.getByRole('button', { name: 'Install', exact: true });
      await install.waitFor();
      await page.getByLabel('Name').fill(NAME);
      if (!(await page.locator('#WEB_PORT').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
      await page.locator('#WEB_PORT').fill('3123');
      const netSel = page.locator('table select').filter({ has: page.locator('option[value="bridge"]') });
      if ((await netSel.count()) === 0) await page.getByRole('button', { name: /\+ interface/ }).first().click();
      await netSel.first().selectOption('bridge');
      await install.click();
      let d;
      for (let i = 0; i < 80; i++) { d = await detail(page).catch(() => ({})); if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break; await sleep(3000); }
      note('installed', d?.status?.state === 'running', `state ${d?.status?.state}`);
    } else {
      // The update was started over the API a moment ago by the companion.
      let d;
      for (let i = 0; i < 100; i++) { d = await detail(page); if (d.busy) break; await sleep(300); }
      note('fjordd is busy with an action this page did not start', !!d.busy, `busy=${d.busy}`);
      await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + NAME, { waitUntil: 'networkidle' });
      const term = page.locator('.xterm, [class*="terminal"]').first();
      let text = '';
      for (let i = 0; i < 40; i++) { text = await page.locator('body').innerText(); if (/\$ podman/.test(text)) break; await sleep(500); }
      await page.screenshot({ path: '/out/output-elsewhere-mid.png', fullPage: true });
      note('mid-action: the Output pane shows what fjordd is doing', /\$ podman/.test(text) && !/Waiting for output/.test(text), /\$ podman/.test(text) ? 'podman lines on screen' : 'nothing but "Waiting for output…"');
      const doneBadge = () => page.locator('span', { hasText: /^\s*Done\s*$/ }).count();
      note('mid-action: the terminal reads as running, not Done', (await doneBadge()) === 0, `${await doneBadge()} Done badge(s)`);
      for (let i = 0; i < 120; i++) { d = await detail(page); if (!d.busy) break; await sleep(3000); }
      await sleep(2500);
      text = await page.locator('body').innerText();
      await page.screenshot({ path: '/out/output-elsewhere-done.png', fullPage: true });
      note('after: the terminal reads Done', (await doneBadge()) === 1, `${await doneBadge()} Done badge(s)`);
      note('after: the end of the output is on screen too', /\$ podman start|\[fjord\]|healthy|\$ podman-compose/.test(text) && !/Waiting for output/.test(text), text.includes('podman start') ? 'podman start line present' : 'end not shown');
      note('and the stack runs', d.status?.state === 'running' && !d.state?.last_failure, `${d.status?.state}`);
      void term;
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/output-elsewhere-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
