// The two host checks that were a terminal job -- pf's anchors for podman and
// ifconfig_DEFAULT running DHCP on every interface -- are set up from the
// Setup page like the rest. STEP=broken: both are among what fjord sets up,
// neither is in "Your turn", Set up runs them. STEP=partial: with the NAT
// line gone from a running pf, only the pf step is back, and Set up reloads
// the rules. STEP=fixed: nothing left, the host is ready.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const STEP = process.env.STEP || 'broken';
const PF = "Add podman's rules to /etc/pf.conf and load them";
const DHCP = "Name the host's own NIC in rc.conf instead of every interface";
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
    const yourTurn = /Your turn/.test(body) ? body.split('Your turn')[1].split(/Check again/)[0] : '';
    await page.screenshot({ path: `/out/host-fixes-${STEP}.png`, fullPage: true });
    if (STEP === 'fixed') {
      note('after Set up: nothing left for a terminal', !yourTurn, yourTurn ? '"Your turn" is still there' : 'no "Your turn"');
      note('both gone from the plan', !body.includes(PF) && !body.includes(DHCP), [body.includes(PF) && 'pf', body.includes(DHCP) && 'rc.conf'].filter(Boolean).join(', ') || 'neither listed');
      note('ready', /Ready to run apps/.test(body), /Ready to run apps/.test(body) ? 'Ready to run apps' : 'not ready');
    } else {
      note('pf is among what fjord sets up', body.includes(PF), body.includes(PF) ? `"${PF}" listed` : 'not listed');
      if (STEP === 'broken') note('rc.conf is among what fjord sets up', body.includes(DHCP), body.includes(DHCP) ? `"${DHCP}" listed` : 'not listed');
      else note('rc.conf, already right, is not', !body.includes(DHCP), body.includes(DHCP) ? 'listed again' : 'not listed');
      note('neither is left to a terminal', !yourTurn.includes('pf firewall') && !yourTurn.includes('DHCP'), yourTurn ? `"Your turn" says: ${yourTurn.trim().split('\n')[0]}` : 'no "Your turn"');
      await page.getByRole('button', { name: 'Show commands' }).click();
      const shown = await page.locator('body').innerText();
      note('the pf commands are shown', /rdr-anchor "cni-rdr\/\*"/.test(shown) && /pfctl -nf \/etc\/pf\.conf/.test(shown), 'the anchor line and the validation');
      if (STEP === 'broken') note('the rc.conf commands name the NIC', /sysrc ifconfig_[a-z]+[0-9]+="DHCP"/.test(shown) && /sysrc -x ifconfig_DEFAULT/.test(shown), (shown.match(/sysrc ifconfig_[a-z]+[0-9]+="DHCP"/) || ['no sysrc line'])[0]);
      await page.getByRole('button', { name: 'Set up', exact: true }).click();
      for (let i = 0; i < 120 && /Setting up/.test(await page.locator('body').innerText()); i++) await sleep(1000);
      await sleep(2000);
      const after = await page.locator('body').innerText();
      await page.screenshot({ path: `/out/host-fixes-${STEP}-setup.png`, fullPage: true });
      note('Set up finished', !/Setup stopped/.test(after), /Setup stopped/.test(after) ? 'a step failed' : 'no step failed');
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/host-fixes-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
