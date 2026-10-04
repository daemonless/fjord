// Type (networks / host / none) in the Services editor, through fjord's pages:
// bridge next to a LAN network, host and back without losing rows, Save both
// ways, and AppJail's limits. run-typec.sh checks the published port off-host.
const { chromium } = require('playwright-core');
const BASE = process.env.FJORD || 'http://127.0.0.1:3567';
const SVC = 'openspeedtest';
const PORT = '3106';

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
  for (let i = 0; i < secs / 5; i++) {
    const r = await api(page, `/api/stacks/${name}`);
    if (r.body?.status?.state === 'running') return r.body;
    await sleep(5000);
  }
  return (await api(page, `/api/stacks/${name}`)).body;
}
async function answers(page, url, secs = 60) {
  for (let i = 0; i < secs / 5; i++) {
    try { if ((await page.request.get(url, { timeout: 5000 })).status() === 200) return 200; } catch {}
    await sleep(5000);
  }
  return 0;
}
const lanAddr = (d) => (d?.services || []).map((s) => s.live?.['t-lan']).find(Boolean)
  || (d?.status?.containers || []).map((c) => c.addresses?.['t-lan']).find(Boolean) || '';

const typeSel = (page) => page.locator(`#type-${SVC}`);
const rowSels = (page) => page.locator('table select');
const rowValues = (page) => rowSels(page).evaluateAll((ss) => ss.map((s) => s.value));

async function createLan(page) {
  await page.goto(BASE + '/#/networks', { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: /New network/i }).first().click();
  await page.getByRole('button', { name: 'epair', exact: true }).click();
  await page.getByRole('button', { name: 'both', exact: true }).click();
  await page.getByPlaceholder(/vlan4, lan/).fill('t-lan');
  await page.locator('select').filter({ hasText: 'Select a bridge' }).selectOption('lanbridge2');
  await page.getByRole('button', { name: 'DHCP', exact: true }).click();
  await sleep(500);
  await page.getByRole('button', { name: 'Create', exact: true }).click();
  await sleep(2000);
  const ok = ((await api(page, '/api/networks')).body || []).some((n) => n.name === 't-lan');
  note('network t-lan (DHCP, epair) via the Networks page', ok, ok ? 'created' : 'missing');
  return ok;
}

async function openWizard(page, name, engine) {
  await page.goto(BASE + '/', { waitUntil: 'networkidle' });
  await page.getByText('App Store', { exact: true }).first().click();
  await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
  await page.getByRole('button', { name: /^Install/ }).first().click();
  await page.getByRole('button', { name: 'Install', exact: true }).waitFor();
  await page.getByLabel('Name').fill(name);
  if (!(await typeSel(page).isVisible().catch(() => false))) {
    await page.getByRole('button', { name: /^Options/ }).first().click();
    await sleep(500);
  }
  if (engine === 'appjail') {
    const det = page.locator('details').filter({ has: page.locator('summary', { hasText: 'Advanced' }) }).first();
    if (!(await det.evaluate((d) => d.open))) await det.locator('summary').click();
    await page.locator('select').filter({ hasText: 'appjail' }).first().selectOption('appjail');
    await sleep(1500);
  }
}

