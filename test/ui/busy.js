// While an install pulls, the stack reads as busy, not as a problem, and a
// second up is refused -- through the pages.
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
    await page.getByRole('button', { name: /^Install/ }).first().click();
    await page.getByRole('button', { name: 'Install', exact: true }).waitFor();
    await page.getByLabel('Name').fill('t-busy');
    if (!(await page.locator('#type-openspeedtest').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
    await page.locator('#WEB_PORT').fill('3108');
    await page.locator('table select').first().selectOption('bridge');
    await page.getByRole('button', { name: 'Install', exact: true }).click();

    // A second tab, as someone else looking at the same host mid-pull. The
    // pull is real (the image was removed) but not slow, so the moment is
    // caught by asking, not by waiting a guessed number of seconds.
    const other = await ctx.newPage();
    const t0 = Date.now();
    let seen = false;
    for (let i = 0; i < 200 && !seen; i++) {
      const d = await (await other.request.get(BASE + '/api/stacks/t-busy')).json().catch(() => ({}));
      seen = d?.busy === 'installing';
      if (!seen) await sleep(300);
    }
    note('mid-pull: fjordd says installing', seen, seen ? `busy=installing seen ${Date.now() - t0}ms after Install` : 'the install was never seen busy (pull already done?)');
    // The refusal first, the moment the install is seen: it is the contract.
    const api = await other.request.post(BASE + '/api/stacks/t-busy/up', { headers: { Origin: BASE } });
    const apiText = (await api.text()).trim();
    await other.goto(BASE + '/#/stacks/t-busy', { waitUntil: 'load' });
    const pill = other.locator('span.rounded-full').filter({ hasText: /Installing/i }).first();
    await pill.waitFor({ timeout: 10000 }).catch(() => {});
    // Read together, so a pull ending between two reads cannot split them.
    const [badge, startDisabled, stillBusy] = await Promise.all([
      other.locator('h2 + span, span.rounded-full').filter({ hasText: /Installing|Starting|Stopped|Running/i }).first().innerText().catch(() => ''),
      other.getByRole('button', { name: /^Start$/ }).first().isDisabled().catch(() => null),
      (async () => (await (await other.request.get(BASE + '/api/stacks/t-busy')).json().catch(() => ({})))?.busy)(),
    ]);
    const problems = await other.getByRole('button', { name: /Problems/ }).first().innerText().catch(() => 'no Problems filter');
    await other.screenshot({ path: '/out/busy-midpull.png', fullPage: true });
    note('mid-pull: a second up is refused, naming the install', api.status() === 409 && /installing/.test(apiText), `${api.status()} "${apiText.split('\n')[0]}"`);
    // The page can only be judged while the install is still running. A
    // pull of a few hundred MB on a fast link is over in seconds, so when it
    // ended before the page was read, that is said rather than guessed.
    if (stillBusy === 'installing') {
      note('mid-pull: badge says Installing', /Installing/i.test(badge), `badge "${badge}" at ${Date.now() - t0}ms`);
      note('mid-pull: not counted as a problem', !/Problems\s*[1-9]/.test(problems), `filter "${problems.replace(/\s+/g, ' ')}"`);
      note('mid-pull: Start disabled', startDisabled === true, `Start disabled ${startDisabled}`);
    } else {
      console.log(`SKIP mid-pull page checks: the install finished ${Date.now() - t0}ms after Install, before the page could be read (badge "${badge}")`);
    }

    for (let i = 0; i < 80; i++) {
      const d = await (await other.request.get(BASE + '/api/stacks/t-busy')).json().catch(() => ({}));
      if (d?.status?.state === 'running' && !d.busy) break;
      await sleep(3000);
    }
    await sleep(2500);
    const after = await other.locator('span.rounded-full').filter({ hasText: /Installing|Starting|Stopped|Running/i }).first().innerText().catch(() => '');
    note('after the pull: badge back to the real state', /Running/i.test(after), `badge "${after}"`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
  }
  await b.close();
})();
