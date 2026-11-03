// Adopt & Restart with nobody watching. A container started by hand is
// adopted with replace from the Adopt page and the browser is closed the
// moment the click is made: the request that removed the running container
// must also bring the stack up, by fjord's doing. Then a page opened later
// finds the stack running and its start in the kept Output.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const C = process.env.CONTAINER || 't-byhand';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);

(async () => {
  let b = await chromium.launch({ args: ['--no-sandbox'] });
  let page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1000 } }))).newPage();
  page.setDefaultTimeout(30000);
  const req = async (path) => (await page.request.get(BASE + path)).json().catch(() => null);
  try {
    const before = ((await req('/api/adopt')) || []).find((c) => c.name === C);
    note('the hand-started container is offered for adoption', before?.state === 'running', before ? `${before.name} ${before.state}` : 'not listed');
    await openPage(page, BASE + '/?t=' + Date.now() + '#/adopt');
    // Adopt, then the confirmation that names what happens: Adopt & Restart
    // for a running container. Clicked, and the browser goes away at once.
    await page.getByRole('button', { name: 'Adopt', exact: true }).first().click();
    const confirm = page.getByRole('button', { name: 'Adopt & Restart' }).first();
    await confirm.waitFor();
    await confirm.click();
    await sleep(300);
    await page.screenshot({ path: '/out/adopt-replace-1-clicked.png', fullPage: true }).catch(() => {});
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/adopt-replace-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();

  // No browser now. The daemon alone must finish the job.
  b = await chromium.launch({ args: ['--no-sandbox'] });
  page = await (await b.newContext(signedIn())).newPage();
  const api = async (path) => (await page.request.get(BASE + path)).json().catch(() => null);
  let d = null;
  for (let i = 0; i < 40; i++) {
    d = await api('/api/stacks/' + C);
    if (d && !d.busy && d.status?.state === 'running') break;
    await sleep(3000);
  }
  note('with the browser gone, the stack came up by itself', d?.status?.state === 'running', `state ${d?.status?.state ?? 'no stack'}, busy ${d?.busy ?? 'no'}`);
  const out = (await api(`/api/stacks/${C}/output`)) || {};
  note('its start is in the kept Output', /podman start|Starting|stayed up|\[fjord\]/.test(out.text || '') && out.done, out.text ? out.text.trim().split('\n').slice(-2).join(' | ').slice(0, 160) : 'no output kept');
  note('the hand-started container is gone, replaced by the stack\'s', !((await api('/api/adopt')) || []).some((c) => c.name === C), 'checked');
  await page.goto(BASE + '/?t=' + Date.now() + '#/stacks/' + C, { waitUntil: 'domcontentloaded' });
  await sleep(2000);
  await page.screenshot({ path: '/out/adopt-replace-2-later.png', fullPage: true }).catch(() => {});
  const shown = await page.getByText(/^running$/i).first().isVisible().catch(() => false);
  note('a page opened later shows it running', shown, shown ? 'Running' : 'not shown running');
  await b.close();
})();
