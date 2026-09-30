// A failed install says so on the stack page and in fjordd's log, and its
// rows read "no container" in grey; the next action that succeeds clears it.
// Driven with a tag that does not exist, so the pull fails.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-fail';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/' + NAME)).json();
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(30000);
  try {
    await page.goto(BASE + '/', { waitUntil: 'networkidle' });
    await page.getByText('App Store', { exact: true }).first().click();
    await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
    await page.getByRole('button', { name: /^Install/ }).first().click();
    const install = page.getByRole('button', { name: 'Install', exact: true });
    await install.waitFor();
    await page.getByLabel('Name').fill(NAME);
    if (!(await page.locator('#type-openspeedtest').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
    await page.locator('#WEB_PORT').fill('3113');
    const netSel = page.locator('table select').filter({ has: page.locator('option[value="bridge"]') });
    if ((await netSel.count()) === 0) await page.getByRole('button', { name: /\+ interface/ }).first().click();
    await netSel.first().selectOption('bridge');
    await page.getByText('Advanced', { exact: true }).click();
    await page.getByPlaceholder(/Custom tag/).fill('no-such-tag-1.2.3');
    await sleep(2000); // the platform check answers 502 for a missing tag and must not block
    note('wizard lets a missing tag through (not its job to know)', !(await install.isDisabled()), `Install disabled ${await install.isDisabled()}`);
    await install.click();
    let d;
    for (let i = 0; i < 60; i++) { d = await detail(page).catch(() => ({})); if (d?.name && !d.busy) break; await sleep(3000); }
    await sleep(2500);
    // 1. The page, as someone opening it later would see it.
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + NAME, { waitUntil: 'networkidle' });
    await sleep(2000);
    const banner = page.getByText(/Install failed at/).first();
    const bannerText = await banner.innerText().catch(() => '');
    await page.screenshot({ path: '/out/outcome-failed.png', fullPage: true });
    note('stack page says the install failed, with the reason', /^Install failed at \d\d?:\d\d( [AP]M)? — .+/.test(bannerText), `"${bannerText}"`);
    note('fjordd recorded it', d?.state?.last_failure?.action === 'install' && !!d?.state?.last_failure?.message, JSON.stringify(d?.state?.last_failure || null));
    const row = page.locator('span.font-medium', { hasText: /^openspeedtest$/ }).first().locator('xpath=ancestor::div[contains(@class,"items-center")][1]');
    const rowState = await row.getByText(/no container|running|stopped/).first().innerText().catch(() => '');
    const grey = await row.locator('span.rounded-full.bg-fjord-neutral').count();
    note('the row reads "no container" in grey, not yellow', rowState === 'no container' && grey === 1, `"${rowState}", grey dots ${grey}, yellow ${await row.locator('span.rounded-full.bg-fjord-warning').count()}`);
    // 2. Fix it through the page: Change Version to latest, which updates.
    await page.getByRole('button', { name: 'More Actions' }).click();
    await page.getByRole('button', { name: /Change Version/ }).click();
    await page.getByText(/Loading published versions/).waitFor({ state: 'hidden', timeout: 30000 }).catch(() => {});
    await page.getByPlaceholder(/Custom tag/).fill('latest');
    await sleep(1500);
    await page.getByRole('button', { name: /Change & Redeploy/ }).click();
    for (let i = 0; i < 80; i++) { d = await detail(page); if (!d.busy && d.status?.state === 'running') break; await sleep(3000); }
    await sleep(2500);
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + NAME, { waitUntil: 'networkidle' });
    await sleep(2000);
    await page.screenshot({ path: '/out/outcome-recovered.png', fullPage: true });
    note('after a successful update: running, failure cleared, banner gone', d.status?.state === 'running' && !d.state?.last_failure && (await page.getByText(/Install failed at/).count()) === 0, `state ${d.status?.state}, last_failure ${JSON.stringify(d.state?.last_failure || null)}`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/outcome-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
