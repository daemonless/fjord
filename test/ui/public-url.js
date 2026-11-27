// An app's public address follows the name it is given, and what it needs
// behind the proxy follows the name too. Pocket ID, from a catalog whose
// manifest says APP_URL needs an https:// domain and TRUST_PROXY is true
// behind a proxy (public-url.sh). The proxy is set up first, as the names
// test does. Then, in one wizard: the name fills APP_URL and turns
// TRUST_PROXY on; TRUST_PROXY flipped by hand holds until the name is turned
// off or on again; with no name APP_URL is asked for, with why, and a
// wrong address holds Install.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const RP = 't-rp';
const T = 't-pu';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const shot = (page, n) => page.screenshot({ path: `/out/public-url-${n}.png`, fullPage: true });

(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const ctx = await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 }, ignoreHTTPSErrors: true }));
  const page = await ctx.newPage();
  page.setDefaultTimeout(30000);
  const detail = async (n) => (await page.request.get(BASE + '/api/stacks/' + n)).json().catch(() => ({}));
  const until = async (n, tries = 80) => {
    let d;
    for (let i = 0; i < tries; i++) {
      d = await detail(n);
      if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break;
      await sleep(3000);
    }
    return d;
  };
  try {
    // The names provider, installed and set up for apps.lan.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
    await page.getByPlaceholder(/Search apps/).fill('Reverse Proxy');
    await sleep(800);
    await page.getByRole('button', { name: /^Install/ }).first().click();
    await page.getByRole('button', { name: 'Install', exact: true }).waitFor();
    await page.getByLabel('Name', { exact: true }).fill(RP);
    // It bind-mounts podman's socket: a capability asked for at install.
    const caps = page.getByLabel(/Allow host capabilities/);
    if (await caps.isVisible().catch(() => false)) await caps.check();
    await page.getByRole('button', { name: 'Install', exact: true }).click();
    const rp = await until(RP);
    if (rp?.status?.state !== 'running') throw new Error('proxy not running: ' + rp?.status?.state);
    let finish = '';
    for (let i = 0; i < 20 && !finish; i++) {
      await page.goto(BASE + '/?t=' + Date.now() + '#/stacks', { waitUntil: 'domcontentloaded' });
      await sleep(1500);
      finish = (await page.getByRole('link', { name: 'Finish setup' }).getAttribute('href').catch(() => '')) || '';
    }
    if (!finish) throw new Error('no Finish setup link');
    const caddy = (rp?.status?.containers || []).find((c) => c.address);
    const base = caddy ? `https://${caddy.address}:8443` : finish.split('/#')[0];
    const api = (m, p, data) => ctx.request.fetch(base + p, { method: m, data, headers: { Origin: base } });
    await api('POST', '/api/v1/setup/token', { token: finish.split('#token=')[1] });
    await api('POST', '/api/v1/setup/password', { password: 'correct horse battery staple' });
    await api('PUT', '/api/v1/settings', { domain: 'apps.lan', trustedNetworks: [] });
    let ready = false;
    for (let i = 0; i < 20 && !ready; i++) {
      ready = !!(await (await page.request.get(BASE + '/api/names')).json().catch(() => ({})))?.ready;
      if (!ready) await sleep(3000);
    }
    note('names provider ready', ready, 'apps.lan');

    // Pocket ID from the test catalog.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
    await page.getByPlaceholder(/Search apps/).fill('Pocket');
    await sleep(800);
    await page.getByRole('button', { name: /^Install/ }).first().click();
    const install = page.getByRole('button', { name: 'Install', exact: true });
    await install.waitFor();
    if (!(await page.locator('#repo').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
    await page.locator('#repo').selectOption({ label: 'pu-test' }).catch(() => {});
    await sleep(2000);
    await page.getByLabel('Name', { exact: true }).fill(T);
    await page.locator('#appname').fill('pocket-id');
    await sleep(800);

    const url = page.locator('#APP_URL');
    const trust = page.getByRole('switch', { name: 'TRUST_PROXY' });
    const named = page.getByRole('switch', { name: 'Give it a name' });
    const on = async (sw) => (await sw.getAttribute('aria-checked')) === 'true';
    const says = async (sel) => page.locator(sel).first().isVisible().catch(() => false);
    const state = async () => `APP_URL="${await url.inputValue().catch(() => '?')}" TRUST_PROXY=${await on(trust)}`;

    await shot(page, '1-named');
    note('the name fills APP_URL', (await url.inputValue()) === 'https://pocket-id.apps.lan' && (await says('[data-url-from-name]')), await state());
    note('the name turns TRUST_PROXY on, and says so', (await on(trust)) && /TRUST_PROXY=true/.test(await page.locator('[data-behind-proxy]').innerText().catch(() => '')), await state());
    note('Install offered', await install.isEnabled(), 'checked');

    await trust.click();
    await sleep(500);
    note('TRUST_PROXY off by hand holds, and nothing claims it is on', !(await on(trust)) && !(await says('[data-behind-proxy]')), await state());

    await named.click();
    await sleep(800);
    await shot(page, '2-no-name');
    note('no name: APP_URL asked for, with why', (await url.inputValue()) === '' && (await says('[data-needs-domain]')), await state());
    note('no name: TRUST_PROXY back off', !(await on(trust)), await state());
    note('no name: Install held', !(await install.isEnabled()), 'checked');

    await named.click();
    await sleep(800);
    note('name back: TRUST_PROXY on again, APP_URL the name', (await on(trust)) && (await url.inputValue()) === 'https://pocket-id.apps.lan', await state());

    await page.locator('#appname').fill('auth');
    await sleep(800);
    note('APP_URL follows the name', (await url.inputValue()) === 'https://auth.apps.lan', await state());
    await page.locator('#appname').fill('pocket-id');
    await sleep(800);

    // Typed addresses, with no name.
    await named.click();
    await sleep(500);
    for (const [typed, why] of [
      ['http://pocket.example.com', 'Needs https://, not http://'],
      ['https://192.168.1.5:1411', 'Needs a domain name, not an address'],
      ['https://pocket:1411', 'Needs a full domain name, like pocket.example.com'],
    ]) {
      await url.fill(typed);
      await sleep(300);
      const shown = await page.getByText(why, { exact: true }).isVisible().catch(() => false);
      note(`refuses ${typed}`, shown && !(await install.isEnabled()), shown ? why : 'no reason shown');
    }
    await url.fill('https://pocket.example.com');
    await sleep(300);
    note('takes https://pocket.example.com', await install.isEnabled(), 'Install offered');
    await shot(page, '3-typed');

    // Install with the name: its .env is checked in public-url.sh.
    await url.fill('');
    await named.click();
    await sleep(800);
    if ((await url.inputValue()) === '') {
      // Typed then emptied counts as typed: put the name's address back.
      await url.fill('https://pocket-id.apps.lan');
    }
    note('ready to install with the name', (await url.inputValue()) === 'https://pocket-id.apps.lan' && (await on(trust)), await state());
    await install.click();
    const d = await until(T);
    note('Pocket ID installed and running', d?.status?.state === 'running', `state ${d?.status?.state}`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await shot(page, 'error').catch(() => {});
  } finally {
    await b.close();
  }
})();
