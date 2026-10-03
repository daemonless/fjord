// The wizard says what "private" is; a busy stack says why its buttons are
// greyed -- read off the pages.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const ctx = await b.newContext({ viewport: { width: 1400, height: 1000 } });
  const page = await ctx.newPage();
  page.setDefaultTimeout(20000);
  try {
    await page.goto(BASE + '/', { waitUntil: 'networkidle' });
    await page.getByText('App Store', { exact: true }).first().click();
    await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
    await page.waitForTimeout(800); // let the search filter, or Install opens another app
    await page.getByRole('button', { name: /^Install/ }).first().click();
    await page.getByRole('button', { name: 'Install', exact: true }).waitFor();
    await page.getByLabel('Name').fill('t-word');
    if (!(await page.locator('#type-openspeedtest').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
    await page.locator('#WEB_PORT').fill('3109');
    // A one-service app starts with no interface row; add one to see the picker.
    await page.getByRole('button', { name: /\+ interface/ }).first().click();
    const netSel = page.locator('table select').filter({ has: page.locator('option[value="bridge"]') }).first();
    const opts = await netSel.locator('option').allInnerTexts();
    const priv = opts.map((o) => o.trim()).find((o) => o.startsWith('private'));
    await page.screenshot({ path: '/out/wording-wizard.png', fullPage: true });
    note('wizard: private option says what it is', /this stack's own network, not reachable from your LAN/.test(priv || ''), `"${priv}"`);
    note('wizard: no "only this stack" left', !/only this stack|only it can reach/.test(await page.content()), 'page text');
    await netSel.selectOption('bridge');
    const other = await ctx.newPage();
    await page.getByRole('button', { name: 'Install', exact: true }).click();

    // The image was removed before the run, so the pull takes a while: poll
    // the stack page for the reason line rather than guess a delay.
    await other.goto(BASE + '/#/stacks/t-word', { waitUntil: 'load' });
    const reasonLine = other.locator('span.text-xs').filter({ hasText: /come back when it finishes/ }).first();
    await reasonLine.waitFor({ timeout: 30000 }).catch(() => {});
    const reason = await reasonLine.innerText().catch(() => '');
    const start = other.getByRole('button', { name: /^Start$/ }).first();
    const startTitle = await start.getAttribute('title').catch(() => null);
    const stopTitle = await other.getByRole('button', { name: /^Stop/ }).first().getAttribute('title').catch(() => null);
    const more = await other.getByRole('button', { name: /come back when it finishes/ }).count();
    await other.screenshot({ path: '/out/wording-busy.png', fullPage: true });
    note('mid-pull: the action row says why', /^Installing… — actions come back when it finishes$/.test(reason), `"${reason}"`);
    note('mid-pull: Start disabled with the reason', (await start.isDisabled()) && startTitle === reason, `title "${startTitle}"`);
    note('mid-pull: Stop carries the reason', stopTitle === reason, `title "${stopTitle}"`);
    note('mid-pull: More Actions carries it too', more >= 1, `${more} button(s) titled with the reason (Start, Stop, More Actions expected)`);

    for (let i = 0; i < 80; i++) {
      const d = await (await other.request.get(BASE + '/api/stacks/t-word')).json().catch(() => ({}));
      if (d?.status?.state === 'running' && !d.busy) break;
      await sleep(3000);
    }
    await sleep(2500);
    const gone = await other.locator('span.text-xs').filter({ hasText: /come back when it finishes/ }).count();
    const startAfter = await other.getByRole('button', { name: /^Start$/ }).first().getAttribute('title').catch(() => 'n/a');
    await other.screenshot({ path: '/out/wording-after.png', fullPage: true });
    note('after: reason gone, Start has no title', gone === 0 && !startAfter, `reason lines ${gone}, Start title ${JSON.stringify(startAfter)}`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
  }
  await b.close();
})();