// Wizard: bridge + t-lan on podman.
async function installMixed(page) {
  await openWizard(page, 't-tc', 'podman');
  await page.locator('#WEB_PORT').fill(PORT);
  const types = await typeSel(page).locator('option').allInnerTexts();
  note('wizard: Type offers networks, host, none', ['networks', 'host', 'none'].every((t) => types.some((x) => x.trim().startsWith(t))), types.map((t) => t.trim()).join(' | '));
  // The networks arrive after the dialog draws; wait for the epair group.
  await rowSels(page).first().locator('optgroup[label="epair"]').waitFor({ state: 'attached' }).catch(() => {});
  const groups = await rowSels(page).first().locator('optgroup').evaluateAll((gs) => gs.map((g) => g.label));
  note('wizard: networks grouped by type', groups.includes('bridge') && groups.includes('epair'), groups.join(', '));
  await rowSels(page).first().selectOption('t-lan');
  await page.getByRole('button', { name: '+ interface' }).first().click();
  await sleep(400);
  await rowSels(page).nth(1).selectOption('bridge');
  await sleep(400);
  const vals = await rowValues(page);
  await shot(page, 'tc-wizard');
  note('wizard: t-lan and bridge on one service', vals.join(',') === 't-lan,bridge', vals.join(', '));
  await page.getByRole('button', { name: 'Install', exact: true }).click();
  const d = await stackUp(page, 't-tc');
  const c = d?.compose || '';
  const wrote = /networks:\s*\n\s+t-lan:/.test(c) || /- t-lan/.test(c);
  const def = /default/.test(c);
  const ports = /ports:/.test(c) && !/x-fjord-published/.test(c);
  note('install: running with t-lan + default, ports kept', d?.status?.state === 'running' && wrote && def && ports,
    `state ${d?.status?.state}; t-lan ${wrote}; default ${def}; ports kept ${ports}`);
  const addr = lanAddr(d) || lanAddr(await stackUp(page, 't-tc'));
  const code = addr ? await answers(page, `http://${addr}:3000/`) : 0;
  note('install: answers on its t-lan address', code === 200, `${addr || 'no address'}:3000 -> ${code}`);
  return d;
}

async function openServices(page) {
  await page.goto(BASE + '/#/stacks/t-tc', { waitUntil: 'networkidle' });
  await sleep(2000);
  await typeSel(page).waitFor();
}

// Services tab: host -> none -> networks without saving keeps both rows.
async function roundTrip(page) {
  await openServices(page);
  const before = await rowValues(page);
  await typeSel(page).selectOption('host');
  await sleep(300);
  const hidden = await rowSels(page).count();
  const keptNote = await page.getByText(/interfaces? kept until Save/).isVisible().catch(() => false);
  await typeSel(page).selectOption('none');
  await sleep(300);
  await typeSel(page).selectOption('networks');
  await sleep(300);
  const after = await rowValues(page);
  await shot(page, 'tc-roundtrip');
  note('Services: host / none / networks keeps the rows', hidden === 0 && keptNote && after.join(',') === before.join(','),
    `rows ${before.join('+')} -> host: ${hidden} rows, note ${keptNote} -> back: ${after.join('+')}`);
}

async function save(page) {
  await page.getByRole('button', { name: 'Save', exact: true }).first().click();
  await sleep(2500);
  const apply = page.getByRole('button', { name: 'Apply Changes', exact: true });
  if (await apply.count()) await apply.first().click().catch(() => {});
  await sleep(4000);
}

// Save on host, back to networks, none (survives a reload), networks again.
async function saveBothWays(page) {
  const composeOf = async () => (await stackUp(page, 't-tc'))?.compose || '';
  await openServices(page);
  await typeSel(page).selectOption('host');
  await save(page);
  let c = await composeOf();
  await shot(page, 'tc-saved-host');
  note('Save host: compose network_mode host', /network_mode:\s*host/.test(c), `host ${/network_mode:\s*host/.test(c)}`);
  const code = await answers(page, `http://127.0.0.1:3000/`);
  note('on host: answers on this host :3000', code === 200, `127.0.0.1:3000 -> ${code}`);

  await typeSel(page).selectOption('networks');
  await sleep(300);
  const rows = await rowValues(page);
  note('after saving host, networks brings the rows back', rows.join(',') === 't-lan,bridge', rows.join(', '));
  await save(page);
  c = await composeOf();
  const back = !/network_mode/.test(c) && /t-lan/.test(c) && /default/.test(c);
  await shot(page, 'tc-saved-networks');
  note('Save networks again: t-lan + default', back, `compose ok ${back}`);

  await typeSel(page).selectOption('none');
  await save(page);
  await page.reload({ waitUntil: 'networkidle' });
  await sleep(2000);
  const shown = await typeSel(page).inputValue();
  c = await composeOf();
  note('Save none: compose none, and a reload still shows none', /network_mode:\s*none/.test(c) && shown === 'none', `compose none ${/network_mode:\s*none/.test(c)}; Type after reload "${shown}"`);

  // After a reload the rows are gone from memory: networks starts on bridge.
  await typeSel(page).selectOption('networks');
  await sleep(300);
  const fresh = await rowValues(page);
  await rowSels(page).first().selectOption('t-lan');
  await page.getByRole('button', { name: '+ interface' }).first().click();
  await sleep(300);
  await rowSels(page).nth(1).selectOption('bridge');
  await save(page);
  c = await composeOf();
  const again = !/network_mode/.test(c) && /t-lan/.test(c) && /default/.test(c) && /ports:/.test(c);
  note('from none back to t-lan + bridge', fresh.join(',') === 'bridge' && again, `networks started as ${fresh.join('+')}; compose ok ${again}`);
  const addr = lanAddr(await stackUp(page, 't-tc'));
  const lan = addr ? await answers(page, `http://${addr}:3000/`) : 0;
  note('answers on its t-lan address again', lan === 200, `${addr || 'no address'}:3000 -> ${lan}`);
}

