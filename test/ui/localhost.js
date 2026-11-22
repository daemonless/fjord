// fjord opened in a browser on the host itself (#295): an app's Open link
// goes to localhost, which only loads once pf redirects the host's own
// connections. Whatever the banner offers is pressed, then the link is
// followed. PHASE=again is the same host after a reboot: no banner, and the
// link still loads.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-local';
const AGAIN = process.env.PHASE === 'again';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const LOCAL = /Apps do not open at localhost/;

// banners are the pf and localhost conditions on the page, the folded ones
// ("+2 more") included.
const OURS = /Podman's pf firewall|localhost/;
async function banners(page) {
  await sleep(1500);
  const more = page.getByRole('button', { name: /^\+\d+ more$/ });
  if (await more.isVisible().catch(() => false)) await more.click();
  await sleep(300);
  return (await page.locator('.font-semibold.text-fjord-fg').allInnerTexts()).filter((t) => OURS.test(t));
}
// loads follows the link the way a click does and says what came back.
async function loads(ctx, url) {
  const p = await ctx.newPage();
  try {
    const r = await p.goto(url, { timeout: 12000, waitUntil: 'domcontentloaded' });
    return { ok: !!r && r.status() === 200, what: `HTTP ${r && r.status()}, title "${await p.title()}"` };
  } catch (e) {
    return { ok: false, what: e.message.split('\n')[0] };
  } finally {
    await p.close();
  }
}
async function install(page) {
  await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
  await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
  await sleep(800);
  await page.getByRole('button', { name: /^Install/ }).first().click();
  const go = page.getByRole('button', { name: 'Install', exact: true });
  await go.waitFor();
  await page.getByLabel('Name', { exact: true }).fill(NAME);
  if (!(await page.locator('#WEB_PORT').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
  await page.locator('#WEB_PORT').fill('3122');
  await go.click();
  let d;
  for (let i = 0; i < 80; i++) { d = await (await page.request.get(BASE + '/api/stacks/' + NAME)).json().catch(() => ({})); if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break; await sleep(3000); }
  note('installed from the App Store, running', d?.status?.state === 'running', `state ${d?.status?.state}`);
}
async function openLink(page) {
  await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/' + NAME);
  await sleep(1500);
  return (await page.getByRole('link', { name: /Open/ }).first().getAttribute('href').catch(() => '')) || '';
}
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const ctx = await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }));
  const page = await ctx.newPage();
  page.setDefaultTimeout(30000);
  try {
    await openPage(page, BASE + '/?t=' + Date.now());
    const first = await banners(page);
    await page.screenshot({ path: `/out/localhost-${AGAIN ? 'again' : 'start'}.png`, fullPage: true });
    if (AGAIN) {
      note('after a reboot: no pf or localhost banner', first.length === 0, first.join(' | ') || 'none');
    } else {
      note('a banner says what is wrong', first.length > 0, first.join(' | ') || 'no banner');
      await install(page);
      const url = await openLink(page);
      note('Open link goes to this host by its loopback name', /\/\/(127\.0\.0\.1|localhost):3122/.test(url), url || 'no Open link');
      const before = await loads(ctx, url);
      note('before the fix the link does not load', !before.ok, before.what);
      // Press what the banner offers until it has nothing left to say.
      for (let i = 0; i < 3; i++) {
        const now = await banners(page);
        if (!now.length) break;
        const fix = page.locator('div.flex.items-start').filter({ hasText: now[0] }).locator('button.bg-fjord-accent').first();
        const label = (await fix.innerText().catch(() => '')).trim();
        if (!label) { note('the banner has a button', false, now.join(' | ')); break; }
        await fix.click();
        await page.getByText(/^Fixed: /).first().waitFor({ timeout: 90000 }).catch(() => {});
        note(`pressed "${label}"`, true, `banner was: ${now[0]}`);
        await sleep(2500);
      }
      await page.screenshot({ path: '/out/localhost-fixed.png', fullPage: true });
      const left = await banners(page);
      note('no pf or localhost banner is left', left.length === 0, left.join(' | ') || 'none');
      note('a localhost banner was never needed twice', left.filter((t) => LOCAL.test(t)).length === 0, 'checked');
    }
    const url = await openLink(page);
    const after = await loads(ctx, url);
    note(`Open link loads the app${AGAIN ? ' after a reboot' : ''}`, after.ok, `${url}: ${after.what}`);
    const name = await loads(ctx, url.replace('127.0.0.1', 'localhost'));
    note('and at localhost by name', name.ok, name.what);
    await page.screenshot({ path: `/out/localhost-${AGAIN ? 'again' : 'done'}-stack.png`, fullPage: true });
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/localhost-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
