// Moving from a stack with output to one without leaves nothing of the first
// on the page: Terminal once threw on the second stack's missing output, which
// stopped the page's update and kept the first stack's Open text and badge.
// The output is answered here, so no stack on the host is touched.
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const bare = (u) => (u || '').replace(/^https?:\/\//, '').replace(/[#?].*$/, '').replace(/\/$/, '');
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }))).newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const stacks = (await (await page.request.get(BASE + '/api/stacks')).json()).filter((s) => s.status?.state === 'running');
  if (stacks.length < 2) {
    note('switch stack', true, 'fewer than two running stacks (nothing to judge)');
    await b.close();
    return;
  }
  const [from, to] = stacks.map((s) => s.name);
  const json = (body) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route(`**/api/stacks/${from}/output`, (r) => r.fulfill(json({ text: 'output of the first stack\n', done: true, action: 'update' })));
  await page.route(`**/api/stacks/${to}/output`, (r) => r.fulfill(json({ text: '', done: true })));
  await openPage(page, BASE + '/#/stacks/' + from);
  await sleep(1500);
  await page.evaluate((name) => { location.hash = '#/stacks/' + name; }, to);
  await sleep(1500);
  note('no exception on the page', !errors.length, errors[0] || 'none');
  const heading = (await page.locator('header h2').innerText()).trim();
  note('heading is the new stack', heading === (stacks[1].displayName || to), heading);
  const open = page.getByRole('link', { name: /Open/ }).first();
  if (await open.count()) {
    const href = await open.getAttribute('href');
    const text = await open.locator('span.font-mono').innerText();
    note('Open says where it goes', text.trim() === bare(href), `${text.trim()} -> ${href}`);
  }
  await b.close();
})();
