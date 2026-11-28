// Clicking another stack in the sidebar keeps the tab you were on: History
// stays History, Settings stays Settings, and nothing of the first stack is
// left on the page. The two stacks (t-kt-a, t-kt-b) are saved, never started.
const { chromium } = require('playwright-core');
const { BASE, sleep, note, signedIn, openPage } = require('./session');
const H = { Origin: BASE, 'Content-Type': 'application/json' };
const compose = (port) => `services:\n  web:\n    image: ghcr.io/daemonless/openspeedtest:latest\n    ports:\n      - "${port}:3000"\n`;
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }))).newPage();
  page.setDefaultTimeout(20000);
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const hash = () => page.url().split('#')[1] || '';
  const heading = async () => (await page.locator('header h2').innerText().catch(() => '')).trim();
  const click = async (name) => { await page.locator('aside').getByText(name, { exact: true }).first().click(); await sleep(1500); };
  try {
    for (const [n, p] of [['t-kt-a', 3131], ['t-kt-b', 3132]]) {
      const r = await page.request.post(BASE + `/api/stacks/${n}/save`, { headers: H, data: { compose: compose(p), env: '' } });
      if (!r.ok()) throw new Error(`saving ${n}: ${r.status()}`);
    }
    for (const tab of ['history', 'settings', 'compose']) {
      await openPage(page, BASE + '/?t=' + Date.now() + `#/stacks/t-kt-a/${tab}`);
      await sleep(1500);
      await click('t-kt-b');
      note(`${tab}: stays on ${tab} for the next stack`, hash() === `/stacks/t-kt-b/${tab}` && (await heading()) === 't-kt-b', `#${hash()}, heading "${await heading()}"`);
    }
    // The compose shown is the new stack's, not the one left behind.
    const shown = await page.locator('.cm-content').innerText().catch(() => '');
    note('compose: the editor shows the new stack', /3132:3000/.test(shown) && !/3131:3000/.test(shown), shown.replace(/\s+/g, ' ').slice(0, 120));
    await page.goBack(); await sleep(1000);
    note('Back returns to the first stack, same tab', hash() === '/stacks/t-kt-a/compose' && (await heading()) === 't-kt-a', `#${hash()}, heading "${await heading()}"`);
    // From a page that is not a stack, a click opens Services as before.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
    await click('t-kt-a');
    note('from the store: opens the stack on Services', hash() === '/stacks/t-kt-a', '#' + hash());
    note('no exception on the page', !errors.length, errors[0] || 'none');
    await page.screenshot({ path: '/out/keep-tab.png', fullPage: true });
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/keep-tab-error.png', fullPage: true }).catch(() => {});
  }
  for (const n of ['t-kt-a', 't-kt-b']) await page.request.delete(BASE + `/api/stacks/${n}?data=1`, { headers: H, timeout: 60000 }).catch(() => {});
  await b.close();
})();
