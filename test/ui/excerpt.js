// A failed update keeps a few lines of its output in History: "Show output"
// opens them under the entry, on the app's History tab and on System ->
// History, and an open excerpt stays open when the list reloads. The stack
// is t-exc, updated to a tag the registry does not have.
const { chromium } = require('playwright-core');
const { BASE, sleep, note, signedIn, openPage } = require('./session');
const H = { Origin: BASE, 'Content-Type': 'application/json' };
const LINUX = process.env.HOST_OS === 'linux';
const IMAGE = LINUX ? 'docker.io/traefik/whoami' : 'ghcr.io/daemonless/openspeedtest';
const compose = (tag) => `services:\n  web:\n    image: ${IMAGE}:${tag}\n`;
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }))).newPage();
  page.setDefaultTimeout(30000);
  try {
    const save = (tag) => page.request.post(BASE + '/api/stacks/t-exc/save', { headers: H, data: { compose: compose(tag), env: '' } });
    if (!(await save(LINUX ? 'v1.10' : 'latest')).ok()) throw new Error('saving t-exc');
    await (await page.request.post(BASE + '/api/stacks/t-exc/up', { headers: H, timeout: 180000 })).text();
    if (!(await save('no-such-tag-fjord-test')).ok()) throw new Error('saving the bad tag');
    await (await page.request.post(BASE + '/api/stacks/t-exc/update', { headers: H, timeout: 180000 })).text();
    let entry;
    for (let i = 0; i < 20 && !entry; i++) {
      const hist = await (await page.request.get(BASE + '/api/stacks/t-exc/history')).json().catch(() => []);
      entry = (hist || []).find((e) => e.action === 'update' && e.result === 'failed');
      if (!entry) await sleep(1000);
    }
    note('fjordd kept an excerpt with the failed update', !!entry?.excerpt && /no-such-tag-fjord-test/.test(entry.excerpt), entry ? `${(entry.excerpt || '').length} chars` : 'no failed update in History');
    note('the excerpt is short', (entry?.excerpt || '').split('\n').length <= 25, `${(entry?.excerpt || '').split('\n').length} lines`);
    await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/t-exc/history');
    await sleep(1500);
    const toggle = page.locator('[data-excerpt-toggle]').first();
    note('History: the failed entry offers Show output', (await toggle.count()) === 1 && /Show output/.test(await toggle.innerText()), await toggle.innerText().catch(() => 'no toggle'));
    note('closed: says so to a screen reader', (await toggle.getAttribute('aria-expanded')) === 'false', `aria-expanded=${await toggle.getAttribute('aria-expanded')}`);
    await toggle.click();
    await sleep(500);
    const main = page.locator('main').first();
    const open = await main.innerText();
    note('open: the pull error is on the page', /Hide output/.test(open) && /manifest unknown|no such version|not found/i.test(open), (open.match(/.*(manifest unknown|not found).*/i) || [''])[0].slice(0, 160));
    note('open: aria-expanded follows', (await toggle.getAttribute('aria-expanded')) === 'true', `aria-expanded=${await toggle.getAttribute('aria-expanded')}`);
    await page.screenshot({ path: '/out/excerpt-open.png', fullPage: true });
    // A new entry arriving must not close what is open: stop is one more row.
    await (await page.request.post(BASE + '/api/stacks/t-exc/down', { headers: H, timeout: 120000 })).text();
    await sleep(4000);
    note('still open after the list gained an entry', /Hide output/.test(await toggle.innerText().catch(() => '')), await toggle.innerText().catch(() => 'no toggle'));
    await openPage(page, BASE + '/?t=' + Date.now() + '#/system/history');
    await sleep(1500);
    const row = page.locator('[data-excerpt-toggle]').first();
    note('System -> History offers it too', (await row.count()) >= 1, `${await page.locator('[data-excerpt-toggle]').count()} toggle(s)`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/excerpt-error.png', fullPage: true }).catch(() => {});
  }
  await page.request.delete(BASE + '/api/stacks/t-exc?data=1', { headers: H, timeout: 120000 }).catch(() => {});
  await b.close();
})();
