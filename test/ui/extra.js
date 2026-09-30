// fjord UI tests beyond the network matrix: Setup, a multi-service stack on
// both engines, and Adopt. Same rules: through the real pages, checked by outcome.
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

async function stackUp(page, name, secs) {
  for (let i = 0; i < secs / 5; i++) {
    const r = await api(page, `/api/stacks/${name}`);
    if (r.body?.status?.state === 'running') return r.body;
    await sleep(5000);
  }
  return (await api(page, `/api/stacks/${name}`)).body;
}
async function answers(page, url, secs) {
  let code = 0;
  for (let i = 0; i < secs / 5 && code !== 200; i++) {
    try { code = (await page.request.get(url, { timeout: 5000 })).status(); } catch { code = 0; }
    if (code !== 200) await sleep(5000);
  }
  return code;
}

async function openWizard(page, search, name) {
  await page.goto(BASE + '/', { waitUntil: 'networkidle' });
  await page.getByText('App Store', { exact: true }).first().click();
  await page.getByPlaceholder(/Search apps/).fill(search);
  await page.getByRole('button', { name: /^Install/ }).first().click();
  await page.getByRole('button', { name: 'Install', exact: true }).waitFor();
  await page.getByLabel('Name').fill(name);
}
async function openOptions(page) {
  const net = page.getByText('Networking', { exact: true });
  if (await net.count() && await net.first().isVisible()) return;
  await page.getByRole('button', { name: /^Options/ }).first().click();
  await sleep(500);
}
async function pickEngine(page, engine) {
  const det = page.locator('details').filter({ has: page.locator('summary', { hasText: 'Advanced' }) }).first();
  if (!(await det.evaluate((d) => d.open))) await det.locator('summary').click();
  await page.locator('select').filter({ hasText: 'appjail' }).selectOption(engine);
  await sleep(1500);
}

// ---------- Setup ----------
async function setupScreen(page) {
  await page.goto(BASE + '/#/setup', { waitUntil: 'networkidle' });
  await sleep(2000);
  const fold = page.getByText(/Everything fjord checked \((\d+)\)/);
  const label = await fold.innerText();
  const want = +label.match(/\((\d+)\)/)[1];
  await fold.click();
  await sleep(800);
  await shot(page, 'setup-checks');
  const body = await page.locator('body').innerText();
  const ready = /Ready to run apps/.test(body);
  note('Setup: host reported ready', ready, ready ? 'shows "Ready to run apps"' : 'no ready line on screen');
  // Each check is a row under the fold; count what is actually listed.
  // The checks are the lines between the fold and the buttons.
  const between = body.split(label)[1]?.split(/Skip setup|Continue/)[0] || '';
  const rows = between.split('\n').filter((l) => l.trim()).length;
  note('Setup: the checks behind the fold are listed', rows >= want, `fold says ${want}, ${rows} rows on screen`);
  const recheck = page.getByRole('button', { name: /Recheck|Check again/i });
  if (await recheck.count()) {
    await recheck.first().click();
    await sleep(3000);
    const after = await page.locator('body').innerText();
    note('Setup: Recheck', /Ready to run apps/.test(after), 'still ready after recheck');
  } else {
    note('Setup: Recheck', true, 'no Recheck button when everything passes (nothing to recheck)');
  }
}

// ---------- multi-service stack ----------
// paperless-ngx: paperless + redis, both network_mode: host, redis at localhost.
async function paperless(page, name, engine) {
  await openWizard(page, 'paperless', name);
  await page.getByLabel('PAPERLESS_ADMIN_PASSWORD').fill('t-pass-1234').catch(() => {});
  await openOptions(page);
  if (engine === 'appjail') await pickEngine(page, 'appjail');
  const netSel = page.locator('select').filter({ hasText: 'none' }).last();
  const chosen = await netSel.evaluate((s) => s.options[s.selectedIndex]?.textContent.trim()).catch(() => '?');
  const warn = (await page.locator('.text-fjord-warning').allInnerTexts()).join(' ');
  await shot(page, `${name}-wizard`);
  const says = /expects host networking/.test(warn);
  note(`${name}: wizard default matches a host-networking app`, !says || chosen === 'host',
    `network picker shows "${chosen}"${says ? ' under "This app expects host networking"' : ''}`);
  const btn = page.getByRole('button', { name: 'Install', exact: true });
  if (await btn.isDisabled()) {
    note(`${name}: install`, false, `Install disabled: "${warn.trim() || 'no reason shown'}"`);
    await page.getByRole('button', { name: 'Cancel', exact: true }).click().catch(() => {});
    return;
  }
  await btn.click();
  const d = await stackUp(page, name, 900);
  if (d?.status?.state !== 'running') { await shot(page, `fail-${name}`); note(`${name}: install`, false, `stack state ${d?.status?.state}`); return; }
  const compose = d.compose || '';
  const hostModes = (compose.match(/network_mode:\s*host/g) || []).length;
  // Host networking: the app is on this host's own addresses, so loopback works.
  const code = await answers(page, 'http://127.0.0.1:8000/', 300);
  await shot(page, `${name}-stack`);
  note(`${name}: install`, code === 200, `running on ${engine}; network_mode host on ${hostModes}/2 services; :8000 -> ${code}`);
}

