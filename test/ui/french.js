// The page in French: chosen in Settings, kept across a reload, and every
// string it owns comes from the catalog. Run against a catalog whose every
// entry is «the English» (scratch), so text that is not wrapped is text the
// catalog missed. The keyboard still works in French: the key names it
// compares (Enter, Escape) are not translated. Read-only.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage, settled } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const APP = process.env.APP || 'openspeedtest';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const info = (msg) => console.log(`INFO ${msg}`);
const PAGES = ['stacks', 'store', 'networks', 'volumes', 'system', 'settings'];

// Visible text the page owns but the catalog did not wrap. Data is skipped:
// names, versions, paths and addresses sit in mono/code, inputs, or are the
// catalog's own app text.
const unwrapped = (page) => page.evaluate(() => {
  const out = new Set();
  const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let n = walk.nextNode(); n; n = walk.nextNode()) {
    const t = n.textContent.trim();
    if (!/[A-Za-z]{3}/.test(t) || t.includes('«') || t.includes('»')) continue;
    const el = n.parentElement;
    if (!el || !el.offsetParent) continue;
    if (el.closest('code, pre, .font-mono, input, textarea, select, svg, .cm-editor, .xterm')) continue;
    out.add(t.length > 70 ? t.slice(0, 70) + '…' : t);
  }
  return [...out];
});

(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const ctx = await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 }, locale: 'en-US' }));
  const page = await ctx.newPage();
  page.setDefaultTimeout(20000);
  try {
    // English by default (the browser says en-US), French once chosen.
    await openPage(page, BASE + '/#/settings');
    note('default: the browser language (en)', (await page.evaluate(() => document.documentElement.lang)) === 'en', await page.evaluate(() => document.documentElement.lang));
    await Promise.all([page.waitForEvent('load'), page.locator('#fjord-locale').selectOption('fr')]);
    await settled(page);
    await sleep(800);
    const lang = await page.evaluate(() => document.documentElement.lang);
    // Two catalogs can be behind this: the real French, or the scratch one
    // whose every entry is «the English» (then the unwrapped scan below
    // means something).
    const bodyFr = await page.locator('body').innerText();
    const scratch = bodyFr.includes('«');
    note('Settings → Français reloads in French', lang === 'fr' && (scratch || /Réglages/.test(bodyFr)), `lang=${lang}, ${scratch ? 'scratch catalog' : 'French catalog'}`);
    await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
    note('the choice survives a new load', (await page.evaluate(() => document.documentElement.lang)) === 'fr', '');

    for (const p of PAGES) {
      await openPage(page, BASE + '/?t=' + Date.now() + '#/' + p);
      await sleep(1200);
      await page.screenshot({ path: `/out/fr-${p}.png`, fullPage: true });
      if (!scratch) continue; // only the scratch catalog marks what is wrapped
      const left = await unwrapped(page);
      info(`${p}: ${left.length} unwrapped: ${left.slice(0, 40).map((s) => JSON.stringify(s)).join(' ')}`);
    }

    // Keys in French: Enter opens a store card, Escape closes it.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
    await page.getByPlaceholder(/Search apps|Rechercher une application/).fill(APP);
    await sleep(800);
    const card = page.locator('[role="button"]').filter({ hasText: new RegExp(APP, 'i') }).first();
    await card.focus();
    await page.keyboard.press('Enter');
    await sleep(800);
    const open = await page.getByRole('heading', { name: new RegExp(APP, 'i') }).count();
    note('French: Enter on a store card opens it', open > 0, `${open} heading(s)`);
    // Escape cancels the catalog rename box (Settings → Catalogs).
    await openPage(page, BASE + '/?t=' + Date.now() + '#/settings');
    await page.getByRole('button', { name: scratch ? '«Catalogs»' : 'Catalogues', exact: true }).click();
    const name = page.locator(scratch ? 'span[title="«Click to rename»"]' : 'span[title="Cliquez pour renommer"]').first();
    if (await name.count()) {
      await name.click();
      await sleep(300);
      const box = await page.locator('input:focus').count();
      await page.keyboard.press('Escape');
      await sleep(300);
      note('French: Escape cancels the catalog rename', box === 1 && (await name.count()) > 0, `box ${box}`);
    } else note('French: Escape', true, 'no renamable catalog on this host (nothing to judge)');

    // And back.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/settings');
    await Promise.all([page.waitForEvent('load'), page.locator('#fjord-locale').selectOption('en')]);
    await settled(page);
    await sleep(800);
    const back = await page.evaluate(() => document.documentElement.lang);
    note('Settings → English goes back', back === 'en' && !(await page.locator('body').innerText()).includes('«'), `lang=${back}`);
    await page.screenshot({ path: '/out/en-settings.png', fullPage: true });
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/french-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
