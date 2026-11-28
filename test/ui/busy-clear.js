// A stack that starts is never read as a problem on a page already open on
// it: busy clears when the start reaches its stay-up check, before the status
// loop's next event, and the page once showed "Stopped" and "Problems 1" for
// that moment. Sampled every 100 ms, each change printed with its time. The
// stack is t-bw, started through the API while the page watches.
const { chromium } = require('playwright-core');
const { BASE, sleep, signedIn, openPage } = require('./session');
const H = { Origin: BASE, 'Content-Type': 'application/json' };
const LINUX = process.env.HOST_OS === 'linux';
const IMAGE = LINUX ? 'docker.io/traefik/whoami:v1.10' : 'ghcr.io/daemonless/openspeedtest:latest';
const CPORT = LINUX ? 80 : 3000;
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }))).newPage();
  await page.request.delete(BASE + '/api/stacks/t-bw?data=1', { headers: H, timeout: 60000 }).catch(() => {});
  await page.request.post(BASE + '/api/stacks/t-bw/save', { headers: H, data: { compose: `services:\n  web:\n    image: ${IMAGE}\n    ports:\n      - "3139:${CPORT}"\n`, env: '' } });
  await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/t-bw');
  await sleep(1500);
  const t0 = Date.now();
  const up = page.request.post(BASE + '/api/stacks/t-bw/up', { headers: H, timeout: 180000 }).then((r) => r.text()).catch(() => '');
  let last = '', bad = 0;
  for (let i = 0; i < 250; i++) {
    const [badge, problems, api] = await Promise.all([
      page.locator('header h2 + span').first().innerText().catch(() => '?'),
      page.getByRole('button', { name: /Problems/ }).first().innerText({ timeout: 50 }).catch(() => '-'),
      page.request.get(BASE + '/api/stacks/t-bw').then((r) => r.json()).then((d) => `${d.busy || 'idle'}/${d.status?.state}`).catch(() => '?'),
    ]);
    const line = `page badge "${badge.replace(/\s+/g, ' ')}" problems "${problems.replace(/\s+/g, ' ')}" | fjordd ${api}`;
    if (/Problems\s*[1-9]/.test(problems) && i > 3) bad++;
    if (line !== last) { console.log(`${String(Date.now() - t0).padStart(6)}ms ${line}`); last = line; }
    await sleep(100);
  }
  await up;
  console.log(`${bad === 0 ? 'PASS' : 'FAIL'} never counted as a problem while starting: ${bad} of 250 samples read "Problems" with a count`);
  await page.request.delete(BASE + '/api/stacks/t-bw?data=1', { headers: H, timeout: 120000 }).catch(() => {});
  await b.close();
})();
