// "Don't show again" on a banner: it goes for good, in this browser and after
// a reload, the System page still lists the check and offers the banner
// back, and taking that offer brings it back. Uses whichever banner the host
// shows first; a host with nothing to say has nothing to test.
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);

// ids are the checks with a banner on the page, folded ones included.
async function ids(page) {
  await sleep(1500);
  const more = page.getByRole('button', { name: /^\+\d+ more$/ });
  if (await more.isVisible().catch(() => false)) await more.click();
  await sleep(300);
  return page.locator('[data-mute]').evaluateAll((els) => els.map((e) => e.getAttribute('data-mute')));
}
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(30000);
  let id = '';
  try {
    await openPage(page, BASE + '/?t=' + Date.now());
    const before = await ids(page);
    if (!before.length) {
      console.log('SKIP mute: this host shows no banner');
      await b.close();
      return;
    }
    id = before[0];
    await page.screenshot({ path: '/out/mute-before.png', fullPage: true });
    await page.locator(`[data-mute="${id}"]`).click();
    await page.getByText(/Turned off/).first().waitFor({ timeout: 10000 }).catch(() => {});
    const after = await ids(page);
    note(`"Don't show again" takes the ${id} banner away`, !after.includes(id), `banners now: ${after.join(', ') || 'none'}`);
    note('the other banners stay', before.filter((x) => x !== id).every((x) => after.includes(x)), `${before.length - 1} before, ${after.length} now`);
    await openPage(page, BASE + '/?t=' + Date.now());
    note('still gone after a reload', !(await ids(page)).includes(id), 'checked');

    await openPage(page, BASE + '/?t=' + Date.now() + '#/system');
    const row = page.locator(`[data-check="${id}"]`);
    await row.waitFor();
    const back = page.locator(`[data-unmute="${id}"]`);
    note('System still lists the check, with its banner on offer', await back.isVisible().catch(() => false), (await row.innerText()).replace(/\s+/g, ' ').slice(0, 140));
    await page.screenshot({ path: '/out/mute-system.png', fullPage: true });
    await back.click();
    await sleep(1500);
    note('the offer goes once taken', !(await back.isVisible().catch(() => false)), 'checked');
    note('and the banner is back', (await ids(page)).includes(id), 'checked');
    id = '';
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/mute-error.png', fullPage: true }).catch(() => {});
  }
  // Leave the host as it was found.
  if (id) await page.request.post(BASE + '/api/conditions/mute', { data: { id, muted: false }, headers: { Origin: BASE } }).catch(() => {});
  await b.close();
})();
