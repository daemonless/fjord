// A network made or removed while a stack's page is open shows up in that
// page's network picker without a reload.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-netev';
const NET = 't-netev-lan';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const api = async (page, path, opts) => {
  const r = await page.request.fetch(BASE + path, { ...opts, headers: { Origin: BASE, 'Content-Type': 'application/json', ...(opts?.headers || {}) } });
  const t = await r.text();
  try { return { status: r.status(), body: JSON.parse(t) }; } catch { return { status: r.status(), body: t }; }
};
const settle = async (page, want) => {
  let d;
  for (let i = 0; i < 60; i++) {
    d = (await api(page, '/api/stacks/' + NAME)).body;
    if (d?.name && !d.busy && (!want || d.status?.state === want)) return d;
    await sleep(3000);
  }
  return d;
};
// Waits up to 15 s for the picker on the first interface row to offer NET (or not).
const offered = async (page, want) => {
  const opt = page.locator('table select').first().locator(`option[value="${NET}"]`);
  for (let i = 0; i < 30; i++) {
    if (((await opt.count()) > 0) === want) return true;
    await sleep(500);
  }
  return false;
};
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(30000);
  try {
    await api(page, '/api/stacks/' + NAME + '/save', { method: 'POST', data: JSON.stringify({ compose: 'services:\n  web:\n    image: ghcr.io/daemonless/openspeedtest:latest\n', env: '' }) });
    await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/' + NAME);
    await page.locator('table select').first().waitFor();
    note('the page is open, the network not made yet', await offered(page, false), 'not offered');

    // A LAN network needs a lab bridge; a host without one skips.
    const parents = (await api(page, '/api/networks/parents')).body;
    if (!JSON.stringify(parents || '').includes('lanbridge2')) {
      console.log('SKIP t-netev: no lanbridge2 on this host to make a network on');
      await b.close();
      return;
    }
    const made = await api(page, '/api/networks', { method: 'POST', data: JSON.stringify({ name: NET, kind: 'lan', parent: 'lanbridge2', addressSource: 'dhcp' }) });
    note('the network is made through the API', made.status === 200, `HTTP ${made.status} ${typeof made.body === 'string' ? made.body.trim() : ''}`);
    note('the open page offers it without a reload', await offered(page, true), 'offered');
    await page.screenshot({ path: '/out/net-event-offered.png', fullPage: true });

    const gone = await api(page, '/api/networks/' + NET, { method: 'DELETE' });
    note('the network is removed through the API', gone.status === 200, `HTTP ${gone.status}`);
    note('the open page stops offering it without a reload', await offered(page, false), 'not offered');
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
  }
  await b.close();
})();
