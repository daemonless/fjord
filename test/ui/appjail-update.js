// One service of an AppJail stack, updated from the panel, then rolled back.
// The stack (t-aju: web and sync, sync built from a stale image) is made by
// appjail-update.sh, which also checks the jails after each step; this is
// the page's half. STEP=rollback: Roll back sync from its row.
const { chromium } = require('playwright-core');
const { signedIn } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-aju';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/' + NAME)).json();
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(30000);
  try {
    if (process.env.STEP === 'rollback') {
      await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + NAME, { waitUntil: 'networkidle' });
      await page.getByRole('button', { name: '↶ roll back' }).first().waitFor({ timeout: 90000 });
      await page.getByRole('button', { name: '↶ roll back' }).first().click();
      const sure = page.getByRole('button', { name: 'Roll back', exact: true });
      const asked = (await page.locator('body').innerText()).match(/Roll sync back to [^\n?]+\?/);
      note('Roll back says where to, and asks first', !!asked, asked ? asked[0] : 'no confirmation shown');
      await sure.click();
      let d, seen = false;
      for (let i = 0; i < 100 && !seen; i++) { d = await detail(page); seen = !!d.busy; if (!seen) await sleep(300); }
      for (let i = 0; i < 120; i++) { d = await detail(page); if (!d.busy) break; await sleep(3000); }
      await sleep(3000);
      d = await detail(page);
      note('rolled back, running, no failure recorded', d.status?.state === 'running' && !d.state?.last_failure, `${d.status?.state}, ${JSON.stringify(d.state?.last_failure || null)}`);
      note('the director pins sync to a digest', /from: ghcr\.io\/daemonless\/syncthing:latest@sha256:[0-9a-f]{64}/.test(d.director || ''), (d.director || '').split('\n').find((l) => /syncthing/.test(l))?.trim() || 'no syncthing line');
      await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + NAME, { waitUntil: 'networkidle' });
      await sleep(3000);
      await page.screenshot({ path: '/out/appjail-rollback-done.png', fullPage: true });
      const body = await page.locator('body').innerText();
      note('the row says pinned, with unpin', /pinned · unpin/.test(body), /pinned/.test(body) ? 'pinned shown' : 'no pinned badge');
      await b.close();
      return;
    }
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + NAME, { waitUntil: 'networkidle' });
    await page.getByText(/available/).first().waitFor({ timeout: 90000 }).catch(() => {});
    await sleep(500);
    await page.getByRole('button', { name: 'Update', exact: true }).first().click();
    const go = page.getByRole('button', { name: /^Update \d+ service/ }).first();
    await go.waitFor({ timeout: 90000 }).catch(() => {});
    await page.screenshot({ path: '/out/appjail-update-panel.png', fullPage: true });
    const panel = await page.locator('body').innerText();
    note('the panel offers the one service that is behind', (await go.count()) === 1 && /Update 1 service/.test(await go.innerText()), await go.innerText().catch(() => 'no "Update N services" button'));
    note('and says the rest keep running', /recreates only sync\. The rest keep running/.test(panel.replace(/\s+/g, ' ')), (panel.match(/[^\n]*(recreate|whole stack)[^\n]*/i) || ['no such sentence'])[0].trim().slice(0, 140));

    let out = '';
    page.on('response', async (r) => { if (new RegExp(`/api/stacks/${NAME}/update$`).test(r.url())) out = await r.text().catch(() => ''); });
    await go.click();
    let d, seen = false;
    for (let i = 0; i < 100 && !seen; i++) { d = await detail(page); seen = !!d.busy; if (!seen) await sleep(300); }
    note('the update is seen running', seen, seen ? `busy=${d.busy}` : 'never seen busy');
    for (let i = 0; i < 120; i++) { d = await detail(page); if (!d.busy) break; await sleep(3000); }
    // Not busy comes when the stay-up check starts; the stream ends with it,
    // 30 seconds on.
    for (let i = 0; i < 240 && !out; i++) await sleep(500);
    note('only sync was rebuilt', /Creating sync/.test(out) && !/(Creating|Stopping|Destroying) web/.test(out), out ? out.split('\n').filter((l) => /^\$|Creating|Destroying|Stopping|Nothing to do/.test(l)).join(' | ').slice(0, 300) : 'no stream captured');
    note('the stay-up check ran on sync', /checking sync stays up for 30s/.test(out) && /sync has stayed up for 30s/.test(out), out ? (out.split('\n').filter((l) => /stays up|stayed up/.test(l)).join(' | ') || 'no stay-up lines') : 'no stream captured');
    note('the stream ended without an [error]', !!out && !/\[error\]/.test(out), out ? (out.split('\n').filter((l) => /\[error\]/.test(l)).join(' | ') || 'clean') : 'no stream captured');
    d = await detail(page);
    note('the stack is running, no failure recorded', d.status?.state === 'running' && !d.state?.last_failure, `${d.status?.state}, ${JSON.stringify(d.state?.last_failure || null)}`);

    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + NAME, { waitUntil: 'networkidle' });
    await page.getByText(/Up to date/).first().waitFor({ timeout: 90000 }).catch(() => {});
    const body = await page.locator('body').innerText();
    await page.screenshot({ path: '/out/appjail-update-done.png', fullPage: true });
    // The image it ran before is recorded, and the director can be pinned
    // to it: Roll back is offered.
    note('the page says Up to date and offers Roll back', /Up to date/.test(body) && /roll back/i.test(body), `${/Up to date/.test(body) ? 'Up to date' : 'no Up to date'}, ${/roll back/i.test(body) ? 'roll back offered' : 'no roll back'}`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/appjail-update-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
