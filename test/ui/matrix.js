// fjord UI test matrix: everything through the real pages, checked by outcome.
// Run in ghcr.io/daemonless/playwright on the fjord host (--network host).
const { chromium } = require('playwright-core');
const BASE = process.env.FJORD || 'http://127.0.0.1:3567';
const BRIDGE = 'lanbridge2'; // has a host address: the subnet fills in
const BARE = 'lanbridge'; // no host address
const POOL = { start: '192.168.4.220', end: '192.168.4.225' };
const STATIC_PODMAN = '192.168.4.228';
const POOL_APPJAIL = '192.168.4.226';

const results = [];
const note = (id, ok, msg) => { results.push({ id, ok, msg }); console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`); };
const api = async (page, path, opts) => {
  const r = await page.request.fetch(BASE + path, { ...opts, headers: { Origin: BASE, 'Content-Type': 'application/json', ...(opts?.headers || {}) } });
  const t = await r.text();
  try { return { status: r.status(), body: JSON.parse(t) }; } catch { return { status: r.status(), body: t }; }
};
const shot = (page, name) => page.screenshot({ path: `/out/${name}.png`, fullPage: true }).catch(() => {});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function stackUp(page, name, secs = 240) {
  for (let i = 0; i < secs / 3; i++) {
    const r = await api(page, `/api/stacks/${name}`);
    const st = r.body?.status || {};
    if (st.state === 'running') return r.body;
    await sleep(3000);
  }
  return (await api(page, `/api/stacks/${name}`)).body;
}
// "running" comes before the app listens and before a DHCP lease lands, so
// both get a minute.
async function answers(page, url) {
  let code = 0;
  for (let i = 0; i < 12 && code !== 200; i++) {
    try { code = (await page.request.get(url, { timeout: 5000 })).status(); } catch { code = 0; await sleep(5000); }
  }
  return code;
}
async function addressFor(page, name, d) {
  for (let i = 0; i < 12 && !addrOf(d); i++) { await sleep(5000); d = (await api(page, `/api/stacks/${name}`)).body; }
  return addrOf(d);
}
const addrOf = (d) => (d?.services || []).map((s) => s.address).find(Boolean) || '';

// ---------- Networks page ----------
async function createNetwork(page, { name, bridge, source, range }) {
  await page.goto(BASE + '/#/networks', { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: /New network/i }).first().click();
  // 0 = click as fast as the page lets you, which is what caught the
  // refresh-reset race; >0 only to get past it on a build without the fix.
  await sleep(+(process.env.SETTLE_MS || 0));
  await page.getByRole('button', { name: 'epair', exact: true }).click();
  await page.getByRole('button', { name: 'both', exact: true }).click();
  await page.getByPlaceholder(/vlan4, lan/).fill(name);
  await page.locator('select').filter({ hasText: 'Select a bridge' }).selectOption(bridge);
  await page.getByRole('button', { name: source, exact: true }).click();
  await sleep(500);
  if (range) {
    if (!(await page.locator('#n-rs').isVisible())) await page.getByRole('button', { name: 'Advanced', exact: true }).click();
    await page.locator('#n-rs').fill(range.start, { timeout: 5000 }).catch(async (e) => { await shot(page, 'range-form'); throw e; });
    await page.locator('#n-re').fill(range.end);
  }
  const create = page.getByRole('button', { name: 'Create', exact: true });
  const disabled = await create.isDisabled();
  const reason = disabled ? (await page.locator('.text-fjord-warning').allInnerTexts()).join(' ').trim() : '';
  return { create, disabled, reason };
}

// ---------- App Store wizard ----------
async function openWizard(page, app, name) {
  // Through the sidebar, as a user gets there: a hash-only goto from a stack
  // page left the stack page up.
  await page.goto(BASE + '/', { waitUntil: 'networkidle' });
  await page.getByText('App Store', { exact: true }).first().click();
  await page.getByPlaceholder(/Search apps/).fill(app);
  await page.getByRole('button', { name: /^Install/ }).first().click();
  await page.getByRole('button', { name: 'Install', exact: true }).waitFor();
  const nameBox = page.locator('input').filter({ has: page.locator('xpath=.') }).nth(0);
  await page.getByLabel('Name').fill(name).catch(async () => { await nameBox.fill(name); });
}
// Options may start folded (the wizard remembers); the network editor and
// Advanced live inside it.
async function openOptions(page) {
  const picker = page.locator('table select');
  if (await picker.count() && await picker.first().isVisible()) return;
  await page.getByRole('button', { name: /^Options/ }).first().click();
  await sleep(500);
}
async function wizardEngine(page, engine) {
  // Advanced is a <details>; open it unless the wizard remembered it open.
  const det = page.locator('details').filter({ has: page.locator('summary', { hasText: 'Advanced' }) }).first();
  if (!(await det.evaluate((d) => d.open))) await det.locator('summary').click();
  await page.locator('select').filter({ hasText: 'appjail' }).selectOption({ label: engine === 'appjail' ? 'appjail' : /podman/ }).catch(async () => {
    await page.locator('select').filter({ hasText: 'appjail' }).selectOption(engine);
  });
  await sleep(1500);
}
async function wizardNetwork(page, network) {
  const sel = page.locator('table select');
  await sel.first().selectOption(network);
  await sleep(500);
}

async function install(page, c) {
  await openWizard(page, 'openspeedtest', c.name);
  await openOptions(page);
  if (c.port) await page.locator('#WEB_PORT').fill(c.port);
  if (c.engine === 'appjail') await wizardEngine(page, 'appjail');
  if (c.network) await wizardNetwork(page, c.network);
  // The host's default for new installs may be a LAN network; t-br is about
  // the built-in bridge, so it says so.
  else await wizardNetwork(page, 'bridge');
  const btn = page.getByRole('button', { name: 'Install', exact: true });
  if (c.needsAddress) {
    const blocked = await btn.isDisabled();
    const why = (await page.locator('.text-fjord-warning').allInnerTexts()).join(' ');
    note(`${c.name}: Install waits for an address`, blocked && /address/i.test(why), blocked ? `disabled, reason on screen: "${why.trim() || 'NONE SHOWN'}"` : 'Install was NOT disabled');
    await page.getByPlaceholder('from the network').first().fill(c.needsAddress);
    await sleep(500);
  }
  if (await btn.isDisabled()) {
    const why = (await page.locator('.text-fjord-warning').allInnerTexts()).join(' ');
    await shot(page, `fail-${c.name}`);
    note(`${c.name}: install`, false, `Install disabled; on screen: "${why.trim() || 'no reason shown'}"`);
    await page.getByRole('button', { name: 'Cancel' }).click().catch(() => {});
    return;
  }
  await btn.click();
  const d = await stackUp(page, c.name);
  const compose = d?.compose || '';
  const state = d?.status?.state;
  if (state !== 'running') { await shot(page, `fail-${c.name}`); note(`${c.name}: install`, false, `stack state ${state}`); return; }
  const addr = c.network ? await addressFor(page, c.name, d) : '';
  let ok = true; const bits = [];
  if (c.network) { const on = compose.includes(c.network); ok &&= on; bits.push(on ? `on ${c.network}` : `NOT on ${c.network} (compose)`); }
  if (c.expectAddr) { const good = c.expectAddr(addr); ok &&= good; bits.push(`address ${addr || 'none'}${good ? '' : ' (unexpected)'}`); }
  const url = c.network ? (addr ? `http://${addr}:3000/` : '') : `http://192.168.4.103:${c.port}/`;
  if (!c.network) {
    // Published ports can't be reached from this host (pf skips its own
    // traffic); run.sh curls them from another one.
    const port = (d?.env || '').match(/WEB_PORT=(\S*)/)?.[1];
    note(`${c.name}: install`, port === c.port, `running, WEB_PORT=${port}; reachability checked by run.sh from off-host`);
    return;
  }
  const code = url ? await answers(page, url) : 0;
  ok &&= code === 200; bits.push(`${url || 'no url'} -> ${code}`);
  note(`${c.name}: install`, ok, bits.join(', '));
}

