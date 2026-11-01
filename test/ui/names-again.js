// A name goes with its app, on a host whose provider is already there (the
// names test installs its own, which needs ports 80 and 443 free). APP is
// installed under a name, deleted (the name leaves the proxy) and installed
// again under the same stack name: the name is back and points at the new
// app, not at the address of the one that is gone.
const { chromium } = require('playwright-core');
const { signedIn } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const APP = process.env.APP || 'apprise';
const T = 't-nm';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const shot = (page, n) => page.screenshot({ path: `/out/names-again-${n}.png`, fullPage: true });

(async () => {
  // The provider's Caddy, by its own address: this browser runs ON the host,
  // where pf never redirects a host's published port to itself.
  let b = await chromium.launch({ args: ['--no-sandbox'] });
  let page = await (await b.newContext(signedIn())).newPage();
  const req = async (path, body, method = body ? 'POST' : 'GET') => {
    const r = await page.request.fetch(BASE + path, { method, data: body, headers: { Origin: BASE }, timeout: 240000 });
    let j = null; try { j = await r.json(); } catch {}
    return { code: r.status(), body: j };
  };
  const names = (await req('/api/names')).body || {};
  const prov = (await req('/api/stacks/' + names.provider)).body;
  const caddy = (prov?.status?.containers || []).find((c) => c.address);
  note('a provider is there and ready', !!names.provider && names.ready && !!caddy?.address, `${names.provider || 'none'}, domain ${names.domain}, Caddy at ${caddy?.address}`);
  await b.close();
  if (!caddy?.address) return;
  b = await chromium.launch({ args: ['--no-sandbox', `--host-resolver-rules=MAP *.${names.domain} ${caddy.address}`] });
  page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 }, ignoreHTTPSErrors: true }))).newPage();
  page.setDefaultTimeout(30000);
  try {
    const settle = async (name) => {
      let d;
      for (let i = 0; i < 100; i++) { d = (await req('/api/stacks/' + name)).body; if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break; await sleep(3000); }
      return d;
    };
    const installNamed = async () => {
      await page.goto(BASE + '/?t=' + Date.now() + '#/store', { waitUntil: 'domcontentloaded' });
      await sleep(2000);
      await page.getByPlaceholder(/Search apps/).fill(APP);
      await sleep(800);
      await page.getByRole('button', { name: /^Install/ }).first().click();
      const install = page.getByRole('button', { name: 'Install', exact: true });
      await install.waitFor();
      await page.getByLabel('Name', { exact: true }).fill(T);
      const sw = page.getByRole('switch', { name: 'Give it a name' });
      if ((await sw.getAttribute('aria-checked')) !== 'true') await sw.click();
      await install.click();
      const d = await settle(T);
      // The name is given when the install's stream ends, after the stay-up check.
      const t = page.getByRole('status').filter({ hasText: /is at |has no name yet/ });
      await t.waitFor({ timeout: 120000 }).catch(() => {});
      let rec = null;
      for (let i = 0; i < 20 && !rec; i++) { rec = (await req('/api/names')).body?.names?.[T] || null; if (!rec) await sleep(3000); }
      return { d, rec };
    };
    const one = await installNamed();
    await shot(page, '1-installed-named');
    note('an app installed with a name has one', one.d?.status?.state === 'running' && !!one.rec?.url, one.rec?.url ? `${one.rec.url} -> ${one.rec.target?.address}:${one.rec.target?.port}` : `state ${one.d?.status?.state}, no name`);
    const del = await req(`/api/stacks/${T}?data=1`, null, 'DELETE');
    let still = true;
    for (let i = 0; i < 20 && still; i++) { await sleep(3000); still = !!(await req('/api/names')).body?.names?.[T]; }
    note('delete takes the name with it', del.code === 200 && !still, `delete ${del.code}, name ${still ? 'STILL on the proxy' : 'gone'}`);
    const two = await installNamed();
    await shot(page, '2-installed-again');
    const moved = !!two.rec?.target && (two.rec.target.address !== one.rec?.target?.address || two.rec.target.port !== one.rec?.target?.port) ? ' (a new address)' : '';
    // Through the proxy, as a device with the DNS record would open it.
    let via = 'no name';
    if (two.rec?.url) {
      const c2 = await b.newContext({ ignoreHTTPSErrors: true });
      const p2 = await c2.newPage();
      via = await p2.goto(two.rec.url + '/', { waitUntil: 'commit', timeout: 20000 }).then((r) => r.status()).catch((e) => `error: ${e.message.split('\n')[0]}`);
      await c2.close();
    }
    note('installed again under its name, the name points at the new app', !!two.rec?.url && [200, 302, 404].includes(via), two.rec?.url ? `${two.rec.url} -> ${two.rec.target?.address}:${two.rec.target?.port}${moved}, answers ${via}` : 'no name');
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await shot(page, 'error').catch(() => {});
  }
  await b.close();
})();
