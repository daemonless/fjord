// A Stacks row says the restart policy only when it is not what a new
// install takes, and that default is the one set in Settings for the
// engine: change it there and the rows follow. The stack is t-rrow, saved
// and never started.
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const H = { Origin: BASE, 'Content-Type': 'application/json' };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const compose = (restart) => `services:\n  web:\n    image: ghcr.io/daemonless/openspeedtest:latest\n    restart: "${restart}"\n`;
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 900 } }))).newPage();
  page.setDefaultTimeout(30000);
  const save = (restart) => page.request.post(BASE + '/api/stacks/t-rrow/save', { headers: H, data: { compose: compose(restart), env: '' } });
  const rowText = async () => {
    await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks');
    await page.getByRole('button', { name: /^List$/ }).click().catch(() => {});
    await sleep(1500);
    return (await page.locator('div.cursor-pointer', { hasText: 't-rrow' }).first().innerText().catch(() => '(no row)')).replace(/\s+/g, ' ');
  };
  // The default new installs take on podman, set on the Settings page.
  const setDefault = async (value) => {
    await openPage(page, BASE + '/?t=' + Date.now() + '#/settings');
    const sel = page.locator('#default-restart-podman');
    if (!(await sel.isVisible().catch(() => false))) await page.getByText('Advanced', { exact: true }).first().click();
    await sel.selectOption(value);
    await sleep(1500);
    return (await (await page.request.get(BASE + '/api/settings/restart')).json()).podman?.policy;
  };
  try {
    if (!(await save('unless-stopped')).ok()) throw new Error('saving t-rrow');
    let row = await rowText();
    note('the default policy is not on the row', /t-rrow/.test(row) && !/restart/i.test(row), row);
    if (!(await save('no')).ok()) throw new Error('saving t-rrow with no');
    row = await rowText();
    await page.screenshot({ path: '/out/restart-row-shown.png' });
    note('another policy is', /Don't restart/.test(row), row);
    const set = await setDefault('no');
    row = await rowText();
    await page.screenshot({ path: '/out/restart-row-default-changed.png' });
    note('with that policy made the default in Settings, the row drops it', set === 'no' && /t-rrow/.test(row) && !/restart/i.test(row), `default ${set}; ${row}`);
    const back = await setDefault('unless-stopped');
    row = await rowText();
    note('and shows it again with the default put back', back === 'unless-stopped' && /Don't restart/.test(row), `default ${back}; ${row}`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/restart-row-error.png' }).catch(() => {});
  }
  await page.request.post(BASE + '/api/settings/restart', { headers: H, data: { engine: 'podman', policy: 'unless-stopped' } }).catch(() => {});
  await page.request.delete(BASE + '/api/stacks/t-rrow?data=1', { headers: H, timeout: 120000 }).catch(() => {});
  await b.close();
})();
