// An unattended job that keeps failing leaves one counted notice on its app:
// a mark in the sidebar, a banner on the app with the count and the dates,
// and a line in Settings -> Maintenance. Dismiss hides the mark and the
// banner and keeps the Maintenance line, marked dismissed. STEP=make saves
// the stack (t-fn, never started); failure-notices.sh writes the notice.
const { chromium } = require('playwright-core');
const { BASE, sleep, note, signedIn, openPage } = require('./session');
const H = { Origin: BASE, 'Content-Type': 'application/json' };
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }))).newPage();
  page.setDefaultTimeout(20000);
  try {
    if (process.env.STEP === 'make') {
      const r = await page.request.post(BASE + '/api/stacks/t-fn/save', { headers: H, data: { compose: 'services:\n  web:\n    image: ghcr.io/daemonless/openspeedtest:latest\n', env: '' } });
      note('t-fn saved', r.ok(), String(r.status()));
      await b.close();
      return;
    }
    const fleet = async () => ((await (await page.request.get(BASE + '/api/notices')).json()).notices || []).filter((n) => n.stack === 't-fn');
    const got = await fleet();
    note('fjordd reports the notice', got.length === 1 && got[0].kind === 'auto-update' && got[0].count === 3, JSON.stringify(got));
    await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/t-fn');
    await sleep(1500);
    const mark = page.locator('aside [data-stack-notice-mark]');
    note('sidebar: the app carries a failure mark', (await mark.count()) === 1, `${await mark.count()} mark(s)`);
    const banner = page.locator('[data-failure-notice="auto-update"]');
    const text = (await banner.innerText().catch(() => '')).replace(/\s+/g, ' ');
    note('app page: one banner, with the count and since when', (await banner.count()) === 1 && /Automatic update failed 3 times in a row, since 2026-11-24/.test(text), `"${text}"`);
    note('app page: it says why', /did not stay up after the update/.test(text), `"${text}"`);
    await page.screenshot({ path: '/out/failure-notices-banner.png', fullPage: true });
    await openPage(page, BASE + '/?t=' + Date.now() + '#/settings/maintenance');
    await sleep(2000);
    let m = (await page.locator('main, body').last().innerText()).replace(/\s+/g, ' ');
    note('Maintenance: the summary counts it', /1 app has failing automatic updates/.test(m), (m.match(/\d+ apps? ha(s|ve) failing[^.]*/) || ['no summary line'])[0]);
    await page.screenshot({ path: '/out/failure-notices-maintenance.png', fullPage: true });
    await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/t-fn');
    await sleep(1500);
    await banner.getByRole('button', { name: 'Dismiss' }).click();
    await banner.waitFor({ state: 'detached', timeout: 5000 }).catch(() => {});
    await sleep(1000);
    note('Dismiss: the banner goes', (await banner.count()) === 0, `${await banner.count()} banner(s)`);
    note('Dismiss: no other failure banner takes its place', (await page.getByText(/Update failed at/).count()) === 0, `${await page.getByText(/Update failed at/).count()} "Update failed at" line(s)`);
    note('Dismiss: the sidebar mark goes', (await mark.count()) === 0, `${await mark.count()} mark(s)`);
    await page.reload(); await sleep(2500);
    note('still dismissed after a reload', (await banner.count()) === 0 && (await mark.count()) === 0, `${await banner.count()} banner(s), ${await mark.count()} mark(s)`);
    const after = await fleet();
    note('fjordd keeps it, dismissed, with its count', after.length === 1 && after[0].dismissed === true && after[0].count === 3, JSON.stringify(after));
    await openPage(page, BASE + '/?t=' + Date.now() + '#/settings/maintenance');
    await sleep(2000);
    m = (await page.locator('main, body').last().innerText()).replace(/\s+/g, ' ');
    note('Maintenance: still listed, marked dismissed', /\(dismissed\)/.test(m), (m.match(/.{0,60}\(dismissed\)/) || ['no "(dismissed)" on the page'])[0]);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/failure-notices-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
