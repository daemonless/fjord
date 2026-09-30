// A version with no build for this host: the wizard and Change Version say so
// and hold the button; the API refuses the retag; and a compose edited to such
// a tag is pulled, judged and not applied -- the running container stays.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-arch';
const BAD = 'latest-aarch64'; // an arm64-only manifest; netlab is amd64
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/' + NAME)).json();
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(20000);
  try {
    // 1. The wizard.
    await page.goto(BASE + '/', { waitUntil: 'networkidle' });
    await page.getByText('App Store', { exact: true }).first().click();
    await page.getByPlaceholder(/Search apps/).fill('openspeedtest');
    await page.getByRole('button', { name: /^Install/ }).first().click();
    const install = page.getByRole('button', { name: 'Install', exact: true });
    await install.waitFor();
    await page.getByLabel('Name').fill(NAME);
    if (!(await page.locator('#type-openspeedtest').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
    await page.locator('#WEB_PORT').fill('3112');
    // On the built-in bridge, whatever the host's default network is.
    const netSel = page.locator('table select').filter({ has: page.locator('option[value="bridge"]') });
    if ((await netSel.count()) === 0) await page.getByRole('button', { name: /\+ interface/ }).first().click();
    await netSel.first().selectOption('bridge');
    await page.getByText('Advanced', { exact: true }).click(); // the custom tag lives there
    const custom = page.getByPlaceholder(/Custom tag/);
    await custom.fill(BAD);
    const warn = page.getByText(/has no build for this host/).first();
    await warn.waitFor({ timeout: 30000 }).catch(() => {});
    const warnText = await warn.innerText().catch(() => '');
    await page.screenshot({ path: '/out/arch-wizard.png', fullPage: true });
    note('wizard: says the tag has no build here', /:latest-aarch64 has no build for this host \(freebsd\/amd64\) — it is built for freebsd\/arm64/.test(warnText), `"${warnText}"`);
    note('wizard: Install held, with the reason', (await install.isDisabled()) && (await install.getAttribute('title')) === warnText, `disabled ${await install.isDisabled()}, title "${await install.getAttribute('title')}"`);
    await custom.fill('');
    await page.getByText(/has no build for this host/).first().waitFor({ state: 'hidden', timeout: 10000 }).catch(() => {});
    note('wizard: clearing the tag frees Install', !(await install.isDisabled()), `disabled ${await install.isDisabled()}`);
    await install.click();
    let d;
    for (let i = 0; i < 60; i++) { d = await detail(page).catch(() => ({})); if (d?.status?.state === 'running' && !d.busy) break; await sleep(3000); }
    note('install on :latest', d?.status?.state === 'running', `state ${d?.status?.state}`);
    const before = d.status.containers[0].id;

    // 2. Change Version…
    await page.goto(BASE + '/#/stacks/' + NAME, { waitUntil: 'networkidle' });
    await sleep(1500);
    await page.getByRole('button', { name: 'More Actions' }).click();
    await page.getByRole('button', { name: /Change Version/ }).click();
    await page.getByText(/Loading published versions/).waitFor({ state: 'hidden', timeout: 30000 }).catch(() => {});
    await page.getByPlaceholder(/Custom tag/).fill(BAD);
    const cvWarn = page.getByText(/has no build for this host/).first();
    await cvWarn.waitFor({ timeout: 30000 }).catch(() => {});
    const cvText = await cvWarn.innerText().catch(() => '');
    const redeploy = page.getByRole('button', { name: /Change & Redeploy/ });
    await page.screenshot({ path: '/out/arch-change-version.png', fullPage: true });
    note('Change Version: says the tag has no build here', /no build for this host/.test(cvText), `"${cvText}"`);
    note('Change Version: button held', await redeploy.isDisabled(), `disabled ${await redeploy.isDisabled()}`);
    await page.keyboard.press('Escape');

    // 3. The API behind it refuses outright.
    const r = await page.request.post(BASE + `/api/stacks/${NAME}/set-tag`, { headers: { Origin: BASE, 'Content-Type': 'application/json' }, data: { tag: BAD, pin: false } });
    const rt = (await r.text()).trim();
    note('set-tag refused with the reason', r.status() === 409 && /no build for this host/.test(rt), `${r.status()} "${rt}"`);

    // 4. A compose edited by hand: Save, Apply -- judged after the pull, before the teardown.
    await page.getByRole('button', { name: 'compose.yaml', exact: true }).click();
    await page.locator('.cm-content').click();
    await page.keyboard.press('Control+A');
    await page.keyboard.insertText((await detail(page)).compose.replace('openspeedtest:latest', 'openspeedtest:' + BAD));
    await page.getByRole('button', { name: 'Save', exact: true }).first().click();
    await sleep(2500);
    let applied = '';
    page.on('response', async (x) => { if (new RegExp(`/api/stacks/${NAME}/up$`).test(x.url())) applied = await x.text().catch(() => ''); });
    await page.getByRole('button', { name: 'Apply Changes', exact: true }).click();
    for (let i = 0; i < 80; i++) { d = await detail(page); if (!d.busy) break; await sleep(3000); }
    for (let i = 0; i < 40 && !applied; i++) await sleep(500);
    await sleep(1500);
    await page.screenshot({ path: '/out/arch-apply.png', fullPage: true });
    console.log('--- Apply output:\n' + applied.split('\n').filter((l) => /\$ podman|\[error\]|\[warn\]|WARNING/.test(l)).join('\n') + '\n---');
    note('Apply: pulled the tag first', /\$ podman pull ghcr.io\/daemonless\/openspeedtest:latest-aarch64/.test(applied), 'podman pull line');
    note('Apply: judged by the image, said as an [error]', /\[error\] openspeedtest: ghcr.io\/daemonless\/openspeedtest:latest-aarch64 is built for freebsd\/arm64; this host is freebsd\/amd64/.test(applied) && /\[error\] not started: pick a version built for freebsd\/amd64/.test(applied), 'error lines');
    d = await detail(page);
    note('Apply: the running container was left alone', d.status?.state === 'running' && d.status.containers[0]?.id === before, `${d.status?.state}, ${before.slice(0, 12)} -> ${(d.status?.containers?.[0]?.id || '').slice(0, 12)}`);
    note('Apply: still pending, banner still up', (d.state?.pending_services || []).includes('openspeedtest') && (await page.getByText(/still use the old configuration/).isVisible()), `pending ${(d.state?.pending_services || []).join(',')}`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/arch-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
