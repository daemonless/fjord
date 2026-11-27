// The wizard says what "private" is; a busy stack says why its buttons are
// greyed -- read off the pages.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const ctx = await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }));
  const page = await ctx.newPage();
  page.setDefaultTimeout(20000);
  try {
    await openPage(page, BASE + '/');
    await page.getByText('App Store', { exact: true }).first().click();
    await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
    await page.waitForTimeout(800); // let the search filter, or Install opens another app
    await page.getByRole('button', { name: /^Install/ }).first().click();
    await page.getByRole('button', { name: 'Install', exact: true }).waitFor();
    await page.getByLabel('Name', { exact: true }).fill('t-word');
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
    // Opened before the install saves the stack, the page falls back to
    // the list and stays there: wait for the stack to exist.
    for (let i = 0; i < 100 && !(await page.request.get(BASE + '/api/stacks/t-word')).ok(); i++) await sleep(200);
    await other.goto(BASE + '/#/stacks/t-word', { waitUntil: 'load' });
    const reasonLine = other.locator('[data-stack-note]').filter({ hasText: /come back when it finishes/ }).first();
    await reasonLine.waitFor({ timeout: 30000 }).catch(() => {});
    const reason = await reasonLine.innerText().catch(() => '');
    // Start while anything is down, Stop while anything is up: whichever is
    // shown is disabled and says why.
    const action = other.getByRole('button', { name: /^(Start|Stop…)$/ }).first();
    const actionName = (await action.innerText().catch(() => '')).trim();
    const actionTitle = await action.getAttribute('title').catch(() => null);
    const more = await other.getByRole('button', { name: /come back when it finishes/ }).count();
    await other.screenshot({ path: '/out/wording-busy.png', fullPage: true });
    note('mid-pull: the line under the header says why', /^Installing… — actions come back when it finishes$/.test(reason), `"${reason}"`);
    note('mid-pull: Start or Stop is disabled with the reason', !!actionName && (await action.isDisabled().catch(() => false)) && actionTitle === reason, `${actionName || 'neither shown'}, title "${actionTitle}"`);
    note('mid-pull: More Actions carries it too', more >= 1, `${more} button(s) titled with the reason (Start or Stop, and More Actions, expected)`);

    for (let i = 0; i < 80; i++) {
      const d = await (await other.request.get(BASE + '/api/stacks/t-word')).json().catch(() => ({}));
      if (d?.status?.state === 'running' && !d.busy) break;
      await sleep(3000);
    }
    await sleep(2500);
    const gone = await other.locator('[data-stack-note]').filter({ hasText: /come back when it finishes/ }).count();
    const afterBtn = other.getByRole('button', { name: /^(Start|Stop…)$/ }).first();
    const afterName = (await afterBtn.innerText().catch(() => '')).trim();
    const afterTitle = await afterBtn.getAttribute('title').catch(() => 'n/a');
    await other.screenshot({ path: '/out/wording-after.png', fullPage: true });
    note('after: reason gone, Start or Stop has no title', gone === 0 && !!afterName && !afterTitle, `reason lines ${gone}, ${afterName || 'neither shown'} title ${JSON.stringify(afterTitle)}`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
  }
  await b.close();
})();