// AppJail in the wizard: bridge stands alone.
async function appjailLimits(page) {
  await openWizard(page, 't-tcj', 'appjail');
  await rowSels(page).first().selectOption('bridge');
  await sleep(300);
  const addDisabled = await page.getByRole('button', { name: '+ interface' }).first().isDisabled();
  const why = await page.getByText(/bridge stands alone/).isVisible().catch(() => false);
  await rowSels(page).first().selectOption('t-lan');
  await sleep(300);
  await page.getByRole('button', { name: '+ interface' }).first().click();
  await sleep(300);
  const bridgeOpt = await rowSels(page).nth(1).locator('option[value="bridge"]').evaluate((o) => ({ disabled: o.disabled, text: o.textContent.trim() }));
  await shot(page, 'tc-appjail');
  note('AppJail: bridge stands alone, and says so', addDisabled && why && bridgeOpt.disabled,
    `+ interface disabled ${addDisabled}, reason shown ${why}; bridge next to t-lan: "${bridgeOpt.text}"`);
  await page.getByRole('button', { name: 'Cancel', exact: true }).click().catch(() => {});
}

(async () => {
  const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-setuid-sandbox'] });
  const page = await (await browser.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(20000);
  const errs = [];
  page.on('pageerror', (e) => errs.push(e.message));
  // Every write the page makes, so a Save that does not take can be read back.
  page.on('request', (r) => {
    if (r.method() === 'GET' || !r.url().includes('/api/')) return;
    let body = r.postData() || '';
    try { const j = JSON.parse(body); body = JSON.stringify({ networks: j.networks, networkModes: j.networkModes, network: j.network }); } catch {}
    console.log(`  ${r.method()} ${r.url().replace(BASE, '')} ${body.slice(0, 300)}`);
  });
  page.on('response', async (r) => {
    if (r.request().method() === 'GET' || !r.url().includes('/api/')) return;
    console.log(`  -> ${r.status()} ${(await r.text().catch(() => '')).slice(0, 200)}`);
  });
  const step = async (id, fn) => {
    try { await fn(); } catch (e) { await shot(page, `fail-${id}`); note(id, false, 'error: ' + e.message.split('\n')[0]); }
  };
  try {
    if (await createLan(page)) {
      await step('install', () => installMixed(page));
      await step('roundtrip', () => roundTrip(page));
      await step('save', () => saveBothWays(page));
      await step('appjail', () => appjailLimits(page));
    }
  } finally {
    note('no page errors', errs.length === 0, errs.length ? errs.join(' | ') : 'none');
    const pass = results.filter((r) => r.ok).length;
    console.log(`\n==== UI: ${pass}/${results.length} passed ====`);
    await browser.close();
  }
})();