(async () => {
  const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-setuid-sandbox'] });
  const page = await (await browser.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(20000);
  const made = { stacks: [], nets: [] };
  try {
    // --- networks
    for (const n of [
      { name: 't-dhcp', bridge: BRIDGE, source: 'DHCP' },
      { name: 't-pool', bridge: BRIDGE, source: 'Range', range: POOL },
      { name: 't-static', bridge: BRIDGE, source: 'Static' },
    ]) {
      const f = await createNetwork(page, n);
      if (f.disabled) { await shot(page, `fail-net-${n.name}`); note(`network ${n.name}`, false, `Create disabled: "${f.reason || 'no reason shown'}"`); continue; }
      await f.create.click(); await sleep(2000);
      const list = (await api(page, '/api/networks?engine=podman')).body || [];
      const got = list.find((x) => x.name === n.name);
      const want = { DHCP: 'dhcp', Range: 'pool', Static: 'static' }[n.source];
      const ok = !!got && got.addressSource === want;
      note(`network ${n.name}`, ok, !got ? 'not in the network list after Create' : `created on ${got.bridge}, addressSource ${got.addressSource}${ok ? '' : ` (wanted ${want})`}`);
      if (!got) await shot(page, `fail-net-${n.name}`);
      if (got) made.nets.push(n.name);
    }
    // The bare bridge: does the subnet fill in from a network already on it?
    {
      const f0 = await createNetwork(page, { name: 't-bare', bridge: BARE, source: 'Range' });
      // The answer takes a second or two; the form fills in when it lands.
      await page.getByText(/from lanbridge's DHCP server/).waitFor({ timeout: 20000 }).catch(() => {});
      await sleep(300);
      const create = page.getByRole('button', { name: 'Create', exact: true });
      const f = { ...f0, disabled: await create.isDisabled(), reason: (await page.locator('.fixed .text-fjord-warning, [role="dialog"] .text-fjord-warning').allInnerTexts().catch(() => [])).join(' ') };
      // The host holds no address on lanbridge, so the form asks its DHCP
      // server, and the subnet comes from the answer.
      const got = await page.locator('#n-subnet').inputValue().catch(() => '');
      const ok = !f.disabled && got === '192.168.4.0/24';
      note('Range on a bridge with no host address: subnet from its DHCP server', ok, `subnet "${got}", Create ${f.disabled ? 'disabled: ' + f.reason : 'enabled'}`);
      await page.getByRole('button', { name: 'Cancel' }).click().catch(() => {});
    }
    // --- installs
    const inPool = (a) => { const n = +a.split('.').pop(); return a.startsWith('192.168.4.') && n >= 220 && n <= 225; };
    const cases = [
      { name: 't-br', engine: 'podman', port: '3101' },
      { name: 't-dh', engine: 'podman', network: 't-dhcp', expectAddr: (a) => a.startsWith('192.168.4.') },
      { name: 't-po', engine: 'podman', network: 't-pool', expectAddr: inPool },
      { name: 't-st', engine: 'podman', network: 't-static', needsAddress: STATIC_PODMAN, expectAddr: (a) => a === STATIC_PODMAN },
      { name: 't-jd', engine: 'appjail', network: 't-dhcp', expectAddr: (a) => a.startsWith('192.168.4.') },
      { name: 't-jp', engine: 'appjail', network: 't-pool', needsAddress: POOL_APPJAIL, expectAddr: (a) => a === POOL_APPJAIL },
    ];
    for (const c of cases) {
      if (c.network && !made.nets.includes(c.network)) { note(`${c.name}: install`, false, `skipped: network ${c.network} was not created`); continue; }
      made.stacks.push(c.name);
      try { await install(page, c); } catch (e) {
        await shot(page, `fail-${c.name}`);
        note(`${c.name}: install`, false, 'error: ' + e.message.split('\n')[0]);
        // A wizard left open blocks every later case.
        await page.getByRole('button', { name: 'Cancel', exact: true }).click({ timeout: 3000 }).catch(() => page.keyboard.press('Escape'));
      }
    }
    // --- Services tab (AppJail): same-bridge networks shown disabled
    if (made.nets.length >= 2) {
      try {
        await page.goto(BASE + '/#/stacks/t-jd', { waitUntil: 'networkidle' });
        await page.getByRole('button', { name: /^Services/ }).first().click().catch(() => {});
        await sleep(1500);
        const add = page.getByRole('button', { name: /\+ interface/ });
        if (await add.count()) await add.first().click();
        await sleep(800);
        // The new row is the one to check: it shares a service with t-dhcp.
        const opts = await page.locator('table select').last().locator('option').evaluateAll((os) => os.map((o) => `${o.textContent.trim()}${o.disabled ? ' [disabled]' : ''}`));
        const same = opts.filter((o) => /t-(pool|static)/.test(o));
        const ok = same.length > 0 && same.every((o) => o.includes('[disabled]') && /same bridge as/.test(o));
        await shot(page, 'services-t-jd');
        note('Services (AppJail): same-bridge networks disabled', ok, same.join(' | ') || 'no t-pool/t-static option found');
      } catch (e) { note('Services (AppJail): same-bridge networks disabled', false, 'error: ' + e.message.split('\n')[0]); }
    }
  } finally {
    // --- clean up everything this run made
    // KEEP=t-br leaves a stack up to check from another host: pf does not
    // redirect this host's traffic to its own address.
    const keep = (process.env.KEEP || '').split(',');
    for (const s of made.stacks.filter((x) => !keep.includes(x))) await api(page, `/api/stacks/${s}?data=1`, { method: 'DELETE' });
    for (const n of made.nets) await api(page, `/api/networks/${n}?engine=podman`, { method: 'DELETE' });
    const pass = results.filter((r) => r.ok).length;
    console.log(`\n==== ${pass}/${results.length} passed ====`);
    for (const r of results.filter((r) => !r.ok)) console.log(`FAIL ${r.id}: ${r.msg}`);
    await browser.close();
  }
})();
