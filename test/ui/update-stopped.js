// Updating a stopped stack leaves it stopped (tailscale came up on its own,
// 2026-10-11). The stack (t-ups, one service behind a staged older image)
// is made by update-stopped.sh; this page stops it, takes the update, checks
// nothing came up, then starts it.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const NAME = process.env.STACK;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${NAME} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/' + NAME)).json();
const settle = async (page) => {
  let d;
  for (let i = 0; i < 200; i++) { d = await detail(page); if (!d.busy) break; await sleep(3000); }
  return d;
};
const open = async (page) => {
  await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + NAME, { waitUntil: 'networkidle' });
  await sleep(1000);
};
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(30000);
  try {
    // 1. Stop, through the confirm.
    await open(page);
    await page.getByRole('button', { name: /^Stop…/ }).click();
    await page.getByRole('button', { name: 'Stop', exact: true }).click();
    await sleep(2000);
    let d = await settle(page);
    note('stopped', d.status?.state !== 'running' && d.state?.desired_state === 'stopped', `state ${d.status?.state}, desired ${d.state?.desired_state}`);

    // 2. Update it, stopped.
    await open(page);
    await page.getByText(/available/).first().waitFor({ timeout: 90000 }).catch(() => {});
    await page.getByRole('button', { name: 'Update', exact: true }).first().click();
    const go = page.getByRole('button', { name: /^Update \d+ service/ }).first();
    await go.waitFor({ timeout: 90000 }).catch(() => {});
    await page.screenshot({ path: `/out/${NAME}-panel.png`, fullPage: true });
    note('the panel offers the update while stopped', (await go.count()) === 1, await go.innerText().catch(() => 'no "Update N services" button'));
    let out = '';
    page.on('response', async (r) => { if (new RegExp(`/api/stacks/${NAME}/update$`).test(r.url())) out = await r.text().catch(() => ''); });
    await go.click();
    await sleep(2000);
    d = await settle(page);
    for (let i = 0; i < 40 && !out; i++) await sleep(500);
    await sleep(3000);
    d = await detail(page);
    await page.screenshot({ path: `/out/${NAME}-updated.png`, fullPage: true });
    note('it says it was left stopped', /updated and left stopped/.test(out), out ? out.trim().split('\n').slice(-3).join(' | ').slice(0, 300) : 'no stream captured');
    note('the stream ended without an [error]', !!out && !/\[error\]/.test(out), out ? (out.split('\n').filter((l) => /\[error\]/.test(l)).join(' | ') || 'clean') : 'no stream captured');
    note('still stopped after the update', d.status?.state !== 'running' && !(d.status?.containers || []).some((c) => c.state === 'running'), `state ${d.status?.state}`);
    note('desired state still stopped (no start at boot)', d.state?.desired_state === 'stopped', `desired ${d.state?.desired_state}`);
    note('no failure recorded', !d.state?.last_failure, JSON.stringify(d.state?.last_failure || null));

    // 3. Start runs it.
    await open(page);
    await page.getByRole('button', { name: 'Start', exact: false }).filter({ hasText: /^\s*Start\s*$/ }).first().click();
    await sleep(2000);
    d = await settle(page);
    for (let i = 0; i < 20 && d.status?.state !== 'running'; i++) { await sleep(3000); d = await detail(page); }
    await page.screenshot({ path: `/out/${NAME}-started.png`, fullPage: true });
    note('Start brings it up', d.status?.state === 'running' && d.state?.desired_state === 'running', `state ${d.status?.state}, desired ${d.state?.desired_state}`);
    await open(page);
    await page.getByText(/Up to date/).first().waitFor({ timeout: 90000 }).catch(() => {});
    const body = await page.locator('body').innerText();
    note('and the page says Up to date', /Up to date/.test(body), (body.match(/[^\n]*(Up to date|available)[^\n]*/) || ['neither'])[0].trim().slice(0, 120));
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: `/out/${NAME}-error.png`, fullPage: true }).catch(() => {});
  }
  await b.close();
})();