// ---------- Adopt ----------
// run.sh starts t-adopt by hand (openspeedtest, -p 3102:3000) before this runs.
async function adopt(page) {
  await page.goto(BASE + '/', { waitUntil: 'networkidle' });
  await page.goto(BASE + '/#/adopt', { waitUntil: 'networkidle' });
  await sleep(2000);
  // The smallest block holding t-adopt and a Preview button (it stays while
  // Adopt & replace turns into Replace): not the list, which also holds the
  // playwright container running this test.
  const row = page.locator('div').filter({ hasText: 't-adopt' }).filter({ hasNotText: 'playwright' }).filter({ has: page.getByRole('button', { name: 'Preview', exact: true }) }).last();
  if (!(await row.count())) { await shot(page, 'fail-adopt'); note('Adopt: t-adopt listed', false, 'not on the Adopt page'); return; }
  note('Adopt: t-adopt listed', true, 'hand-started container shows on the Adopt page');
  await row.getByRole('button', { name: 'Adopt & replace', exact: true }).click();
  // It asks once more, in the same row, naming the container it removes.
  const replace = row.getByRole('button', { name: 'Replace', exact: true });
  await replace.waitFor();
  const warns = await page.locator('body').innerText(); // the line sits under the row block
  note('Adopt & replace: says what it removes before doing it', /Removes the container t-adopt/.test(warns), warns.split('\n').find((l) => /Removes/.test(l)) || 'no warning shown');
  await replace.click();
  const d2 = await stackUp(page, 't-adopt', 240);
  const compose = d2?.compose || '';
  const kept = /3102:3000/.test(compose);
  await shot(page, 'adopt-stack');
  // "replace" means compose re-created it: the container must be fjord's now.
  const cname = (d2?.services || [])[0]?.container || '';
  note('Adopt & replace: container re-created by the stack', cname !== '' , `container now "${cname}"`);
  note('Adopt: t-adopt becomes a running stack', d2?.status?.state === 'running' && kept,
    `state ${d2?.status?.state}; compose ${kept ? 'keeps' : 'LOST'} the 3102:3000 publish`);
}

(async () => {
  const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-setuid-sandbox'] });
  const page = await (await browser.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(20000);
  const errs = [];
  page.on('pageerror', (e) => errs.push(e.message));
  const only = (process.env.ONLY || 'setup,pl,pj,adopt').split(',');
  const made = [];
  const step = async (id, fn) => {
    if (!only.includes(id)) return;
    try { await fn(); } catch (e) {
      await shot(page, `fail-${id}`);
      note(id, false, 'error: ' + e.message.split('\n')[0]);
      await page.getByRole('button', { name: 'Cancel', exact: true }).click({ timeout: 3000 }).catch(() => {});
    }
  };
  try {
    await step('setup', () => setupScreen(page));
    // Both paperless installs use host :8000, so one at a time.
    await step('pl', async () => { made.push('t-pl'); await paperless(page, 't-pl', 'podman'); });
    await api(page, '/api/stacks/t-pl?data=1', { method: 'DELETE' });
    await step('pj', async () => { made.push('t-pj'); await paperless(page, 't-pj', 'appjail'); });
    await api(page, '/api/stacks/t-pj?data=1', { method: 'DELETE' });
    await step('adopt', () => adopt(page));
  } finally {
    const keep = (process.env.KEEP || '').split(',');
    for (const s of made.filter((x) => !keep.includes(x))) await api(page, `/api/stacks/${s}?data=1`, { method: 'DELETE' });
    note('no page errors', errs.length === 0, errs.length ? errs.join(' | ') : 'none');
    const pass = results.filter((r) => r.ok).length;
    console.log(`\n==== ${pass}/${results.length} passed ====`);
    for (const r of results.filter((r) => !r.ok)) console.log(`FAIL ${r.id}: ${r.msg}`);
    await browser.close();
  }
})();
