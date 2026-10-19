// fjord on a Linux host, through the pages: what Setup and System offer, an
// app from the linuxserver catalog installed from the store, SELinux on a
// folder fjord made and on one it did not, and a macvlan LAN network made on
// the Networks page. PARENT names the interface for the macvlan network.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const PARENT = process.env.PARENT || '';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const api = async (page, path, body, method = body ? 'POST' : 'GET') => {
  const r = await page.request.fetch(BASE + path, { method, data: body, headers: { Origin: BASE } });
  let j = null; try { j = await r.json(); } catch {}
  return { code: r.status(), body: j, text: j ? '' : await r.text().catch(() => '') };
};
async function settle(page, name, secs = 300) {
  let d;
  for (let i = 0; i < secs / 3; i++) { d = (await api(page, `/api/stacks/${name}`)).body; if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break; await sleep(3000); }
  return d;
}

async function setupAndSystem(page) {
  await page.goto(BASE + '/?t=' + Date.now() + '#/setup', { waitUntil: 'networkidle' });
  await sleep(2000);
  const setup = await page.locator('body').innerText();
  note('Setup: podman only, no AppJail', /Apps run in containers, with podman/.test(setup) && !/AppJail/.test(setup), (setup.split('\n').find((l) => l.startsWith('Apps run')) || '(no heading)'));
  await page.goto(BASE + '/?t=' + Date.now() + '#/system', { waitUntil: 'networkidle' });
  await sleep(2500);
  const sys = await page.locator('body').innerText();
  note('System: the host firewall is checked', /host firewall/i.test(sys), /host firewall/i.test(sys) ? 'shown' : 'missing');
}

// The day-one path on Linux: a linuxserver app from the store, nothing
// changed but the name and a free port.
async function installFromStore(page) {
  await page.goto(BASE + '/?t=' + Date.now() + '#/store', { waitUntil: 'networkidle' });
  await page.getByPlaceholder(/Search apps/).fill('apprise');
  await sleep(800);
  const cards = await page.locator('body').innerText();
  note('store: a linuxserver app is offered', /apprise/i.test(cards), /apprise/i.test(cards) ? 'found' : 'not in the store');
  await page.getByRole('button', { name: /^Install/ }).first().click();
  const install = page.getByRole('button', { name: 'Install', exact: true });
  await install.waitFor();
  await page.getByLabel('Name').fill('t-lx-app');
  if (!(await page.locator('#WEB_PORT').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
  await page.locator('#WEB_PORT').fill('3130');
  await page.screenshot({ path: '/out/linux-wizard.png', fullPage: true });
  await install.click();
  const d = await settle(page, 't-lx-app');
  const f = d?.state?.last_failure;
  note('install from the store: running', d?.status?.state === 'running' && !f, `state ${d?.status?.state}${f ? ', failed: ' + f.message : ''}`);
  let code = 0;
  for (let i = 0; i < 20 && !code; i++) { code = await page.request.get('http://127.0.0.1:3130/', { timeout: 5000 }).then((r) => r.status()).catch(() => 0); if (!code) await sleep(3000); }
  note('the app answers on its published port', code > 0 && code < 500, `127.0.0.1:3130 -> ${code}`);
}

// SELinux: an App data folder fjord makes works; a folder it did not make is
// refused at Start with the fix named, on the page.
async function selinux(page) {
  const compose = (src) => `services:\n  box:\n    image: docker.io/library/busybox:1.36\n    command: ["sh", "-c", "echo ok > /data/w && sleep 3600"]\n    volumes:\n      - ${src}:/data\n`;
  await api(page, '/api/stacks/t-lx-own/save', { compose: compose('/containers/t-lx-own/data'), env: '' });
  await api(page, '/api/stacks/t-lx-foreign/save', { compose: compose('/srv/t-lx-foreign'), env: '' });
  for (const [name, want] of [['t-lx-own', 'running'], ['t-lx-foreign', 'refused']]) {
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + name, { waitUntil: 'networkidle' });
    await sleep(1500);
    await page.getByRole('button', { name: 'Start', exact: true }).click();
    await sleep(6000);
    const body = await page.locator('body').innerText();
    const d = (await api(page, `/api/stacks/${name}`)).body;
    await page.screenshot({ path: `/out/linux-${name}.png`, fullPage: true });
    if (want === 'running') note('SELinux: a folder fjord made is the container\'s', d?.status?.state === 'running', `state ${d?.status?.state}`);
    else note('SELinux: a folder it did not make is refused, with the fix', /SELinux/.test(body) && /:z|chcon/.test(body) && d?.status?.state !== 'running', /SELinux/.test(body) ? 'refusal shown' : 'no refusal on the page');
  }
}

// A macvlan network on the Networks page, then an app put on it.
async function macvlan(page) {
  if (!PARENT) { note('macvlan', false, 'PARENT not set'); return; }
  await page.goto(BASE + '/?t=' + Date.now() + '#/networks', { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: /New network/i }).first().click();
  await page.getByRole('button', { name: 'macvlan', exact: true }).click();
  await page.getByPlaceholder(/vlan4, lan/).fill('t-mv');
  await page.locator('#n-parent').selectOption(PARENT);
  await page.getByRole('button', { name: 'DHCP', exact: true }).click();
  await sleep(500);
  await page.screenshot({ path: '/out/linux-macvlan-form.png', fullPage: true });
  await page.getByRole('button', { name: 'Create', exact: true }).click();
  await sleep(2500);
  const net = ((await api(page, '/api/networks')).body || []).find((n) => n.name === 't-mv');
  note('Networks page: a macvlan network with DHCP', net?.driver === 'macvlan', net ? `${net.driver} ${net.addressSource || ''}` : 'missing');
  await api(page, '/api/stacks/t-lx-mv/save', { compose: 'services:\n  web:\n    image: docker.io/traefik/whoami:v1.10\n', env: '', networks: [{ network: 't-mv', service: 'web', ip: '', mac: '' }] });
  await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/t-lx-mv', { waitUntil: 'networkidle' });
  await sleep(1500);
  await page.getByRole('button', { name: 'Start', exact: true }).click();
  const d = await settle(page, 't-lx-mv', 120);
  const addr = (d?.status?.containers || []).map((c) => c.addresses?.['t-mv']).find(Boolean) || '';
  note('an app on it gets a LAN address', !!addr, addr || 'none');
}

(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(30000);
  for (const step of [setupAndSystem, installFromStore, selinux, macvlan]) {
    try { await step(page); } catch (e) {
      note(step.name, false, e.message.split('\n')[0]);
      await page.screenshot({ path: `/out/linux-${step.name}-error.png`, fullPage: true }).catch(() => {});
    }
  }
  await b.close();
})();
