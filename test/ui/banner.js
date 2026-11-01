// The color startup banner of daemonless images, in the Logs tab: a stack
// installed from the store on each engine prints the full-color logo, drawn
// in block rows with no gap between them. The variable reaches the stack
// through its .env, so a value the user writes there (plain) is kept.
// needs: freebsd appjail
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const APP = 'zensical';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page, name) => (await page.request.get(BASE + '/api/stacks/' + name)).json();
async function install(page, name, engine, port) {
  await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
  await page.getByPlaceholder(/Search apps/).fill(APP);
  await sleep(800);
  await page.getByRole('button', { name: /^Install/ }).first().click();
  const install = page.getByRole('button', { name: 'Install', exact: true });
  await install.waitFor();
  await page.getByLabel('Name', { exact: true }).fill(name);
  if (!(await page.locator('#WEB_PORT').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
  await page.locator('#WEB_PORT').fill(port);
  if (engine === 'appjail') {
    const sel = page.locator('select').filter({ hasText: 'appjail' }).first();
    if (!(await sel.isVisible().catch(() => false))) await page.getByText('Advanced', { exact: true }).click();
    await sel.selectOption('appjail');
    await sleep(1500);
  }
  if (await install.isDisabled()) {
    const why = (await page.locator('.text-fjord-warning, .text-fjord-danger').allInnerTexts()).join(' | ');
    note(`${name}: Install offered as-is`, false, `Install disabled: "${why.trim()}"`);
    return false;
  }
  await install.click();
  let d;
  for (let i = 0; i < 80; i++) { d = await detail(page, name).catch(() => ({})); if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break; await sleep(3000); }
  const f = (d?.state || {}).last_failure;
  note(`${name}: installed on ${engine}, running`, d?.status?.state === 'running' && !f, `state ${d?.status?.state}${f ? ', failed: ' + f.message : ''}`);
  const env = d?.env || '';
  note(`${name}: .env asks for the color banner`, /^DAEMONLESS_BANNER=color$/m.test(env), (env.match(/^DAEMONLESS_BANNER=.*$/m) || ['no DAEMONLESS_BANNER line'])[0]);
  const ref = engine === 'appjail' ? /DAEMONLESS_BANNER: !ENV '\$\{DAEMONLESS_BANNER\}'/.test(d?.director || '') : /DAEMONLESS_BANNER=\$\{DAEMONLESS_BANNER\}/.test(d?.compose || '');
  note(`${name}: the service reads it`, ref, ref ? `named in the ${engine === 'appjail' ? 'director' : 'compose'}` : `not named in the ${engine === 'appjail' ? 'director' : 'compose'}`);
  return d?.status?.state === 'running';
}
// logo reads the Logs tab: the block-art rows, how many are in color, and
// the largest gap between two consecutive rows.
async function logo(page, name, shot) {
  await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/' + name);
  await sleep(1000);
  await page.getByRole('button', { name: 'Logs', exact: true }).click();
  let got = { art: 0, colored: 0, maxGap: 0, lines: 0 };
  for (let i = 0; i < 20; i++) {
    got = await page.evaluate(() => {
      const rows = [...document.querySelectorAll('.line.art')];
      const colored = rows.filter((r) => [...r.querySelectorAll('span')].some((s) => /color/.test(s.getAttribute('style') || ''))).length;
      let maxGap = 0;
      // Between rows that follow each other: a jail's log can hold two
      // banners, with text between.
      for (let i = 1; i < rows.length; i++) {
        if (rows[i - 1].nextElementSibling !== rows[i]) continue;
        const a = rows[i - 1].getBoundingClientRect(), b = rows[i].getBoundingClientRect();
        maxGap = Math.max(maxGap, b.top - a.bottom);
      }
      return { art: rows.length, colored, maxGap: Math.round(maxGap * 100) / 100, lines: document.querySelectorAll('.line').length };
    });
    if (got.lines > 0 && (got.art > 0 || i > 6)) break;
    await sleep(1500);
  }
  await page.screenshot({ path: `/out/banner-${shot}.png`, fullPage: true });
  return got;
}
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(30000);
  try {
    for (const [name, engine, port] of [['t-ban-podman', 'podman', '3130'], ['t-ban-appjail', 'appjail', '3131']]) {
      if (!(await install(page, name, engine, port))) continue;
      const got = await logo(page, name, engine);
      note(`${name}: the Logs tab shows the color logo`, got.art > 2 && got.colored === got.art, `${got.art} block-art rows, ${got.colored} in color, ${got.lines} lines`);
      note(`${name}: no gap between the logo's rows`, got.art > 1 && got.maxGap <= 0.5, `largest gap ${got.maxGap}px`);
    }
    // The user's own value: plain, written on the .env tab and applied.
    const name = 't-ban-podman';
    await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/' + name);
    await sleep(1500);
    await page.getByRole('button', { name: '.env', exact: true }).click();
    await page.locator('.cm-content').click();
    await page.keyboard.press('Control+A');
    await page.keyboard.insertText((await detail(page, name)).env.replace('DAEMONLESS_BANNER=color', 'DAEMONLESS_BANNER=plain'));
    await page.getByRole('button', { name: 'Save', exact: true }).first().click();
    await sleep(3000);
    await page.getByRole('button', { name: 'Apply Changes', exact: true }).click();
    let d;
    for (let i = 0; i < 80; i++) { d = await detail(page, name); if (!d.busy && d.status?.state === 'running') break; await sleep(3000); }
    await sleep(5000);
    note(`${name}: applied with the user's value`, d?.status?.state === 'running' && /^DAEMONLESS_BANNER=plain$/m.test(d?.env || ''), `state ${d?.status?.state}, ${(d?.env || '').match(/^DAEMONLESS_BANNER=.*$/m)?.[0]}`);
    const got = await logo(page, name, 'plain');
    note(`${name}: plain prints no logo`, got.lines > 0 && got.art === 0, `${got.art} block-art rows, ${got.lines} lines`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/banner-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
