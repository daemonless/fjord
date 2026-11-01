// Names through a provider installed from the store (the reverse proxy):
// its dashboard row says Finish setup (the link signs in with the token
// fjord made), its own page offers names for the apps already there, Add
// names gives them, the dashboard and the stack page open by name, and a
// device that does not trust the provider gets the shield beside the name.
// Needs a catalog with a provides.names app (names.sh) and one app with a
// page already installed.
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const STACK = 't-rp';
const APP = process.env.APP || 'apprise';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const shot = (page, n) => page.screenshot({ path: `/out/names-${n}.png`, fullPage: true });

(async () => {
  const context = (opts) => b.newContext(signedIn({ viewport: { width: 1400, height: 1000 }, ...opts }));
  let b = await chromium.launch({ args: ['--no-sandbox'] });
  let ctx = await context({ ignoreHTTPSErrors: true });
  let page = await ctx.newPage();
  page.setDefaultTimeout(30000);
  try {
    // Install the proxy as anyone would: store, Install, nothing changed but the name.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
    await page.getByPlaceholder(/Search apps/).fill('Reverse Proxy');
    await sleep(800);
    await page.getByRole('button', { name: /^Install/ }).first().click();
    const install = page.getByRole('button', { name: 'Install', exact: true });
    await install.waitFor();
    await page.getByLabel('Name', { exact: true }).fill(STACK);
    note('wizard: no name row for the proxy itself', !(await page.getByText('Open it at').isVisible().catch(() => false)), 'checked');
    await shot(page, '1-wizard');
    await install.click();
    let d;
    for (let i = 0; i < 80; i++) {
      d = await (await page.request.get(BASE + '/api/stacks/' + STACK)).json().catch(() => ({}));
      if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break;
      await sleep(3000);
    }
    note('proxy installed and running', d?.status?.state === 'running', `state ${d?.status?.state}`);

    // Dashboard: the provider's own row ends in Finish setup, signed in by
    // the token fjord made. No banner.
    let finish = '';
    for (let i = 0; i < 20 && !finish; i++) {
      await page.goto(BASE + '/?t=' + Date.now() + '#/stacks', { waitUntil: 'domcontentloaded' });
      await sleep(1500);
      finish = (await page.getByRole('link', { name: 'Finish setup' }).getAttribute('href').catch(() => '')) || '';
      if (!finish) await sleep(3000);
    }
    await shot(page, '2-finish-setup');
    note('the provider row says Finish setup', /#token=[0-9a-f]{64}$/.test(finish), finish.replace(/token=.*/, 'token=…') || 'no Finish setup link');
    note('no banner on the dashboard', !(await page.getByRole('region', { name: 'Reverse proxy' }).isVisible().catch(() => false)), 'checked');
    if (!finish) throw new Error('no setup link');
    const token = finish.split('#token=')[1];
    // The link is right for a browser elsewhere (this host's address and
    // published port); this browser runs ON the host, where pf never
    // redirects a host to itself, so it goes to Caddy's own address.
    const caddy = (d?.status?.containers || []).find((c) => c.address);
    const base = caddy ? `https://${caddy.address}:8443` : finish.split('/#')[0];
    finish = `${base}/#token=${token}`;

    // The proxy's page opens on its setup with the token in hand.
    const pp = await ctx.newPage();
    await openPage(pp, finish);
    await sleep(1500);
    await pp.screenshot({ path: '/out/names-3-provider-setup.png', fullPage: true });
    await pp.close();

    // Finish the proxy's setup through its API (its own UI tests cover the screens).
    const api = (m, p, data) => ctx.request.fetch(base + p, { method: m, data, headers: { Origin: base } });
    const r1 = await api('POST', '/api/v1/setup/token', { token });
    const r2 = await api('POST', '/api/v1/setup/password', { password: 'correct horse battery staple' });
    const r3 = await api('PUT', '/api/v1/settings', { domain: 'apps.lan', trustedNetworks: [] });
    note('proxy accepts the token fjord made', r1.ok() && r2.ok() && r3.ok(), `${r1.status()} ${r2.status()} ${r3.status()}`);
    // Nothing on the test host resolves the ending, and a browser that
    // cannot find the names is rightly given addresses (the no-dns shield).
    // From here on the browser resolves them where the proxy answers, as a
    // device with the DNS record does. It does not trust the proxy's
    // certificate (no HTTPS leniency now: that made the trust probe pass),
    // so the shield says so, as on a new device.
    await b.close();
    b = await chromium.launch({ args: ['--no-sandbox', `--host-resolver-rules=MAP *.apps.lan ${new URL(base).hostname}`] });
    ctx = await context({});
    page = await ctx.newPage();
    page.setDefaultTimeout(30000);

    // Back in fjord, on the provider's own page: names for the apps already there.
    let ask = false;
    for (let i = 0; i < 10 && !ask; i++) {
      await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + STACK, { waitUntil: 'domcontentloaded' });
      await sleep(2000);
      ask = await page.getByRole('button', { name: /^Add names?…$/ }).isVisible().catch(() => false);
    }
    await shot(page, '4-give-names');
    const line = await page.getByText(/no name yet\. Like .*\.apps\.lan, home network only/).innerText().catch(() => '');
    note('the provider page offers names', ask, line || 'no Add names');
    if (!ask) throw new Error('no Add names');
    await page.getByRole('button', { name: /^Add names?…$/ }).click();
    const picker = page.getByRole('group', { name: 'Choose names' });
    note('Add names opens a list to pick from', await picker.isVisible().catch(() => false), `${await picker.getByRole('checkbox').count()} apps listed`);
    await picker.getByRole('button', { name: /^Add (name|\d+ names)$/ }).click();
    const t = page.getByRole('status').filter({ hasText: /apps have names|is ready/ });
    await t.waitFor({ timeout: 60000 }).catch(() => {});
    const toastText = await t.innerText().catch(() => '');
    await shot(page, '5-names-added');
    note('Add names: toast with Undo', /Undo/.test(toastText), toastText.replace(/\s+/g, ' ') || 'no toast');

    const px = await (await page.request.get(BASE + '/api/names')).json();
    const named = Object.keys(px.names || {}).filter((n) => n !== STACK);
    note('apps have names', named.length > 0, named.map((n) => px.names[n].url).join(', ') || 'none');
    const first = named[0];

    // The dashboard links the name, with the shield: this browser does not
    // trust the provider (it cannot even find apps.lan).
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks', { waitUntil: 'domcontentloaded' });
    await sleep(4000);
    const rowLink = await page.getByRole('link', { name: new RegExp(px.names[first].url.replace(/^https?:\/\//, '')) }).first().getAttribute('href').catch(() => '');
    note('the dashboard links the name', rowLink === px.names[first].url, rowLink || 'no link');
    await page.getByLabel(/doesn't trust/).first().click();
    await sleep(300);
    await shot(page, '5-shield');
    const trust = await page.getByRole('link', { name: 'Set up this device' }).getAttribute('href').catch(() => '');
    // By name: this device resolves the names, it only does not trust them.
    note('the shield offers to set up this device', trust === 'http://proxy.apps.lan/trust', trust || 'none');

    // The stack page: Open goes to the name; the menu beside it lists the
    // name and the address, and the one picked is what Open uses after.
    const page2 = BASE + '/?t=' + Date.now() + '#/stacks/' + first;
    const openHref = () => page.getByRole('link', { name: /^Open\b/ }).first().getAttribute('href').catch(() => '');
    await page.goto(page2, { waitUntil: 'domcontentloaded' });
    await sleep(2500);
    const byName = await openHref();
    note('Open goes to the name', byName === px.names[first].url, byName || 'no Open');
    await page.getByTitle(/^Open by name/).click();
    await shot(page, '6-open-menu');
    const byNameItem = await page.getByRole('button', { name: /^By name/ }).isVisible().catch(() => false);
    const byAddrItem = await page.getByRole('button', { name: /^Direct/ }).isVisible().catch(() => false);
    note('menu offers by name and direct', byNameItem && byAddrItem, `name ${byNameItem}, direct ${byAddrItem}`);
    const popup = page.waitForEvent('popup').catch(() => null);
    await page.getByRole('button', { name: /^Direct/ }).click();
    const pop = await popup;
    if (pop) await pop.close().catch(() => {});
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + first, { waitUntil: 'domcontentloaded' });
    await sleep(2500);
    const byAddr = await openHref();
    note('picking the address is remembered', !!byAddr && !/apps\.lan/.test(byAddr), byAddr || 'no Open');
    // Back to names for the rest (and for whoever looks at netlab next).
    await page.evaluate(() => localStorage.setItem('fjord.openBy', 'name'));

    // Change the name from the menu (inline editor, Save), then Undo from the toast.
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + first, { waitUntil: 'domcontentloaded' });
    await sleep(2500);
    await page.getByTitle(/^Open by name/).click();
    await page.getByRole('button', { name: /^Change name/ }).click();
    const nameBox = page.getByLabel('Open it at');
    const oldLabel = await nameBox.inputValue();
    await nameBox.fill(oldLabel + '-two');
    await shot(page, '6b-change-name');
    // The form's Save, not the stack page's.
    await nameBox.locator('xpath=ancestor::form').getByRole('button', { name: 'Save', exact: true }).click();
    await page.getByRole('status').filter({ hasText: /is now/ }).waitFor();
    let renamed = (await (await page.request.get(BASE + '/api/names')).json()).names?.[first]?.name;
    note('Change name renames the host', renamed === oldLabel + '-two', `name ${renamed}`);
    await page.getByRole('button', { name: 'Undo' }).click();
    for (let i = 0; i < 10 && renamed !== oldLabel; i++) {
      await sleep(1000);
      renamed = (await (await page.request.get(BASE + '/api/names')).json()).names?.[first]?.name;
    }
    note('Undo gives the old name back', renamed === oldLabel, `name ${renamed}`);

    // Remove from the menu, then Undo from the toast.
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + first, { waitUntil: 'domcontentloaded' });
    await sleep(2500);
    await page.getByTitle(/^Open by name/).click();
    await page.getByRole('button', { name: /^Remove (name|\d+ names)$/ }).click();
    await page.getByRole('status').filter({ hasText: /has no names? now/ }).waitFor();
    let gone = !(await (await page.request.get(BASE + '/api/names')).json()).names?.[first];
    await page.getByRole('button', { name: 'Undo' }).click();
    await sleep(4000);
    const back = !!(await (await page.request.get(BASE + '/api/names')).json()).names?.[first];
    note('remove a name, Undo brings it back', gone && back, `removed ${gone}, back ${back}`);

    // A name goes with its app. Deleted and installed again under the same
    // stack name, the app gets a name that points at the new one -- not a
    // record left on the proxy pointing at an address that is gone.
    const T = 't-nm';
    const req = async (path, body, method = body ? 'POST' : 'GET') => {
      const r = await page.request.fetch(BASE + path, { method, data: body, headers: { Origin: BASE }, timeout: 240000 });
      let j = null; try { j = await r.json(); } catch {}
      return { code: r.status(), body: j };
    };
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
    await shot(page, '7-installed-named');
    note('an app installed with a name has one', one.d?.status?.state === 'running' && !!one.rec?.url, one.rec?.url ? `${one.rec.url} -> ${one.rec.target?.address}:${one.rec.target?.port}` : `state ${one.d?.status?.state}, no name`);
    const del = await req(`/api/stacks/${T}?data=1`, null, 'DELETE');
    let still = true;
    for (let i = 0; i < 20 && still; i++) { await sleep(3000); still = !!(await req('/api/names')).body?.names?.[T]; }
    note('delete takes the name with it', del.code === 200 && !still, `delete ${del.code}, name ${still ? 'STILL on the proxy' : 'gone'}`);
    const two = await installNamed();
    await shot(page, '8-installed-again');
    // Through the proxy, as a device with the DNS record would open it: a
    // page of this browser (it resolves the ending), trusting the proxy.
    let via = 'no name';
    if (two.rec?.url) {
      const c2 = await b.newContext({ ignoreHTTPSErrors: true });
      const p2 = await c2.newPage();
      via = await p2.goto(two.rec.url + '/', { waitUntil: 'commit', timeout: 20000 }).then((r) => r.status()).catch((e) => `error: ${e.message.split('\n')[0]}`);
      await c2.close();
    }
    note('installed again under its name, the name points at the new app', !!two.rec?.url && [200, 302, 404].includes(via), two.rec?.url ? `${two.rec.url} -> ${two.rec.target?.address}:${two.rec.target?.port}, answers ${via}` : 'no name');

  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await shot(page, 'error').catch(() => {});
  }
  await b.close();
})();
