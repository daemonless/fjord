// A stack's restart policy, set through the restart chip in its page's
// header. STEP=install: install t-restart on podman and t-restart-aj on
// AppJail; a new podman install restarts unless stopped, an AppJail one
// starts at boot and offers only the boot choices. STEP=set POLICY=<value>:
// pick it, Apply Changes, check the page and the compose agree.
// needs: freebsd appjail
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page, name) => (await page.request.get(BASE + '/api/stacks/' + name)).json();
const chip = (page) => page.locator('[data-restart-policy] > button');
const policy = (page) => chip(page).getAttribute('data-policy');
const LABEL = { 'unless-stopped': 'Restart unless stopped', 'on-failure': 'Restart after a crash', no: "Don't restart" };
async function settle(page, name) {
  let d;
  // Pending is cleared a moment after busy ends (both at the stay-up check).
  for (let i = 0; i < 80; i++) { d = await detail(page, name).catch(() => ({})); if (!d?.busy && !(d?.state?.pending_services || []).length && (d?.status?.state === 'running' || d?.state?.last_failure)) break; await sleep(3000); }
  return d;
}
async function install(page, name, engine, port) {
  await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
  await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
  await sleep(800);
  await page.getByRole('button', { name: /^Install/ }).first().click();
  const install = page.getByRole('button', { name: 'Install', exact: true });
  await install.waitFor();
  await page.getByLabel('Name', { exact: true }).fill(name);
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
  await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/' + name);
  await sleep(1500);
}
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(30000);
  try {
    if (process.env.STEP === 'install') {
      await install(page, 't-restart', 'podman', '3125');
      const v = await policy(page);
      await page.screenshot({ path: '/out/restart-podman-new.png', fullPage: true });
      note('a new podman install restarts unless stopped', v === 'unless-stopped', `the chip shows "${v}" (${await chip(page).innerText()})`);
      await install(page, 't-restart-aj', 'appjail', '3126');
      // AppJail offers the boot half only: worded as that, with no restart
      // promised and no on-failure.
      const shown = await chip(page).innerText();
      note('AppJail: a new install starts at boot', /Start at boot/.test(shown) && (await policy(page)) === 'unless-stopped', `chip "${shown}"`);
      await chip(page).click();
      const items = await page.getByRole('menuitemradio').allInnerTexts();
      await page.screenshot({ path: '/out/restart-appjail-menu.png', fullPage: true });
      const words = items.map((t) => t.split('\n')[0]);
      note('AppJail: the menu offers the boot choices only', JSON.stringify(words) === JSON.stringify(['Start at boot', 'Always start at boot', "Don't start at boot"]) && (await page.getByText('does not restart an app when it stops').count()) > 0,
        JSON.stringify(words));
      await page.getByRole('menuitemradio', { name: "Don't start at boot" }).click();
      await sleep(1500);
      const aj = (await detail(page, 't-restart-aj')).restart || {};
      note("AppJail: Don't start at boot is saved, nothing to apply", aj.policy === 'no' && !aj.onExit && !(await page.getByRole('button', { name: 'Apply Changes', exact: true }).isVisible()), JSON.stringify(aj));
      await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/t-restart-aj');
      await sleep(1500);
      note('AppJail: the chip shows it after a reload', (await policy(page)) === 'no', await chip(page).innerText());
    } else {
      const want = process.env.POLICY;
      await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/t-restart');
      await sleep(1500);
      // A running stack takes the change through Apply Changes; one left down
      // (after "Don't restart" and a crash) takes it at the next Start.
      const down = (await detail(page, 't-restart')).status?.state !== 'running';
      await chip(page).click();
      await page.getByRole('menuitemradio', { name: LABEL[want] }).click();
      const apply = page.getByRole('button', { name: 'Apply Changes', exact: true });
      if (!down) await apply.waitFor();
      else await sleep(2000);
      if (want === 'on-failure') {
        const n = page.getByLabel('Restarts after a crash');
        note('on-failure: the count is offered, 5 by default', (await n.inputValue()) === '5', `count ${await n.inputValue()}`);
      }
      await page.screenshot({ path: `/out/restart-${want}-saved.png`, fullPage: true });
      await page.keyboard.press('Escape');
      if (down) await page.getByRole('button', { name: 'Start', exact: true }).click();
      else await apply.click();
      await sleep(2000);
      const d = await settle(page, 't-restart');
      const r = d.restart || {};
      const expect = want === 'on-failure' ? 'on-failure:5' : want;
      note(`${want}: saved and applied`, r.policy === want && !r.mixed && d.status?.state === 'running' && !(d.state?.pending_services || []).length,
        `policy ${r.policy}, compose ${JSON.stringify(r.services)}, state ${d.status?.state}, pending ${(d.state?.pending_services || []).join(',') || 'none'}`);
      note(`${want}: the compose says ${expect}`, Object.values(r.services || {}).every((s) => s === expect), JSON.stringify(r.services));
      await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/t-restart');
      await sleep(1500);
      const shown = await policy(page);
      note(`${want}: the chip shows it after a reload`, shown === want, `${shown} (${await chip(page).innerText()})`);
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/restart-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
