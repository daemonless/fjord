// Jail name resolution, set up from the Setup page: with dnsmasq stopped it
// is one of the things fjord sets up itself; Set up does it, and it is gone
// from the page after. STEP=broken reads the first state and clicks Set up,
// STEP=fixed reads the page again.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const STEP = process.env.STEP || 'broken';
const NAME = 'jail name resolution (appjail-dns + dnsmasq)';
const ACTION = 'Set up jail name resolution';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(30000);
  try {
    await page.goto(BASE + '/?t=' + Date.now() + '#/setup', { waitUntil: 'networkidle' });
    await sleep(2500);
    const body = await page.locator('body').innerText();
    const yourTurn = /Your turn/.test(body) ? body.split('Your turn')[1].split(/Recheck|Check again/)[0] : '';
    await page.screenshot({ path: `/out/appjail-dns-${STEP}.png`, fullPage: true });
    if (STEP === 'broken') {
      note('with dnsmasq stopped: fjord sets it up', body.includes(ACTION), body.includes(ACTION) ? `"${ACTION}" listed` : 'not among what fjord sets up');
      note('not left to a terminal', !yourTurn.includes(NAME), yourTurn.includes(NAME) ? '"Your turn" lists it' : 'not in "Your turn"');
      await page.getByRole('button', { name: 'Show commands' }).click();
      const shown = await page.locator('body').innerText();
      note('its commands are shown', /sysrc dnsmasq_conf=\/usr\/local\/etc\/fjord\/dnsmasq-appjail\.conf/.test(shown), 'dnsmasq_conf points at fjord\'s config');
      await page.getByRole('button', { name: 'Set up', exact: true }).click();
      for (let i = 0; i < 120 && /Setting up/.test(await page.locator('body').innerText()); i++) await sleep(1000);
      await sleep(2000);
      const after = await page.locator('body').innerText();
      await page.screenshot({ path: '/out/appjail-dns-setup.png', fullPage: true });
      note('Set up finished', !/Setup stopped/.test(after), /Setup stopped/.test(after) ? 'a step failed' : 'no step failed');
    } else {
      note('after Set up: nothing left to do for it', !body.includes(ACTION) && !yourTurn.includes(NAME), body.includes(ACTION) || yourTurn.includes(NAME) ? 'still listed' : 'gone from the page');
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/appjail-dns-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
