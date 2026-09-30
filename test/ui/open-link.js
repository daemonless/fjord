// Where each running stack's Open link points, read off the stack page: an
// address a browser on the LAN can reach, never a stack's private 10.x one
// (#46), and never nothing. Read-only.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1000 } })).newPage();
  const stacks = (await (await page.request.get(BASE + '/api/stacks')).json()).filter((s) => s.status?.state === 'running').map((s) => s.name);
  if (!stacks.length) note('open links', true, 'no running stack to read (nothing to judge)');
  for (const s of stacks) {
    await page.goto(BASE + '/#/stacks/' + s, { waitUntil: 'networkidle' });
    await new Promise((r) => setTimeout(r, 1500));
    const href = await page.getByRole('link', { name: /Open/ }).first().getAttribute('href').catch(() => '');
    note(`${s}: Open link reaches the LAN`, !!href && !/\/\/10\./.test(href), href || 'no Open link');
  }
  await b.close();
})();
