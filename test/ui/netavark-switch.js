// Switching podman's network backend both ways from the System page, on
// podman 5: CNI -> netavark -> CNI. Stacks on a published port, a compose
// network, a LAN pool (pinned address) and LAN DHCP come back each way;
// a network and stack made on netavark come back on CNI. STEP=setup, switch,
// onnv, back, check (after the host rebooted).
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const api = async (page, path, body, method = body ? 'POST' : 'GET') => {
  const r = await page.request.fetch(BASE + path, { method, data: body, headers: { Origin: BASE } });
  let j = null;
  try { j = await r.json(); } catch {}
  return { code: r.status(), body: j, text: j ? '' : await r.text().catch(() => '') };
};
const IMG = 'ghcr.io/daemonless/openspeedtest:latest';
const POOL_IP = '192.168.4.221';
// name -> how it is made
const STACKS = {
  't-nvs-port': { compose: `services:\n  web:\n    image: ${IMG}\n    ports:\n      - "3131:3000"\n` },
  't-nvs-own': { compose: `services:\n  web:\n    image: ${IMG}\n    networks: [inner]\nnetworks:\n  inner: {}\n` },
  't-nvs-pool': { compose: `services:\n  web:\n    image: ${IMG}\n`, networks: [{ network: 't-nvs-pool', service: 'web', ip: POOL_IP, mac: '' }] },
  't-nvs-dhcp': { compose: `services:\n  web:\n    image: ${IMG}\n`, networks: [{ network: 't-nvs-dhcp', service: 'web', ip: '', mac: '' }] },
};
const NV_STACK = 't-nvs-new';

async function settle(page, name, secs = 240) {
  let d;
  for (let i = 0; i < secs / 3; i++) {
    d = (await api(page, '/api/stacks/' + name)).body;
    if (d && !d.busy && d.status?.state === 'running') return d;
    await sleep(3000);
  }
  return d;
}
const addrOf = (d, net) => (d?.status?.containers || []).map((c) => c.addresses?.[net] || c.address).find(Boolean) || '';

async function make(page, name, def) {
  await api(page, `/api/stacks/${name}/save`, { compose: def.compose, env: '', networks: def.networks || [] });
  await api(page, `/api/stacks/${name}/up`, {});
}

// Every stack running; the pool one on its pinned address.
async function allUp(page, label, names) {
  for (const n of names) {
    const d = await settle(page, n);
    note(`${label}: ${n} running`, d?.status?.state === 'running', `state ${d?.status?.state}`);
  }
  const pool = (await api(page, '/api/stacks/t-nvs-pool')).body;
  note(`${label}: the pool stack kept ${POOL_IP}`, addrOf(pool, 't-nvs-pool') === POOL_IP, addrOf(pool, 't-nvs-pool') || 'no address');
}

// The System page's button, its confirmation, and the run to the end.
async function switchTo(page, target) {
  await openPage(page, BASE + '/?t=' + Date.now() + '#/system');
  const btn = page.locator('[data-switch-backend]');
  await btn.waitFor({ timeout: 30000 });
  note(`${target}: the System page offers it`, (await btn.innerText()).includes(target === 'cni' ? 'Switch back to CNI' : 'Switch to netavark'), await btn.innerText());
  await btn.click();
  const confirm = await page.locator('[data-switch-confirm]').innerText();
  note(`${target}: the confirmation names the stacks that restart`, confirm.includes('t-nvs-port') && confirm.includes('AppJail is not touched'), confirm.split('\n').slice(0, 2).join(' | '));
  await page.screenshot({ path: `/out/netavark-switch-confirm-${target}.png`, fullPage: true });
  await page.locator('[data-switch-go]').click();
  // The switch stops every container, this browser's too: the host side of
  // the test waits for it, and the next step checks the result.
  await sleep(2000);
}

// After the switch: the page shows it done and podman is on target.
async function switched(page, target) {
  const st = (await api(page, '/api/system/netavark')).body;
  await openPage(page, BASE + '/?t=' + Date.now() + '#/system');
  await page.locator('[data-switch-log]').waitFor({ timeout: 30000 }).catch(() => {});
  await page.screenshot({ path: `/out/netavark-switch-done-${target}.png`, fullPage: true });
  note(`${target}: podman is on it, the switch says done`, st?.backend === target && st?.result === 'ok', `backend ${st?.backend}, result ${st?.result}: ${(st?.log || '').split('\n').slice(-5).join(' | ')}`);
}

(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(30000);
  try {
    const step = process.env.STEP;
    if (step === 'setup') {
      const st = (await api(page, '/api/system/netavark')).body;
      if (st?.backend !== 'cni' || !st?.canSwitch) {
        console.log(`SKIP t-nvs: needs podman 5 on CNI with netavark-migrate 2 (backend ${st?.backend}, tool ${st?.tool})`);
        return;
      }
      const pool = await api(page, '/api/networks', { name: 't-nvs-pool', kind: 'lan', parent: 'lanbridge', addressSource: 'pool', subnet: '192.168.4.0/24', gateway: '192.168.4.1', rangeStart: '192.168.4.220', rangeEnd: '192.168.4.228' });
      const dhcp = await api(page, '/api/networks', { name: 't-nvs-dhcp', kind: 'lan', parent: 'lanbridge', addressSource: 'dhcp' });
      note('setup: a LAN pool and a LAN DHCP network', pool.code === 200 && dhcp.code === 200, `pool ${pool.code} ${pool.text.trim()} dhcp ${dhcp.code} ${dhcp.text.trim()}`);
      for (const [n, def] of Object.entries(STACKS)) await make(page, n, def);
      await allUp(page, 'on CNI', Object.keys(STACKS));
    } else if (step === 'switch') {
      await switchTo(page, 'netavark');
    } else if (step === 'switched') {
      await switched(page, 'netavark');
      await allUp(page, 'on netavark', Object.keys(STACKS));
    } else if (step === 'onnv') {
      const net = await api(page, '/api/networks', { name: 't-nvs-new', kind: 'nat', subnet: '10.231.0.0/24' });
      note('on netavark: a new network', net.code === 200, `${net.code} ${net.text.trim()}`);
      await make(page, NV_STACK, { compose: `services:\n  web:\n    image: ${IMG}\n`, networks: [{ network: 't-nvs-new', service: 'web', ip: '', mac: '' }] });
      const d = await settle(page, NV_STACK);
      note('on netavark: a stack on it', d?.status?.state === 'running', `state ${d?.status?.state}`);
    } else if (step === 'back') {
      await switchTo(page, 'cni');
    } else if (step === 'backed') {
      await switched(page, 'cni');
      await allUp(page, 'back on CNI', [...Object.keys(STACKS), NV_STACK]);
      const nets = ((await api(page, '/api/networks')).body || []).map((n) => n.name);
      note('back on CNI: the network made on netavark is a CNI network', nets.includes('t-nvs-new'), JSON.stringify(nets.filter((n) => n.startsWith('t-'))));
    } else if (step === 'check') {
      await allUp(page, 'after a reboot', [...Object.keys(STACKS), NV_STACK]);
      const st = (await api(page, '/api/system/netavark')).body;
      note('after a reboot: still on CNI', st?.backend === 'cni', st?.backend);
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
  }
  await b.close();
})();
