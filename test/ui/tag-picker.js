// The install wizard's version menu: catalog variants only (no leftover
// registry tags or aliases), each "tag · release", grouped by release line
// when a line has several builds, the recommended one picked, and Updates
// as "Keep up to date (now …)" or "Stay on …". An app with one variant has
// no Version menu. Nothing is installed.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);

async function openWizard(page, app) {
  await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
  await page.getByPlaceholder(/Search apps/).fill(app);
  await sleep(800);
  await page.getByRole('button', { name: /^Install/ }).first().click();
  await page.getByRole('button', { name: 'Install', exact: true }).waitFor();
  if (!(await page.locator('#ver').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
  await page.locator('#ver').waitFor();
  // The registry's list arrives after the wizard opens.
  for (let i = 0; i < 20 && !/now /.test(await page.locator('#ver option').first().innerText()); i++) await sleep(500);
}

(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(30000);
  try {
    await openWizard(page, 'forgejo');
    const opts = await page.locator('#train option').allInnerTexts();
    const groups = await page.locator('#train optgroup').evaluateAll((g) => g.map((x) => x.label));
    const picked = await page.locator('#train').inputValue();
    const desc = await page.locator('[data-variant-desc]').innerText().catch(() => '');
    const upd = await page.locator('#ver option').allInnerTexts();
    await page.screenshot({ path: '/out/tag-picker-forgejo.png', fullPage: true });
    const ids = opts.map((o) => o.split(' ')[0]);
    note('no leftover registry tags or aliases', !ids.some((i) => /^(11|14-pkg|15-14-pkg-latest|lts.*|pkg|pkg-latest)$/.test(i)), JSON.stringify(ids));
    note('each row is tag · release', opts.length > 1 && opts.every((o) => /^\S+ · \S+/.test(o)), JSON.stringify(opts.slice(0, 3)));
    note('grouped by release line, newest first', groups.length > 1 && groups.every((g, i) => i === 0 || parseFloat(g.replace('Release ', '')) < parseFloat(groups[i - 1].replace('Release ', '')) || g === 'Other'), JSON.stringify(groups));
    note('the recommended one is picked, its description shown', opts.some((o) => o.startsWith(picked + ' ') && /recommended/.test(o)) && desc.length > 0, `${picked}: "${desc}"`);
    note('Updates: keep up to date or stay on a release', /^Keep up to date \(now \S+\)$/.test(upd[0]) && upd.slice(1).every((u) => /^Stay on \S+$/.test(u)) && upd.length > 1, JSON.stringify(upd.slice(0, 3)));
    await page.getByRole('button', { name: 'Cancel', exact: true }).first().click().catch(() => {});

    // An app with one variant: nothing to pick, only Updates.
    await openWizard(page, 'openspeedtest');
    const one = await page.locator('#train').count();
    note('one variant: no Version menu', one === 0, `#train shown ${one}`);
    await page.getByRole('button', { name: 'Cancel', exact: true }).first().click().catch(() => {});

    // The store's Builds chips use the same words.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
    await page.getByPlaceholder(/Search apps/).fill('forgejo');
    await sleep(800);
    await page.getByText('Forgejo', { exact: true }).first().click();
    const chips = await page.getByText('Builds', { exact: true }).locator('xpath=..').locator('span').allInnerTexts();
    // The store asks no registry: a release only where the catalog knows one.
    note('store Builds read by tag, not the long description', chips.length > 1 && chips.every((c) => /^\S+( · \S+)?( +\(recommended\))?$/.test(c) && !/Built from/.test(c)), JSON.stringify(chips.slice(0, 3)));
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
  }
  await b.close();
})();
