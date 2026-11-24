// A netavark host whose host side was undone (netavark-host.sh): fjordd,
// started, has the port's netavark-migrate set it up again, and the System
// page says what was done, one line each, and the pf check passes.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(30000);
  try {
    let notices = [];
    for (let i = 0; i < 40; i++) {
      notices = (await (await page.request.get(BASE + '/api/host-notices')).json().catch(() => [])) || [];
      if (notices.some((n) => n.id === 'netavark')) break;
      await sleep(3000);
    }
    const lines = (notices.find((n) => n.id === 'netavark')?.lines || []).join(' | ');
    note('fjord says it set pf up', /Added netavark's anchors to \/etc\/pf\.conf and loaded them/.test(lines), lines || 'no notice');
    note('and filter_local', /net\.pf\.filter_local/.test(lines), 'checked');
    note('and pf at boot', /pf now loads at boot/.test(lines), 'checked');

    await openPage(page, BASE + '/?t=' + Date.now() + '#/system');
    await sleep(3000);
    const text = (await page.locator('main').innerText().catch(() => '')).replace(/\s+/g, ' ');
    await page.screenshot({ path: '/out/netavark-host.png', fullPage: true });
    note('the System page shows it', /Added netavark's anchors/.test(text) && /filter_local/.test(text), text.slice(0, 160));
    const pf = await (await page.request.get(BASE + '/api/conditions')).json().catch(() => ({}));
    const bad = (pf.conditions || []).filter((c) => c.id === 'pf' || c.id === 'pf-local');
    note('no pf problem left', bad.length === 0, bad.map((c) => c.id + ': ' + (c.detail || '')).join('; ') || 'none');
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
  } finally {
    await b.close();
  }
})();
