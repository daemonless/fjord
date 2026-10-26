// The store with every catalog switched off: says so, and its one button
// opens Settings, Catalogs (not a Fetch button that fetches nothing).
// store-off.sh turns the catalogs off before and back on after.
const { chromium } = require('playwright-core');
const { signedIn } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }))).newPage();
  await page.goto(BASE + '/?t=' + Date.now() + '#/store', { waitUntil: 'load' });
  await sleep(2500);
  await page.screenshot({ path: '/out/store-off-1.png', fullPage: true });
  const body = await page.locator('body').innerText();
  note('the store says no catalog is turned on', /No catalogs are turned on/.test(body), (body.match(/No catalogs[^\n]*/) || ['not said'])[0]);
  note('no Fetch button', !/Fetch Catalogs/.test(body), /Fetch Catalogs/.test(body) ? 'Fetch Catalogs still offered' : 'none');
  const open = page.getByRole('button', { name: 'Open Catalogs settings' });
  const offered = await open.isVisible().catch(() => false);
  note('a button to Settings is offered', offered, offered ? 'Open Catalogs settings' : 'missing');
  if (offered) {
    await open.click();
    await sleep(1500);
    await page.screenshot({ path: '/out/store-off-2.png', fullPage: true });
    const there = await page.getByRole('heading', { name: 'Catalogs' }).isVisible().catch(() => false);
    note('it lands on Settings, Catalogs', there && /#\/settings\/catalogs$/.test(page.url()), `${page.url().replace(/.*#/, '#')}${there ? '' : ', no Catalogs heading'}`);
  }
  await b.close();
})();
