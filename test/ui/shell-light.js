// The Shell tab in light mode reads as the card it sits in: its wrapper has
// no background of its own, and xterm painted that transparent as black
// under the light theme's dark text -- the prompt all but invisible.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-shl';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/' + NAME)).json();
// Relative luminance of a computed rgb()/rgba() colour, 0 (black) to 1.
const lum = (c) => {
  const [r, g, b] = (c.match(/[\d.]+/g) || []).slice(0, 3).map((v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const ctx = await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }));
  await ctx.addInitScript(() => { try { localStorage.setItem('fjord-theme', 'light'); } catch {} });
  const page = await ctx.newPage();
  page.setDefaultTimeout(30000);
  try {
    if (process.env.STEP === 'install') {
      await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
      await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
      await sleep(800);
      await page.getByRole('button', { name: /^Install/ }).first().click();
      const install = page.getByRole('button', { name: 'Install', exact: true });
      await install.waitFor();
      await page.getByLabel('Name', { exact: true }).fill(NAME);
      // Options open, then bridge, then the port: on a LAN network the form
      // has no port field to fill.
      if (!(await page.getByText('Networking', { exact: true }).first().isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
      const netSel = page.locator('table select').filter({ has: page.locator('option[value="bridge"]') });
      if ((await netSel.count()) === 0) await page.getByRole('button', { name: /\+ interface/ }).first().click();
      await netSel.first().selectOption('bridge');
      await page.locator('#WEB_PORT').fill('3124');
      await install.click();
      let d;
      for (let i = 0; i < 80; i++) { d = await detail(page).catch(() => ({})); if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break; await sleep(3000); }
      note('installed', d?.status?.state === 'running', `state ${d?.status?.state}`);
    } else {
      await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/' + NAME);
      await page.getByRole('button', { name: /Shell/ }).first().click();
      await page.locator('.xterm').first().waitFor();
      for (let i = 0; i < 20 && !/connected/.test(await page.locator('body').innerText()); i++) await sleep(500);
      await page.keyboard.type('echo shell-light-check\n');
      await sleep(1500);
      // What is on screen, read off the screenshot's pixels: the layers are
      // siblings (.xterm-viewport is black in xterm.css, xterm 6 paints the
      // theme on a scroll layer beside it), so no computed style tells which
      // one shows. Sampled from the empty lower half, away from any text.
      const shot = await page.locator('.xterm').first().screenshot({ path: '/out/shell-light.png' });
      const px = await page.evaluate(async (b64) => {
        const img = new Image(); img.src = 'data:image/png;base64,' + b64; await img.decode();
        const c = document.createElement('canvas'); c.width = img.width; c.height = img.height;
        const g = c.getContext('2d'); g.drawImage(img, 0, 0);
        const d = g.getImageData(Math.floor(img.width / 2), Math.floor(img.height * 0.75), 1, 1).data;
        return `rgb(${d[0]}, ${d[1]}, ${d[2]})`;
      }, shot.toString('base64'));
      const bg = px;
      const fg = await page.locator('.xterm-rows').first().evaluate((e) => getComputedStyle(e).color);
      note('light mode: the shell is light, not black', lum(bg) > 0.6, `background ${bg}`);
      const ratio = (Math.max(lum(bg), lum(fg)) + 0.05) / (Math.min(lum(bg), lum(fg)) + 0.05);
      note('light mode: its text is readable (contrast 4.5:1 or better)', ratio >= 4.5, `text ${fg} on ${bg}, ${ratio.toFixed(1)}:1`);
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/shell-light-error-' + (process.env.STEP || 'check') + '.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
