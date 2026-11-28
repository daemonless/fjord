// The System page is three tabs: Checks, Cleanup, History. Each has its own
// address, an old #/system link lands on Checks without a second history
// entry, and an unknown tab falls back to Checks. Read-only.
const { chromium } = require('playwright-core');
const { BASE, sleep, note, signedIn, openPage } = require('./session');
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }))).newPage();
  page.setDefaultTimeout(20000);
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const hash = () => page.url().split('#')[1] || '';
  try {
    await openPage(page, BASE + '/#/store');
    await page.evaluate(() => { location.hash = '#/system'; });
    await sleep(2500);
    note('#/system lands on Checks', hash() === '/system/checks', '#' + hash());
    const tabs = (await page.getByRole('tab').allInnerTexts()).map((t) => t.replace(/\s+/g, ' ').trim());
    note('three tabs, in order', tabs.length === 3 && /^Checks/.test(tabs[0]) && /^Cleanup/.test(tabs[1]) && tabs[2] === 'History', JSON.stringify(tabs));
    const selected = async () => (await page.locator('[role=tab][aria-selected=true]').allInnerTexts()).map((t) => t.replace(/\s+/g, ' ').trim()).join('|');
    note('Checks is the selected tab', /^Checks/.test(await selected()), await selected());
    await page.getByRole('tab', { name: /^Cleanup/ }).click();
    await sleep(2000);
    let body = await page.locator('body').innerText();
    note('Cleanup: its own address and content', hash() === '/system/cleanup' && /Storage cleanup/i.test(body) && /Left-over app data/i.test(body), '#' + hash());
    await page.getByRole('tab', { name: 'History', exact: true }).click();
    await sleep(2000);
    body = await page.locator('body').innerText();
    note('History: its own address and content', hash() === '/system/history' && /What happened across all apps/.test(body), '#' + hash());
    await page.screenshot({ path: '/out/system-tabs-history.png', fullPage: true });
    // Back walks the tabs one press each, then leaves the page: the #/system
    // rewrite must not have left an entry of its own.
    await page.goBack(); await sleep(600);
    const b1 = hash();
    await page.goBack(); await sleep(600);
    const b2 = hash();
    await page.goBack(); await sleep(600);
    const b3 = hash();
    note('Back: History -> Cleanup -> Checks -> where you came from', b1 === '/system/cleanup' && b2 === '/system/checks' && b3 === '/store', `${b1}, ${b2}, ${b3}`);
    // A deep link opens its tab without waiting on the checks.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/system/history');
    await page.getByText(/What happened across all apps/).waitFor({ timeout: 5000 }).catch(() => {});
    note('deep link to History opens History', /What happened across all apps/.test(await page.locator('body').innerText()) && /^History$/.test(await selected()), `selected "${await selected()}"`);
    await openPage(page, BASE + '/?t=' + Date.now() + '#/system/nonsense');
    await sleep(2000);
    note('an unknown tab falls back to Checks', /^Checks/.test(await selected()), `#${hash()}, selected "${await selected()}"`);
    note('no exception on the page', !errors.length, errors[0] || 'none');
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/system-tabs-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
