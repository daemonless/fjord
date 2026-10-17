// Start on boot follows each stack's restart policy, on both engines, across
// a real host reboot (reboot.sh does the reboot; opt-in). STEP=setup: one
// stack per rule, set through the page's chip and Stop. STEP=check: which
// came back.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page, name) => (await page.request.get(BASE + '/api/stacks/' + name)).json();
const chip = (page) => page.locator('[data-restart-policy] > button');
const policy = (page) => chip(page).getAttribute('data-policy');
const LABEL = { 'unless-stopped': 'Restart unless stopped', 'on-failure': 'Restart after a crash', no: "Don't restart" };
async function settle(page, name) {
  let d;
  for (let i = 0; i < 80; i++) { d = await detail(page, name).catch(() => ({})); if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break; await sleep(3000); }
  return d;
}
async function install(page, name, engine, port) {
  await page.goto(BASE + '/?t=' + Date.now() + '#/store', { waitUntil: 'networkidle' });
  await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
  await sleep(800);
  await page.getByRole('button', { name: /^Install/ }).first().click();
  const install = page.getByRole('button', { name: 'Install', exact: true });
  await install.waitFor();
  await page.getByLabel('Name').fill(name);
  if (!(await page.locator('#WEB_PORT').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
  await page.locator('#WEB_PORT').fill(port);
  if (engine === 'appjail') {
    const sel = page.locator('select').filter({ hasText: 'appjail' }).first();
    if (!(await sel.isVisible().catch(() => false))) await page.getByText('Advanced', { exact: true }).click();
    await sel.selectOption('appjail');
    await sleep(1500);
  }
  await install.click();
  const d = await settle(page, name);
  note(`${engine}: ${name} installed`, d?.status?.state === 'running', `state ${d?.status?.state}`);
  await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + name, { waitUntil: 'networkidle' });
  await sleep(1500);
}
// [name, engine, port, chip choice (its words), stopped by the user, up after the reboot]
const CASES = [
  ['t-rb-keep', 'podman', '3131', '', false, true],
  ['t-rb-stopped', 'podman', '3132', '', true, false],
  ['t-rb-always', 'podman', '3133', 'Always restart', true, true],
  ['t-rb-never', 'podman', '3134', "Don't restart", false, false],
  ['t-rb-aj-always', 'appjail', '3135', 'Always start at boot', true, true],
  ['t-rb-aj-never', 'appjail', '3136', "Don't start at boot", false, false],
];
async function stopped(page, name) {
  for (let i = 0; i < 40; i++) {
    const d = await detail(page, name).catch(() => ({}));
    if (!d?.busy && d?.status?.state === 'stopped') return true;
    await sleep(3000);
  }
  return false;
}
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(30000);
  try {
    if (process.env.STEP === 'setup') {
      for (const [name, engine, port, choice, stop] of CASES) {
        await install(page, name, engine, port);
        // A podman install watches that it stays up for a minute; the page
        // re-renders the whole time, so wait for it before using the chip.
        if (engine === 'podman') await page.getByText(/has stayed up/).waitFor({ timeout: 150000 }).catch(() => {});
        if (choice) {
          await chip(page).click();
          await page.getByRole('menuitemradio', { name: choice }).click();
          await sleep(1500);
          // A compose change is taken at Apply; a boot-only one needs none.
          const apply = page.getByRole('button', { name: 'Apply Changes', exact: true });
          if (engine === 'podman') {
            await apply.click();
            await sleep(2000);
            await settle(page, name);
          }
        }
        if (stop) {
          await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + name, { waitUntil: 'networkidle' });
          await sleep(1500);
          await page.getByRole('button', { name: 'Stop…' }).click();
          await page.getByRole('button', { name: 'Stop', exact: true }).click();
        }
        const d = await detail(page, name);
        const ok = stop ? await stopped(page, name) : d.status?.state === 'running';
        note(`setup ${name}: ${engine}, ${choice || 'the default'}${stop ? ', stopped' : ''}`, ok, `policy ${d.restart?.policy}, state ${(await detail(page, name)).status?.state}`);
      }
    } else {
      for (const [name, engine, , choice, stop, up] of CASES) {
        const d = await settle(page, name).catch(() => ({}));
        const state = d.status?.state;
        const desc = `${engine}, ${choice || 'the default'}${stop ? ', stopped before' : ''}`;
        note(`${name} (${desc}): ${up ? 'up' : 'down'} after the reboot`, up ? state === 'running' : state !== 'running', `state ${state}, desired ${d.state?.desired_state}`);
      }
      await page.goto(BASE + '/?t=' + Date.now() + '#/stacks', { waitUntil: 'networkidle' });
      await sleep(2000);
      await page.screenshot({ path: '/out/reboot-stacks.png', fullPage: true });
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/reboot-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
