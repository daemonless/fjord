// An update through the panel, end to end: install syncthing 2.1.3 from the
// store, open Update, read what it says (a real package diff from the
// registry), take it, and check the container was replaced, runs 2.1.5,
// the health watch passed and Roll back is offered.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-upd';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/' + NAME)).json();
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(30000);
  try {
    await page.goto(BASE + '/?t=' + Date.now() + '#/store', { waitUntil: 'networkidle' });
    await page.getByPlaceholder(/Search apps/).fill('syncthing');
    await sleep(800);
    await page.getByRole('button', { name: /^Install/ }).first().click();
    const install = page.getByRole('button', { name: 'Install', exact: true });
    await install.waitFor();
    await page.getByLabel('Name').fill(NAME);
    if (!(await page.locator('#WEB_PORT').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
    await page.locator('#WEB_PORT').fill('3122');
    const netSel = page.locator('table select').filter({ has: page.locator('option[value="bridge"]') });
    if ((await netSel.count()) === 0) await page.getByRole('button', { name: /\+ interface/ }).first().click();
    await netSel.first().selectOption('bridge');
    await page.getByText('Advanced', { exact: true }).click();
    await page.getByPlaceholder(/Custom tag/).fill('2.1.3');
    await sleep(1500);
    await install.click();
    let d;
    for (let i = 0; i < 100; i++) { d = await detail(page).catch(() => ({})); if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break; await sleep(3000); }
    note('installed on 2.1.3', d?.status?.state === 'running' && /:2\.1\.3$/.test(d?.services?.[0]?.image || ''), `state ${d?.status?.state}, ${d?.services?.[0]?.image}`);
    const before = d.status.containers[0].id;

    // 1. The panel says what the update is, from the registry.
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + NAME, { waitUntil: 'networkidle' });
    await page.getByText(/available/).first().waitFor({ timeout: 90000 }).catch(() => {});
    await sleep(500);
    await page.getByRole('button', { name: 'Update', exact: true }).first().click();
    const line = page.getByText(/New (patch|minor|major) version|New build/).first();
    await line.waitFor({ timeout: 90000 }).catch(() => {});
    const sentence = await line.innerText().catch(() => '');
    await page.screenshot({ path: '/out/update-panel.png', fullPage: true });
    note('the panel says what the update is, in words', /New (patch|minor) version, v2\.1\.3 → v2\.1\.5/.test(sentence), `"${sentence.slice(0, 120)}"`);
    note('with a package diff from the registry', /\d+ packages? changed/.test(sentence), /package/.test(sentence) ? 'has the diff' : 'no package line');

    // 2. Take it.
    const go = page.getByRole('button', { name: /^Update \d+ service/ }).first();
    note('one service ticked, the button says so', (await go.count()) === 1 && /Update 1 service/.test(await go.innerText()), await go.innerText().catch(() => 'no button'));
    let out = '';
    page.on('response', async (r) => { if (new RegExp(`/api/stacks/${NAME}/update$`).test(r.url())) out = await r.text().catch(() => ''); });
    await go.click();
    // Busy first (the click is answered before fjordd marks the stack), then
    // not busy: reading before that compared the container with itself.
    let seen = false;
    for (let i = 0; i < 100 && !seen; i++) { d = await detail(page); seen = !!d.busy; if (!seen) await sleep(300); }
    note('the update is seen running', seen, seen ? `busy=${d.busy}` : 'never seen busy');
    for (let i = 0; i < 120; i++) { d = await detail(page); if (!d.busy) break; await sleep(3000); }
    for (let i = 0; i < 40 && !out; i++) await sleep(500);
    await sleep(2000);
    d = await detail(page);
    const after = d.status.containers[0]?.id || '';
    await page.screenshot({ path: '/out/update-done.png', fullPage: true });
    note('the container was replaced', after && after !== before, `${before.slice(0, 12)} -> ${after.slice(0, 12)}`);
    note('it runs 2.1.5', d.status?.state === 'running' && /:2\.1\.5$/.test(d.services?.[0]?.image || ''), `${d.status?.state}, ${d.services?.[0]?.image}`);
    note('the stream ended without an [error]', !!out && !/\[error\]/.test(out), out ? (out.split('\n').filter((l) => /\[error\]|\[warn\]|healthy|watch/i.test(l)).join(' | ') || 'clean') : 'no stream captured');
    note('fjordd recorded no failure', !d.state?.last_failure, JSON.stringify(d.state?.last_failure || null));
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + NAME, { waitUntil: 'networkidle' });
    await sleep(2000);
    const body = await page.locator('body').innerText();
    note('the page says Up to date and offers Roll back', /Up to date/.test(body) && /roll back/i.test(body), `${/Up to date/.test(body) ? 'Up to date' : 'no Up to date'}, ${/roll back/i.test(body) ? 'roll back offered' : 'no roll back'}`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/update-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
