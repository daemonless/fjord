// A FreeBSD host whose podman uses the netavark network backend, through the
// pages: New Network offers both kinds and says why the LAN kind has no
// DHCP, a bridge network made there is podman's own, a LAN network left from
// the CNI backend is still listed, says why podman no longer loads it and can
// be deleted, and the System page checks netavark's pieces, not CNI's.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const NET = 't-nvui';
const LEGACY = 't-legacy';
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const rowOf = (page, name) =>
  page.locator('span.font-mono', { hasText: new RegExp('^' + name + '$') }).first().locator('xpath=ancestor::div[contains(@class,"gap-3") and contains(@class,"px-4")][1]');
const networks = async (page) => (await (await page.request.get(BASE + '/api/networks')).json()) || [];
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(20000);
  try {
    // 1. New Network: both kinds, and the LAN kind says why it has no DHCP.
    await openPage(page, BASE + '/#/networks');
    await page.getByRole('button', { name: /New network/i }).first().click();
    await page.locator('[aria-label="New Network"]').waitFor();
    const kinds = ((await (await page.request.get(BASE + '/api/networks/kinds?engine=podman')).json()).kinds || []).map((k) => k.id);
    note('podman is offered the LAN kind and the bridge kind', kinds.join(',') === 'lan,nat', `podman's kinds: ${kinds.join(', ')}`);
    await page.getByRole('button', { name: 'epair', exact: true }).first().click();
    const form = await page.locator('[aria-label="New Network"]').innerText();
    await page.screenshot({ path: '/out/netavark-form.png', fullPage: true });
    // DHCP on a LAN network needs the epair plugin in netavark's plugin
    // directory: offered with it, explained without it.
    const lanKind = ((await (await page.request.get(BASE + '/api/networks/kinds?engine=podman')).json()).kinds || []).find((k) => k.id === 'lan') || {};
    if (lanKind.supportsDhcp) note('the LAN kind offers DHCP (the plugin is installed)', !/needs the epair plugin/.test(form), 'no missing-plugin note on the form');
    else note('the LAN kind says why it cannot use DHCP', /needs the epair plugin/.test(form), /netavark/.test(form) ? 'the note is on the form' : 'no note');
    await page.getByRole('button', { name: 'bridge', exact: true }).first().click();
    await page.locator('#n-name').waitFor();

    // 2. A bridge network made on the form is one podman knows.
    await page.locator('#n-name').fill(NET);
    await page.locator('#n-priv').fill('10.125.0.0/24');
    await page.getByRole('button', { name: 'Create', exact: true }).click();
    let made;
    for (let i = 0; i < 20 && !made; i++) {
      made = (await networks(page)).find((n) => n.name === NET);
      if (!made) await page.waitForTimeout(500);
    }
    note('the form created it', !!made && made.subnet === '10.125.0.0/24' && made.gateway === '10.125.0.1' && !made.problem,
      made ? `${made.driver} ${made.subnet} gw ${made.gateway} on ${made.bridge || '?'}` : 'not listed');

    // 3. The LAN network the CNI backend left behind.
    await openPage(page, BASE + '/#/networks');
    const legacy = rowOf(page, LEGACY);
    await legacy.waitFor();
    // Its bridge does not exist on this host, so fjord cannot make it again
    // and the row says so.
    for (let i = 0; i < 180; i++) {
      const l = (await networks(page)).find((n) => n.name === LEGACY);
      if (l && /could not make it again/.test(l.problem || '')) break;
      await page.waitForTimeout(500);
    }
    await page.reload(); // the same address again does not load the list again
    await legacy.waitFor();
    const why = await legacy.innerText();
    note('it says why fjord could not bring it back', /could not make it again by itself/.test(why) && /tnobridge/.test(why), `"${why.replace(/\s+/g, ' ').trim().slice(-200)}"`);
    note('it says why podman no longer loads it', /podman no longer loads this network/.test(why) && /netavark/.test(why), `"${why.replace(/\s+/g, ' ').trim().slice(0, 160)}"`);
    await legacy.getByRole('button', { name: 'Delete', exact: true }).click();
    await legacy.getByRole('button', { name: 'Delete', exact: true }).click(); // confirm
    await legacy.waitFor({ state: 'hidden', timeout: 30000 });
    note('it can be deleted from the page', !(await networks(page)).some((n) => n.name === LEGACY), 'gone from the page and the API');

    // 3b. CNI-era networks fjord can make again come back by themselves:
    // same name, bridge, subnet. Nobody clicks.
    const settled = async (name) => {
      let n;
      for (let i = 0; i < 180; i++) {
        n = (await networks(page)).find((x) => x.name === name);
        if (n && !n.problem) break;
        await page.waitForTimeout(500);
      }
      return n;
    };
    const lanAgain = await settled('t-oldlan');
    note('the LAN network came back by itself on its bridge', !!lanAgain && !lanAgain.problem && lanAgain.driver === 'epair' && lanAgain.bridge === 'tnvbr' && lanAgain.subnet === '10.136.0.0/24',
      lanAgain ? `${lanAgain.driver} ${lanAgain.subnet} on ${lanAgain.bridge || '?'}${lanAgain.problem ? ' problem: ' + lanAgain.problem : ''}` : 'not listed');
    const again = await settled('t-oldpriv');
    await openPage(page, BASE + '/#/networks');
    await page.screenshot({ path: '/out/netavark-remade.png', fullPage: true });
    note('the private network came back by itself: same name and subnet', !!again && !again.problem && again.subnet === '10.135.0.0/24' && again.gateway === '10.135.0.1',
      again ? `${again.driver} ${again.subnet} gw ${again.gateway} problem=${again.problem || 'none'}` : 'not listed');

    // The bridge network goes the same way.
    await openPage(page, BASE + '/#/networks');
    const disclosure = page.getByRole('button', { name: /not reachable from your LAN/ }).first();
    if (await disclosure.count()) await disclosure.click().catch(() => {});
    const mine = rowOf(page, NET);
    await mine.waitFor();
    await mine.getByRole('button', { name: 'Delete', exact: true }).click();
    await mine.getByRole('button', { name: 'Delete', exact: true }).click(); // confirm
    await mine.waitFor({ state: 'hidden', timeout: 30000 });
    note('the bridge network is deleted from the page', !(await networks(page)).some((n) => n.name === NET), 'gone from the page and the API');

    // 4. System: netavark's checks, none of CNI's.
    await openPage(page, BASE + '/#/system');
    // Passing checks are folded into one row.
    await page.getByRole('button', { name: /other checks passing|checks passing/ }).first().click();
    await page.getByText(/pf firewall/).first().waitFor();
    await page.screenshot({ path: '/out/netavark-system.png', fullPage: true });
    // What fjord did by itself is said on the page, until dismissed.
    const notice = page.locator('[data-testid="host-notice"]').first();
    const noticeText = (await notice.count()) ? await notice.innerText() : '';
    note('System says which networks fjord made again by itself', /Made the network t-oldlan again/.test(noticeText) && /Made the network t-oldpriv again/.test(noticeText),
      noticeText ? `"${noticeText.replace(/\s+/g, ' ').trim().slice(0, 200)}"` : 'no notice on the page');
    if (noticeText) {
      await notice.getByRole('button', { name: 'Dismiss', exact: true }).click();
      await notice.waitFor({ state: 'hidden', timeout: 10000 }).catch(() => {});
      await page.reload();
      await page.getByText(/pf firewall|checks passing/).first().waitFor();
      note('the note stays dismissed after a reload', (await page.locator('[data-testid="host-notice"]').count()) === 0, 'not shown again');
      await page.getByRole('button', { name: /other checks passing|checks passing/ }).first().click().catch(() => {});
      await page.getByText(/pf firewall/).first().waitFor();
    }
    const sys = await page.locator('body').innerText();
    note('System checks aardvark-dns for names', /container name resolution \(aardvark-dns\)/.test(sys), /aardvark-dns/.test(sys) ? 'listed' : 'not listed');
    note('System has the port 53 check', /container name resolution can start \(port 53\)/.test(sys), /port 53/.test(sys) ? 'listed' : 'not listed');
    note('no CNI plugin checks', !/epair plugin/.test(sys) && !/dnsname plugin/.test(sys), /epair plugin|dnsname plugin/.test(sys) ? 'a CNI check is listed' : 'none listed');
  } catch (e) {
    await page.screenshot({ path: '/out/netavark-fail.png', fullPage: true }).catch(() => {});
    note('run', false, e.message.split('\n')[0]);
  }
  await b.close();
})();
