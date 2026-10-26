// Keyboard shortcuts: ? opens the overlay (every row from the table), Esc
// closes it, g a goes to the store, / focuses the search there, r
// refreshes, and none of it fires while typing in a field.
const { chromium } = require('playwright-core');
const { signedIn } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }))).newPage();
  await page.goto(BASE + '/?t=' + Date.now() + '#/stacks', { waitUntil: 'load' });
  await sleep(2000);
  await page.keyboard.press('?');
  const dlg = page.getByRole('dialog', { name: 'Keyboard shortcuts' });
  const open = await dlg.isVisible().catch(() => false);
  note('? opens the shortcuts', open, open ? 'shown' : 'not shown');
  if (open) {
    const text = await dlg.innerText();
    const rows = ['Go to Stacks', 'Go to the App Store', 'Go to Networks', 'Refresh', 'Find in the log view'];
    const missing = rows.filter((r) => !text.includes(r));
    note('the overlay lists the shortcuts', missing.length === 0, missing.length ? `missing: ${missing.join(', ')}` : `${rows.length} checked`);
    note('the overlay says which fjord', /fjord \d/.test(text), (text.match(/fjord [^\n]*/) || ['no version'])[0]);
    await page.screenshot({ path: '/out/shortcuts-1.png', fullPage: true });
    await page.keyboard.press('Escape');
    await sleep(300);
    note('Esc closes it', !(await dlg.isVisible().catch(() => false)), 'closed');
  }
  await page.keyboard.press('g');
  await page.keyboard.press('a');
  await sleep(1200);
  note('g a goes to the store', /#\/store$/.test(page.url()), page.url().replace(/.*#/, '#'));
  await page.keyboard.press('/');
  await sleep(300);
  const focused = await page.evaluate(() => document.activeElement?.id || document.activeElement?.tagName || '');
  note('/ focuses the store search', focused === 'app-search', focused || 'nothing focused');
  // Typing in the field is not a shortcut: "g s" stays text.
  await page.keyboard.type('g s');
  await sleep(800);
  note('keys typed in a field are text', /#\/store$/.test(page.url()) && (await page.locator('#app-search').inputValue()) === 'g s', `url ${page.url().replace(/.*#/, '#')}, field "${await page.locator('#app-search').inputValue()}"`);
  await page.locator('#app-search').fill('');
  await page.keyboard.press('Escape');
  await page.locator('body').click({ position: { x: 700, y: 900 } });
  await page.keyboard.press('g');
  await page.keyboard.press('s');
  await sleep(1200);
  note('g s goes back to Stacks', /#\/stacks$/.test(page.url()), page.url().replace(/.*#/, '#'));
  await page.keyboard.press('r');
  const t = page.getByRole('status').filter({ hasText: 'Refreshed' });
  note('r refreshes', await t.waitFor({ timeout: 5000 }).then(() => true).catch(() => false), 'toast');
  await b.close();
})();
