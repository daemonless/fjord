// podman's DNS server moved off a taken port 53 from the Setup page, on a
// host where containers already use it: the step is fjord's own, its
// commands say which containers restart, Set up does it, and it is gone from
// the page after. STEP=broken reads the first state and clicks Set up,
// STEP=fixed reads the page again.
// needs: freebsd appjail
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const STEP = process.env.STEP || 'broken';
const NAME = 'container name resolution can start (port 53)';
const ACTION = "Move podman's DNS server off port 53";
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(30000);
  try {
    await openPage(page, BASE + '/?t=' + Date.now() + '#/setup');
    await sleep(2500);
    const body = await page.locator('body').innerText();
    const yourTurn = /Your turn/.test(body) ? body.split('Your turn')[1].split(/Recheck|Check again/)[0] : '';
    await page.screenshot({ path: `/out/dns-port-${STEP}.png`, fullPage: true });
    if (STEP === 'broken') {
      note('with port 53 taken: fjord moves podman\'s DNS server', body.includes(ACTION), body.includes(ACTION) ? `"${ACTION}" listed` : 'not among what fjord sets up');
      note('not left to a terminal', !yourTurn.includes(NAME), yourTurn.includes(NAME) ? '"Your turn" lists it' : 'not in "Your turn"');
      await page.getByRole('button', { name: 'Show commands' }).click();
      const shown = await page.locator('body').innerText();
      const restart = (shown.match(/podman restart [^\n]*/) || ['no restart line'])[0];
      note('its commands set the port', /dns_bind_port = \d+/.test(shown), (shown.match(/dns_bind_port = \d+/) || ['no port'])[0]);
      note('they say which containers restart', /t-dn1/.test(restart) && /t-dn2/.test(restart), restart);
      note('and leave the others alone', !/t-dq1/.test(restart), /t-dq1/.test(restart) ? 't-dq1 would be restarted' : 't-dq1 is not in the list');
      await page.getByRole('button', { name: 'Set up', exact: true }).click();
      for (let i = 0; i < 180 && /Setting up/.test(await page.locator('body').innerText()); i++) await sleep(1000);
      await sleep(2000);
      const after = await page.locator('body').innerText();
      await page.screenshot({ path: '/out/dns-port-setup.png', fullPage: true });
      note('Set up finished', !/Setup stopped/.test(after), /Setup stopped/.test(after) ? 'a step failed' : 'no step failed');
    } else {
      note('after Set up: nothing left to do for it', !body.includes(ACTION) && !yourTurn.includes(NAME), body.includes(ACTION) || yourTurn.includes(NAME) ? 'still listed' : 'gone from the page');
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/dns-port-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
