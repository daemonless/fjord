// Apply after a Save really recreates the changed service, even with an exec
// session holding the old container -- zensical's failure. Through the pages.
// STEP=install: install t-apply (WEB_PORT 3110). STEP=apply: change the port
// to 3111 on the .env tab, Save, reload, Apply Changes, check the outcome.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/t-apply')).json();
const banner = (page) => page.getByText(/still use the old configuration/);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(20000);
  try {
    if (process.env.STEP === 'install') {
      await page.goto(BASE + '/', { waitUntil: 'networkidle' });
      await page.getByText('App Store', { exact: true }).first().click();
      await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
      await page.getByRole('button', { name: /^Install/ }).first().click();
      await page.getByRole('button', { name: 'Install', exact: true }).waitFor();
      await page.getByLabel('Name').fill('t-apply');
      if (!(await page.locator('#type-openspeedtest').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
      await page.locator('#WEB_PORT').fill('3110');
      await page.locator('table select').first().selectOption('bridge');
      await page.getByRole('button', { name: 'Install', exact: true }).click();
      let d;
      for (let i = 0; i < 60; i++) { d = await detail(page).catch(() => ({})); if (d?.status?.state === 'running' && !d.busy) break; await sleep(3000); }
      note('install t-apply', d?.status?.state === 'running', `container ${d?.status?.containers?.[0]?.id?.slice(0, 12)}`);
    } else {
      const before = (await detail(page)).status.containers[0].id;
      await page.goto(BASE + '/#/stacks/t-apply', { waitUntil: 'networkidle' });
      await sleep(1500);
      await page.getByRole('button', { name: '.env', exact: true }).click();
      await page.locator('.cm-content').click();
      await page.keyboard.press('Control+A');
      const env = (await detail(page)).env.replace('WEB_PORT=3110', 'WEB_PORT=3111');
      await page.keyboard.insertText(env);
      await page.getByRole('button', { name: 'Save', exact: true }).first().click();
      await sleep(3000);
      const pend = (await detail(page)).state?.pending_services || [];
      note('Save records the changed service', pend.includes('openspeedtest'), `pending: ${pend.join(', ') || 'none'}`);
      note('banner after Save', await banner(page).isVisible(), 'shown');
      await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/t-apply', { waitUntil: 'networkidle' });
      await sleep(2000);
      note('banner survives a reload', await banner(page).isVisible(), (await banner(page).isVisible()) ? 'still shown' : 'gone after reload');
      // What the page receives for Apply -- the same text its terminal shows
      // (the terminal keeps only the visible rows in the page).
      let applied = '';
      page.on('response', async (r) => { if (/\/api\/stacks\/t-apply\/up$/.test(r.url())) applied = await r.text().catch(() => ''); });
      await page.getByRole('button', { name: 'Apply Changes', exact: true }).click();
      await sleep(2000);
      let d;
      for (let i = 0; i < 80; i++) { d = await detail(page); if (!d.busy && d.status?.state === 'running') break; await sleep(3000); }
      await sleep(3000);
      for (let i = 0; i < 20 && !applied; i++) await sleep(500);
      const out = applied;
      console.log('--- Apply output (as the page got it):\n' + out.split('\n').filter((l) => /fjord|warn|error|refused|podman-compose|exec/.test(l)).join('\n') + '\n---');
      await page.screenshot({ path: '/out/apply-after.png', fullPage: true });
      const after = d.status.containers[0].id;
      const port = (d.status.containers[0].ports || []).map((p) => p.hostPort).join(',');
      note('Apply replaced the container', after !== before, `${before.slice(0, 12)} -> ${after.slice(0, 12)}`);
      note('the new container publishes 3111', /3111/.test(port), `ports ${port}`);
      note('the refusal was handled, not hidden', /recreate refused/.test(out) && !/\[error\]/.test(out), /recreate refused/.test(out) ? 'force-removed and retried' : 'no refusal in the output');
      note('nothing left pending', !(d.state?.pending_services || []).length, `pending: ${(d.state?.pending_services || []).join(', ') || 'none'}`);
      note('banner gone', !(await banner(page).isVisible()), (await banner(page).isVisible()) ? 'still shown' : 'gone');
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/apply-error.png', fullPage: true });
  }
  await b.close();
})();
