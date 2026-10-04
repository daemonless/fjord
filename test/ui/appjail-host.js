// AppJail on the host's stack, through fjord's pages: the Networks page offers
// host as appjail's default, and an install with nothing else changed lands
// there. appjail-host.sh checks the jail and the app from off-host.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const director = async (page) => {
  for (let i = 0; i < 60; i++) {
    const d = await (await page.request.get(BASE + '/api/stacks/t-ajhost')).json().catch(() => ({}));
    if (!d?.busy && d?.status?.state === 'running') return d.director || '';
    await sleep(3000);
  }
  return '';
};
const dirSummary = (d) => ['alias', 'ip4_inherit', 'virtualnet', 'nat', 'expose'].filter((k) => d.includes(k)).join(' ') || 'nothing';
async function answers(page, url, secs = 180) {
  for (let i = 0; i < secs / 5; i++) {
    try { if ((await page.request.get(url, { timeout: 5000 })).status() === 200) return 200; } catch {}
    await sleep(5000);
  }
  return 0;
}
// Services tab: set the Type and Save. networks means one row, on bridge.
async function setType(page, type) {
  await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/t-ajhost', { waitUntil: 'networkidle' });
  await sleep(2000);
  const sel = page.locator('#type-openspeedtest');
  await sel.waitFor();
  await sel.selectOption(type);
  await sleep(300);
  if (type === 'networks') await page.locator('table select').first().selectOption('bridge');
  await page.screenshot({ path: `/out/appjail-host-type-${type}.png`, fullPage: true });
  await page.getByRole('button', { name: 'Save', exact: true }).first().click();
  await sleep(2500);
  const apply = page.getByRole('button', { name: 'Apply Changes', exact: true });
  if (await apply.count()) await apply.first().click().catch(() => {});
  await sleep(4000);
}
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(30000);
  const errs = [];
  page.on('pageerror', (e) => errs.push(e.message));
  try {
    await page.goto(BASE + '/?t=' + Date.now() + '#/networks', { waitUntil: 'networkidle' });
    await sleep(1000);
    const sel = page.locator('label', { hasText: /^\s*appjail/ }).locator('select').first();
    const opts = await sel.locator('option').allTextContents();
    note('Networks: appjail can default to host', opts.includes('host'), opts.join(', '));
    await sel.selectOption('host');
    await sleep(1000);
    const saved = await (await page.request.get(BASE + '/api/settings/network')).json();
    note('Networks: host saved as appjail default', saved?.forEngine?.appjail === 'host', JSON.stringify(saved?.forEngine));
    await page.screenshot({ path: '/out/appjail-host-networks.png', fullPage: true });

    await page.goto(BASE + '/?t=' + Date.now() + '#/store', { waitUntil: 'networkidle' });
    await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
    await sleep(800);
    await page.getByRole('button', { name: /^Install/ }).first().click();
    const install = page.getByRole('button', { name: 'Install', exact: true });
    await install.waitFor();
    await page.getByLabel('Name').fill('t-ajhost');
    await page.getByRole('button', { name: /^Options/ }).first().click();
    const eng = page.locator('select').filter({ hasText: 'appjail' }).first();
    if (!(await eng.isVisible().catch(() => false))) await page.getByText('Advanced', { exact: true }).click();
    await eng.selectOption('appjail');
    await sleep(1500);
    await page.screenshot({ path: '/out/appjail-host-wizard.png', fullPage: true });
    if (await install.isDisabled()) {
      const why = (await page.locator('.text-fjord-warning, .text-fjord-danger').allInnerTexts()).join(' | ');
      note('wizard: Install offered on host', false, `Install disabled: "${why.trim()}"`);
      return;
    }
    await install.click();
    let d;
    for (let i = 0; i < 80; i++) { d = await (await page.request.get(BASE + '/api/stacks/t-ajhost')).json().catch(() => ({})); if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break; await sleep(3000); }
    const f = (d?.state || {}).last_failure;
    note('installed on host, running', d?.status?.state === 'running' && !f, `state ${d?.status?.state}${f ? ', failed: ' + f.message : ''}`);
    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/t-ajhost', { waitUntil: 'networkidle' });
    await sleep(1500);
    await page.screenshot({ path: '/out/appjail-host-stack.png', fullPage: true });
    note('installed: answers on this host :3000', (await answers(page, 'http://127.0.0.1:3000/')) === 200, '127.0.0.1:3000');

    // The Services tab, both ways: what is saved has to reach the director,
    // which is all appjail reads.
    await setType(page, 'networks');
    let dir = await director(page);
    note('Services: bridge saved into the director', /virtualnet/.test(dir) && !/ip4_inherit/.test(dir), dirSummary(dir));
    await setType(page, 'host');
    dir = await director(page);
    note('Services: host saved into the director', /ip4_inherit/.test(dir) && /alias/.test(dir) && !/virtualnet/.test(dir), dirSummary(dir));
    note('Services: on host again, answers on this host :3000', (await answers(page, 'http://127.0.0.1:3000/')) === 200, '127.0.0.1:3000');
    // Ends on bridge: appjail-host.sh checks the jail left the host's stack
    // and the published port answers from off-host.
    await setType(page, 'networks');
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/appjail-host-error.png', fullPage: true }).catch(() => {});
  } finally {
    note('no page errors', errs.length === 0, errs.length ? errs.join(' | ') : 'none');
    await b.close();
  }
})();
