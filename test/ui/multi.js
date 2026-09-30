// One app on several networks, through fjord's pages: picked in the install
// wizard, and added afterwards on the Services tab. run-multi.sh checks from
// the host that every interface got an address and answers.
const { chromium } = require('playwright-core');
const BASE = process.env.FJORD || 'http://127.0.0.1:3567';

const results = [];
const note = (id, ok, msg) => { results.push({ id, ok, msg }); console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`); };
const api = async (page, path, opts) => {
  const r = await page.request.fetch(BASE + path, { ...opts, headers: { Origin: BASE, 'Content-Type': 'application/json', ...(opts?.headers || {}) } });
  const t = await r.text();
  try { return { status: r.status(), body: JSON.parse(t) }; } catch { return { status: r.status(), body: t }; }
};
const shot = (page, name) => page.screenshot({ path: `/out/${name}.png`, fullPage: true }).catch(() => {});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function stackUp(page, name, secs = 300) {
  for (let i = 0; i < secs / 5; i++) {
    const r = await api(page, `/api/stacks/${name}`);
    if (r.body?.status?.state === 'running') return r.body;
    await sleep(5000);
  }
  return (await api(page, `/api/stacks/${name}`)).body;
}

async function createNetwork(page, name, bridge) {
  await page.goto(BASE + '/#/networks', { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: /New network/i }).first().click();
  await page.getByRole('button', { name: 'epair', exact: true }).click();
  await page.getByRole('button', { name: 'both', exact: true }).click();
  await page.getByPlaceholder(/vlan4, lan/).fill(name);
  await page.locator('select').filter({ hasText: 'Select a bridge' }).selectOption(bridge);
  await page.getByRole('button', { name: 'DHCP', exact: true }).click();
  await sleep(500);
  const create = page.getByRole('button', { name: 'Create', exact: true });
  if (await create.isDisabled()) {
    const why = (await page.locator('.text-fjord-warning').allInnerTexts()).join(' ');
    note(`network ${name}`, false, `Create disabled: "${why.trim()}"`);
    return false;
  }
  await create.click();
  await sleep(2000);
  const got = ((await api(page, '/api/networks')).body || []).find((n) => n.name === name);
  note(`network ${name}`, !!got, got ? `DHCP on ${got.bridge}` : 'not listed after Create');
  return !!got;
}

// The interface rows: every row's network picker lists the built-in bridge.
const rowPickers = (page) => page.locator('table select').filter({ has: page.locator('option[value="bridge"]') });

async function install(page, name, engine, nets) {
  await page.goto(BASE + '/', { waitUntil: 'networkidle' });
  await page.getByText('App Store', { exact: true }).first().click();
  await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
  await page.getByRole('button', { name: /^Install/ }).first().click();
  await page.getByRole('button', { name: 'Install', exact: true }).waitFor();
  await page.getByLabel('Name').fill(name);
  if (!(await rowPickers(page).count()) || !(await rowPickers(page).first().isVisible())) {
    await page.getByRole('button', { name: /^Options/ }).first().click();
    await sleep(500);
  }
  if (engine === 'appjail') {
    // The engine picker lives under Advanced.
    const engineSel = page.locator('select').filter({ hasText: 'appjail' });
    if (!(await engineSel.first().isVisible().catch(() => false))) await page.getByText('Advanced', { exact: true }).click();
    await engineSel.first().selectOption('appjail');
    await sleep(1500);
  }
  for (let i = 0; i < nets.length; i++) {
    if (i > 0) { await page.getByRole('button', { name: '+ interface' }).first().click(); await sleep(500); }
    await rowPickers(page).nth(i).selectOption(nets[i]);
    await sleep(500);
  }
  const shown = await rowPickers(page).evaluateAll((ss) => ss.map((s) => s.value));
  await shot(page, `${name}-wizard`);
  note(`${name}: wizard shows every picked network`, nets.every((n, i) => shown[i] === n), `rows: ${shown.join(', ')}`);
  const btn = page.getByRole('button', { name: 'Install', exact: true });
  if (await btn.isDisabled()) {
    const why = (await page.locator('.text-fjord-warning').allInnerTexts()).join(' ');
    note(`${name}: install`, false, `Install disabled: "${why.trim() || 'no reason shown'}"`);
    await page.getByRole('button', { name: 'Cancel', exact: true }).click().catch(() => {});
    return;
  }
  await btn.click();
  const d = await stackUp(page, name);
  const compose = d?.compose || '';
  const inCompose = nets.filter((n) => compose.includes(n));
  await shot(page, `${name}-stack`);
  note(`${name}: install`, d?.status?.state === 'running' && inCompose.length === nets.length,
    `state ${d?.status?.state}; compose has ${inCompose.join(', ') || 'none'} of ${nets.join(', ')}`);
}

// Services tab: add a second network to a running stack and Save.
async function addOnServices(page, name, net) {
  await page.goto(BASE + '/', { waitUntil: 'networkidle' });
  await page.getByText(name, { exact: true }).first().click();
  await sleep(2000);
  await page.getByRole('button', { name: 'Services', exact: true }).first().click().catch(() => {});
  await sleep(1000);
  // A service's editor may be folded; open it by its name.
  if (!(await page.getByRole('button', { name: '+ interface' }).count())) {
    await page.getByRole('button', { name: /openspeedtest/ }).first().click();
    await sleep(800);
  }
  await page.getByRole('button', { name: '+ interface' }).first().click();
  await sleep(500);
  const n = await rowPickers(page).count();
  await rowPickers(page).nth(n - 1).selectOption(net);
  await sleep(500);
  await shot(page, `${name}-services-before-save`);
  await page.getByRole('button', { name: 'Save', exact: true }).first().click();
  await sleep(3000);
  // Save may ask to restart the stack; say yes.
  const restart = page.getByRole('button', { name: /Restart|Save and restart|Apply/ });
  if (await restart.count()) { await restart.first().click(); }
  await sleep(5000);
  const d = await stackUp(page, name);
  await shot(page, `${name}-services-after-save`);
  const on = (d?.compose || '').includes(net);
  note(`${name}: Services tab adds ${net}`, d?.status?.state === 'running' && on, `state ${d?.status?.state}; compose ${on ? 'has' : 'LACKS'} ${net}`);
}

(async () => {
  const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-setuid-sandbox'] });
  const page = await (await browser.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(20000);
  const errs = [];
  page.on('pageerror', (e) => errs.push(e.message));
  const step = async (id, fn) => {
    try { await fn(); } catch (e) {
      await shot(page, `fail-${id}`);
      note(id, false, 'error: ' + e.message.split('\n')[0]);
      await page.getByRole('button', { name: 'Cancel', exact: true }).click({ timeout: 3000 }).catch(() => {});
    }
  };
  try {
    const a = await createNetwork(page, 't-na', 'lanbridge2');
    const b = await createNetwork(page, 't-nb', 'lanbridge');
    if (a && b) {
      await step('t-m2', () => install(page, 't-m2', 'podman', ['t-na', 't-nb']));
      await step('t-j2', () => install(page, 't-j2', 'appjail', ['t-na', 't-nb']));
      await step('t-ms', async () => { await install(page, 't-ms', 'podman', ['t-na']); await addOnServices(page, 't-ms', 't-nb'); });
      await step('t-js', async () => { await install(page, 't-js', 'appjail', ['t-na']); await addOnServices(page, 't-js', 't-nb'); });
    }
  } finally {
    note('no page errors', errs.length === 0, errs.length ? errs.join(' | ') : 'none');
    const pass = results.filter((r) => r.ok).length;
    console.log(`\n==== UI: ${pass}/${results.length} passed ====`);
    await browser.close();
  }
})();
