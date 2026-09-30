// The parts that used to be mouse-only work from the keyboard: an App Store
// card opens on Enter, the stack name is a button that opens the rename box,
// and the two resize sashes are focusable sliders. Read-only; needs a
// running stack on the host to open (any).
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1000 } })).newPage();
  page.setDefaultTimeout(20000);
  try {
    await page.goto(BASE + '/#/store', { waitUntil: 'networkidle' });
    await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
    await sleep(800);
    const card = page.locator('[role="button"]').filter({ hasText: 'OpenSpeedTest' }).first();
    await card.focus();
    await page.keyboard.press('Enter');
    await sleep(800);
    const details = await page.getByRole('heading', { name: /OpenSpeedTest/ }).count();
    const body = await page.locator('body').innerText();
    note('App Store: Enter on a focused card opens its details', details > 0 && /Install/.test(body), details ? 'details open' : 'nothing opened');
    await page.keyboard.press('Escape');

    const stacks = (await (await page.request.get(BASE + '/api/stacks')).json()).map((s) => s.name);
    if (!stacks.length) { note('stack page', true, 'no stack on the host to open (nothing to judge)'); await b.close(); return; }
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + stacks[0], { waitUntil: 'networkidle' });
    await sleep(1500);
    const rename = page.locator('h2 button', { hasText: stacks[0] }).first();
    note('stack page: the name is a button inside the heading', (await rename.count()) === 1, `${await rename.count()} found`);
    await rename.focus();
    await page.keyboard.press('Enter');
    await sleep(500);
    const holding = await page.locator('input').evaluateAll((els, name) => els.filter((e) => e.value === name).length, stacks[0]);
    note('stack page: Enter on the name opens the rename box', holding >= 1, `${holding} input(s) holding the name`);
    await page.keyboard.press('Escape');
    const sliders = page.locator('[role="slider"]');
    const n = await sliders.count();
    note('the resize sashes are sliders with a value', n === 2 && (await sliders.first().getAttribute('aria-valuenow')) !== null, `${n} slider(s)`);
    const before = +(await sliders.first().getAttribute('aria-valuenow'));
    await sliders.first().focus();
    await page.keyboard.press('ArrowRight');
    await sleep(300);
    const after = +(await sliders.first().getAttribute('aria-valuenow'));
    note('ArrowRight on the sidebar sash widens it', after === before + 16, `${before} -> ${after}`);
    await page.keyboard.press('Enter');
    await sleep(300);
    note('Enter on the sash resets it', +(await sliders.first().getAttribute('aria-valuenow')) === 288, `${await sliders.first().getAttribute('aria-valuenow')}`);
    await page.screenshot({ path: '/out/keys.png', fullPage: true });
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/keys-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
