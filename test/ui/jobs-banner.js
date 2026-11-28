// The jobs banner: a run started from Settings -> Maintenance shows at the top
// of every page, its result stays through a reload, and Dismiss removes it for
// good. "Run now" with nothing to update is the cheapest job there is.
const { chromium } = require('playwright-core');
const { BASE, sleep, note, signedIn, openPage } = require('./session');
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }))).newPage();
  page.setDefaultTimeout(20000);
  const jobs = async () => ((await (await page.request.get(BASE + '/api/jobs')).json()).jobs || []);
  const card = page.locator('[data-jobs-banner] [data-job-card="auto-update"]');
  try {
    const au = await (await page.request.get(BASE + '/api/autoupdate')).json();
    if (au.stacksOn > 0) { console.log(`SKIP jobs banner: ${au.stacksOn} app(s) have Keep up to date on; Run now would update them`); await b.close(); return; }
    await openPage(page, BASE + '/?t=' + Date.now() + '#/settings/maintenance');
    for (const j of await jobs()) await page.request.post(BASE + `/api/jobs/${j.id}/dismiss`, { headers: { Origin: BASE } }).catch(() => {});
    await page.getByText('Check for updates now').locator('xpath=ancestor::div[.//button][1]').getByRole('button', { name: 'Run now', exact: true }).click();
    await card.waitFor({ timeout: 15000 });
    let done = false;
    for (let i = 0; i < 40 && !done; i++) { done = (await jobs()).some((j) => j.kind === 'auto-update' && j.status !== 'running'); if (!done) await sleep(500); }
    await sleep(800);
    const text = (await card.innerText()).replace(/\s+/g, ' ');
    note('the banner shows the run and its result', done && /Automatic updates/.test(text) && /nothing to do/i.test(text), `"${text}"`);
    note('a finished job offers Dismiss, not Stop', (await card.getByRole('button', { name: 'Dismiss' }).count()) === 1 && (await card.getByRole('button', { name: /Stop/ }).count()) === 0, text);
    await page.screenshot({ path: '/out/jobs-banner.png' });
    await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
    note('it shows on every page', (await card.count()) === 1, `${await card.count()} card(s) on the App Store`);
    await page.reload(); await sleep(2500);
    note('it is still there after a reload', (await card.count()) === 1, `${await card.count()} card(s)`);
    await card.getByRole('button', { name: 'Dismiss' }).click();
    await card.waitFor({ state: 'detached', timeout: 5000 }).catch(() => {});
    note('Dismiss removes it', (await card.count()) === 0, `${await card.count()} card(s)`);
    await page.reload(); await sleep(2500);
    const left = (await jobs()).filter((j) => j.kind === 'auto-update');
    note('and it stays gone after a reload', (await card.count()) === 0 && left.length === 0, `${await card.count()} card(s), ${left.length} job(s) in /api/jobs`);
    const last = await page.request.get(BASE + '/api/autoupdate').then((r) => r.json());
    note('Last run is a real date', /^20\d\d-/.test(String(last.last?.at || '')), String(last.last?.at));
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/jobs-banner-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
