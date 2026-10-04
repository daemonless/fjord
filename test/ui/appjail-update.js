// One service of an AppJail stack, updated from the panel. The stack (t-aju:
// web and sync, sync built from a stale image) is made by appjail-update.sh,
// which also checks the jails afterwards; this is the page's half.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-aju';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/' + NAME)).json();
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(30000);
  try {
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
    for (let i = 0; i < 40 && !out; i++) await sleep(500);
    note('only sync was rebuilt', /Creating sync/.test(out) && !/(Creating|Stopping|Destroying) web/.test(out), out ? out.split('\n').filter((l) => /^\$|Creating|Destroying|Stopping|Nothing to do/.test(l)).join(' | ').slice(0, 300) : 'no stream captured');
    note('the stream ended without an [error]', !!out && !/\[error\]/.test(out), out ? (out.split('\n').filter((l) => /\[error\]/.test(l)).join(' | ') || 'clean') : 'no stream captured');
    d = await detail(page);
    note('the stack is running, no failure recorded', d.status?.state === 'running' && !d.state?.last_failure, `${d.status?.state}, ${JSON.stringify(d.state?.last_failure || null)}`);

    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + NAME, { waitUntil: 'networkidle' });
    await page.getByText(/Up to date/).first().waitFor({ timeout: 90000 }).catch(() => {});
    const body = await page.locator('body').innerText();
    await page.screenshot({ path: '/out/appjail-update-done.png', fullPage: true });
    // No Roll back: nothing can pin an AppJail service to an image yet.
    note('the page says Up to date, and offers no Roll back it cannot do', /Up to date/.test(body) && !/roll back/i.test(body), `${/Up to date/.test(body) ? 'Up to date' : 'no Up to date'}, ${/roll back/i.test(body) ? 'roll back offered' : 'no roll back'}`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/appjail-update-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
