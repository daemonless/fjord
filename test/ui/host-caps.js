// The install wizard asks before host capabilities only when the catalog asks
// for them: a library folder the user types is their choice, and a .NET app's
// allow.mlock is not a capability, and an app moved off the host network
// is not asked about network_mode: host. Nothing is installed.
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(30000);
  const cap = page.getByRole('checkbox', { name: /Allow host capabilities/ });
  const install = page.getByRole('button', { name: 'Install', exact: true });
  const wizard = async (app) => {
    await openPage(page, BASE + '/#/store');
    await page.getByPlaceholder(/Search apps/).fill(app);
    await sleep(1000);
    await page.getByRole('button', { name: /^Install/ }).first().click();
    await install.waitFor();
    await page.getByRole('button', { name: /^Options/ }).first().click().catch(() => {});
    await sleep(800);
  };
  const asked = async () => cap.isVisible().catch(() => false);
  try {
    await wizard('radarr');
    note('radarr defaults: no capability prompt', !(await asked()) && (await install.isEnabled()), `prompt=${await asked()}, Install enabled=${await install.isEnabled()}`);
    await page.screenshot({ path: '/out/host-caps-radarr.png', fullPage: true });
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    await sleep(500);

    // An app that ships on the host network is asked about it, until its
    // networking is moved onto a network: then it does not get network_mode: host.
    await openPage(page, BASE + '/#/store');
    await page.getByPlaceholder(/Search apps/).fill('home-assistant');
    await sleep(1000);
    if (await page.getByText('No Results Found').isVisible().catch(() => false)) {
      console.log('SKIP an app moved off the host network: this catalog has no home-assistant');
    } else {
      await wizard('home-assistant');
      // Its jail annotation is still asked about: only the network goes.
      const prompt = async () => ((await asked()) ? await cap.locator('xpath=..').innerText() : '');
      const before = await prompt();
      await page.locator('select[id^="type-"]').first().selectOption('networks');
      await sleep(800);
      const after = await prompt();
      note('home-assistant is asked about network_mode: host as it ships', before.includes('network_mode: host'), JSON.stringify(before));
      note('moved onto a network: network_mode: host is no longer asked', !after.includes('network_mode: host'), JSON.stringify(after));
      await page.screenshot({ path: '/out/host-caps-off-host.png', fullPage: true });
      await page.getByRole('button', { name: 'Cancel', exact: true }).click();
      await sleep(500);
    }

    // A catalog without the app (the Linux one) has nothing to ask about.
    await openPage(page, BASE + '/#/store');
    await page.getByPlaceholder(/Search apps/).fill('audiobookshelf');
    await sleep(1000);
    if (await page.getByText('No Results Found').isVisible().catch(() => false)) {
      console.log('SKIP a library folder outside every folder set: this catalog has no audiobookshelf');
      await b.close();
      return;
    }
    await wizard('audiobookshelf');
    const lib = page.locator('input[id*="AUDIOBOOKS"]').first();
    await lib.fill('/mnt/host-caps-test/audiobooks');
    await page.keyboard.press('Tab');
    await sleep(800);
    note('a library folder outside every folder set: no capability prompt', !(await asked()) && (await install.isEnabled()), `prompt=${await asked()}, Install enabled=${await install.isEnabled()}`);
    await page.screenshot({ path: '/out/host-caps-own-folder.png', fullPage: true });
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/host-caps-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
