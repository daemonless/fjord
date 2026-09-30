// The appjail-dns check tells the truth: with dnsmasq stopped it is a thing
// to run in a terminal, with the commands on screen; once they are run it is
// gone from that list. STEP=broken reads the first state, STEP=fixed the
// second (the companion runs the commands in between).
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const STEP = process.env.STEP || 'broken';
const NAME = 'jail name resolution (appjail-dns + dnsmasq)';
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
    const yourTurn = /Your turn/.test(body) ? body.split('Your turn')[1].split(/Recheck/)[0] : '';
    await page.screenshot({ path: `/out/appjail-dns-${STEP}.png`, fullPage: true });
    if (STEP === 'broken') {
      note('with dnsmasq stopped: listed as a thing to run in a terminal', yourTurn.includes(NAME), yourTurn ? `"Your turn" lists: ${yourTurn.replace(/\s+/g, ' ').trim().slice(0, 160)}` : 'no "Your turn" card on the page');
      const cmds = ['pkg install -y dnsmasq', 'sysrc dnsmasq_enable=YES', 'sysrc dnsmasq_conf=/usr/local/share/appjail/files/dnsmasq.conf', 'service dnsmasq start', 'sysrc appjail_dns_enable=YES', 'service appjail-dns restart'];
      const missing = cmds.filter((c) => !body.includes(c));
      note('the six commands are on screen', missing.length === 0, missing.length ? `missing: ${missing.join(' | ')}` : 'all six');
      note('not offered as an Install button', !/Start appjail-dns/.test(body), /Start appjail-dns/.test(body) ? 'an Install button still says "Start appjail-dns"' : 'no button for it');
    } else {
      note('after the commands: no longer on the list', !yourTurn.includes(NAME), yourTurn.includes(NAME) ? 'still listed' : (yourTurn ? 'other things remain, not this' : 'no "Your turn" card at all'));
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/appjail-dns-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
