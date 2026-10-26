// A fjord LINKED to a proxy elsewhere (Settings, Names, Link a reverse
// proxy), the Linux case: the wizard offers a name, refuses one another
// fjord holds, the app installs with its name, Open and the dashboard go
// to it, the name follows a port change, Remove name takes it away.
// names-linked.sh checks the name answers from off-host and cleans up.
const { chromium } = require('playwright-core');
const { signedIn } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const STACK = 't-nm';
const APP = process.env.APP || 'apprise';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const shot = (page, n) => page.screenshot({ path: `/out/names-linked-${n}.png`, fullPage: true });
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }))).newPage();
  page.setDefaultTimeout(30000);
  const api = async (path, body, method = body ? 'POST' : 'GET') => {
    const r = await page.request.fetch(BASE + path, { method, data: body, headers: { Origin: BASE }, timeout: 240000 });
    let j = null; try { j = await r.json(); } catch {}
    return { code: r.status(), body: j };
  };
  const settle = async (name, secs = 300) => {
    let d;
    for (let i = 0; i < secs / 3; i++) { d = (await api(`/api/stacks/${name}`)).body; if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break; await sleep(3000); }
    return d;
  };
  try {
    const p0 = (await api('/api/names')).body;
    note('fjord is linked to a provider', !!p0?.linked && !!p0?.ready, p0?.provider ? `${p0.provider}, ${p0.ready ? 'ready' : 'not ready'}` : 'no provider');
    if (!p0?.ready) throw new Error('no provider');
    const taken = (p0.taken || [])[0];
    await page.goto(BASE + '/?t=' + Date.now() + '#/store', { waitUntil: 'load' });
    await page.evaluate(() => localStorage.setItem('fjord.openBy', 'name'));
    await page.getByPlaceholder(/Search apps/).fill(APP);
    await sleep(800);
    await page.getByRole('button', { name: /^Install/ }).first().click();
    const install = page.getByRole('button', { name: 'Install', exact: true });
    await install.waitFor();
    await page.getByLabel('Name', { exact: true }).fill(STACK);
    const sw = page.getByRole('switch', { name: 'Give it a name' });
    note('the wizard offers a name', await sw.isVisible().catch(() => false), 'Give it a name');
    if ((await sw.getAttribute('aria-checked')) !== 'true') await sw.click();
    const nameBox = page.getByLabel('Open it at');
    note('the name is pre-filled from the stack name', (await nameBox.inputValue()) === STACK, `"${await nameBox.inputValue()}"`);
    if (taken) {
      await nameBox.fill(taken);
      await sleep(500);
      const said = await page.getByText(new RegExp(`${taken}\\.${p0.domain} is .*already`)).first().isVisible().catch(() => false);
      note("a name another fjord holds is refused", said && (await install.isDisabled()), said ? `"${taken}" said taken, Install ${await install.isDisabled() ? 'held' : 'NOT held'}` : 'nothing said');
      await shot(page, '1-taken');
      await nameBox.fill(STACK);
      await sleep(500);
    } else note('a name another fjord holds is refused', true, 'no other fjord holds a name (not judged)');
    await shot(page, '2-wizard');
    await install.click();
    let d = await settle(STACK);
    note('installed with its name', d?.status?.state === 'running', `state ${d?.status?.state}`);
    // The page gives the name when the install's stream ends (after the
    // stay-up check), so stay on it until the toast says where the app is.
    const t = page.getByRole('status').filter({ hasText: /is at |has no name yet/ });
    const toastText = await t.waitFor({ timeout: 120000 }).then(() => t.innerText()).catch(() => '');
    note('a toast says where the app is', /is at /.test(toastText), toastText.replace(/\s+/g, ' ') || 'no toast');
    let px = (await api('/api/names')).body;
    const url = px?.names?.[STACK]?.url || '';
    note('the name points at the app', /^https:\/\/t-nm\./.test(url) && px.names[STACK].target?.port > 0, url ? `${url} -> ${px.names[STACK].target?.address}:${px.names[STACK].target?.port}` : 'no name');
    if (!url) throw new Error('the app got no name');
    console.log(`NAME ${url}`);

    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + STACK, { waitUntil: 'load' });
    const openLink = page.getByRole('link', { name: /^Open\b/ }).first();
    await openLink.waitFor({ timeout: 20000 }).catch(() => {});
    await shot(page, '3-stack');
    const open = await openLink.getAttribute('href').catch(() => '');
    note('Open goes to the name', !!open && open === url, open || 'no Open');
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks', { waitUntil: 'load' });
    await sleep(3000);
    const row = await page.getByRole('link', { name: new RegExp(url.replace(/^https?:\/\//, '')) }).first().getAttribute('href').catch(() => '');
    note('the dashboard links the name', row === url, row || 'no link');

    // The port moves (as a person would do it in the editor): the name follows.
    const env = d.env || '';
    const m = env.match(/^([A-Z_]*PORT[A-Z_]*)=(\d+)$/m);
    if (m) {
      const was = px.names[STACK].target.port;
      const next = String(Number(m[2]) + 1);
      await api(`/api/stacks/${STACK}/save`, { compose: d.compose, env: env.replace(m[0], `${m[1]}=${next}`) });
      const up = await api(`/api/stacks/${STACK}/up`, {});
      d = await settle(STACK);
      let now = was;
      for (let i = 0; i < 20 && now === was; i++) { await sleep(3000); px = (await api('/api/names')).body; now = px?.names?.[STACK]?.target?.port || 0; }
      console.log('RECORD', JSON.stringify(px?.names?.[STACK] || null));
      note('the name follows the port', up.code === 200 && now === Number(next), `${m[1]} ${m[2]} -> ${next}, name's target port ${now}`);
      console.log(`PORT ${now}`);
    } else note('the name follows the port', true, 'no port variable in .env (not judged)');
    // Through the proxy, as a device on the network would: this VM asks the
    // proxy, which connects back here (no hairpin on a Linux host).
    const via = await page.request.get(url + '/', { ignoreHTTPSErrors: true, timeout: 15000, maxRedirects: 0 }).then((r) => r.status()).catch((e) => `error: ${e.message.split('\n')[0]}`);
    note('the name answers through the proxy', via === 200 || via === 404 || via === 302, `${url} -> ${via}`);

    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + STACK, { waitUntil: 'load' });
    await sleep(2500);
    await page.getByTitle(/^Open by name/).click();
    await shot(page, '4-menu');
    await page.getByRole('button', { name: /^Remove (name|\d+ names)$/ }).click();
    await page.getByRole('status').filter({ hasText: /has no names? now/ }).waitFor();
    px = (await api('/api/names')).body;
    note('Remove name takes it away', !px?.names?.[STACK], px?.names?.[STACK] ? 'still named' : 'gone');
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
  }
  await b.close();
})();
