// An update whose pull fails leaves the stack running what it ran: Update on
// the Stacks list, with the compose pointing at a tag the registry does not
// have. podman-compose exits 0 on a failed pull (FreeBSD) or 125 (Linux),
// and the output must say why either way. The stack is t-badtag.
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const H = { Origin: BASE, 'Content-Type': 'application/json' };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const LINUX = process.env.HOST_OS === 'linux';
const IMAGE = LINUX ? 'docker.io/traefik/whoami' : 'ghcr.io/daemonless/openspeedtest';
const compose = (tag) => `services:\n  web:\n    image: ${IMAGE}:${tag}\n`;
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 900 } }))).newPage();
  page.setDefaultTimeout(30000);
  try {
    const save = (tag) => page.request.post(BASE + '/api/stacks/t-badtag/save', { headers: H, data: { compose: compose(tag), env: '' } });
    if (!(await save(LINUX ? 'v1.10' : 'latest')).ok()) throw new Error('saving t-badtag');
    await (await page.request.post(BASE + '/api/stacks/t-badtag/up', { headers: H, timeout: 180000 })).text();
    if (!(await save('no-such-tag-fjord-test')).ok()) throw new Error('saving the bad tag');
    await openPage(page, BASE + '/#/stacks');
    await page.getByRole('button', { name: /^List$/ }).click().catch(() => {});
    await sleep(1500);
    const row = page.locator('div.cursor-pointer', { hasText: 't-badtag' }).first();
    await row.locator('button[title="Update"]').click();
    await row.getByText('Updating…').first().waitFor({ timeout: 5000 }).catch(() => {});
    note('the row says Updating… while it runs', /Updating/.test(await row.innerText()), (await row.innerText()).replace(/\s+/g, ' '));
    await row.getByText('Updating…').first().waitFor({ state: 'detached', timeout: 180000 });
    await sleep(1500);
    const text = (await row.innerText()).replace(/\s+/g, ' ');
    await page.screenshot({ path: '/out/update-badtag.png' });
    note('the stack still runs after the failed pull', /Running/.test(text), text);
    const out = String((await (await page.request.get(BASE + '/api/stacks/t-badtag/output')).json().catch(() => ({}))).text || '');
    note('the output says why and nothing was replaced', /no such version/.test(out) && /keeps what it runs/.test(out) && !/--force-recreate/.test(out),
      out.split('\n').filter((l) => /\[error\]/.test(l)).join(' / ').slice(0, 300));
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/update-badtag-error.png' }).catch(() => {});
  }
  await page.request.delete(BASE + '/api/stacks/t-badtag?data=1', { headers: H, timeout: 120000 }).catch(() => {});
  await b.close();
})();
