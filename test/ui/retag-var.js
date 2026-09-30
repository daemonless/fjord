// Change Version on an image whose tag is a variable ("${TAG:-latest}") sets
// the variable in .env and leaves the compose line as written; the container
// comes back on the new tag. Through the pages, on a stack made for it.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-var';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/' + NAME)).json();
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1000 } })).newPage();
  page.setDefaultTimeout(30000);
  try {
    // The stack, the way the editor saves one: a tag that is a variable.
    const compose = 'services:\n  speed:\n    image: ghcr.io/daemonless/openspeedtest:${SPEED_TAG:-latest}\n    ports:\n      - "3115:3000"\n';
    const r = await page.request.post(BASE + '/api/stacks/' + NAME + '/save', { headers: { Origin: BASE, 'Content-Type': 'application/json' }, data: { compose, env: 'SPEED_TAG=latest\n' } });
    note('stack saved with a variable tag', r.ok(), `${r.status()}`);
    await page.request.post(BASE + '/api/stacks/' + NAME + '/up', { headers: { Origin: BASE } });
    let d;
    for (let i = 0; i < 60; i++) { d = await detail(page); if (d?.status?.state === 'running' && !d.busy) break; await sleep(3000); }
    note('running on the variable\'s value', d?.status?.state === 'running', `state ${d?.status?.state}`);

    await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + NAME, { waitUntil: 'networkidle' });
    await sleep(1500);
    await page.getByRole('button', { name: 'More Actions' }).click();
    await page.getByRole('button', { name: /Change Version/ }).click();
    const current = await page.getByText(/Currently running/).first().innerText().catch(() => '');
    note('the modal shows the tag as it runs, not the variable', /:latest/.test(current) && !/\$\{/.test(current), `"${current}"`);
    await page.getByText(/Loading published versions/).waitFor({ state: 'hidden', timeout: 30000 }).catch(() => {});
    await page.getByPlaceholder(/Custom tag/).fill('2.0.5');
    await sleep(1500);
    await page.getByRole('button', { name: /Change & Redeploy/ }).click();
    for (let i = 0; i < 80; i++) { d = await detail(page); if (!d.busy && d.status?.state === 'running') break; await sleep(3000); }
    await sleep(2000);
    d = await detail(page);
    const line = (d.compose.match(/^\s*image:.*$/m) || [''])[0].trim();
    note('the compose line is as written', line === 'image: ghcr.io/daemonless/openspeedtest:${SPEED_TAG:-latest}', `"${line}"`);
    note('.env carries the new tag', /^SPEED_TAG=2\.0\.5$/m.test(d.env), JSON.stringify(d.env));
    const img = d.services?.[0]?.image || '';
    note('the service runs the new tag', d.status?.state === 'running' && /openspeedtest:2\.0\.5$/.test(img), `${d.status?.state}, ${img}`);
    await page.screenshot({ path: '/out/retag-var.png', fullPage: true });
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/retag-var-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
