// Change Version is one job of the daemon's. STEP=gone: the dialog's Change
// & Redeploy is clicked and the browser closed the moment set-tag answers;
// with no page anywhere the stack must end up running the new tag, the
// compose naming it, the rollback recorded (the page used to run the
// update as a second call: closed in between, the compose said the new
// version while the old one ran). STEP=stays: the same change with the
// page open follows the update in the Output pane and ends idle on the
// new version.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-cv';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/' + NAME)).json();
const image = (d) => d?.services?.[0]?.image || '';
const settled = async (page, tag) => {
  let d;
  for (let i = 0; i < 80; i++) {
    d = await detail(page);
    if (!d.busy && d.status?.state === 'running' && new RegExp(':' + tag.replace(/\./g, '\\.') + '$').test(image(d))) break;
    await sleep(3000);
  }
  await sleep(2000);
  return detail(page);
};
const newBrowser = async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(30000);
  return { b, page };
};
// The dialog, filled with tag, up to the click: the response of set-tag.
const changeTo = async (page, tag) => {
  await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/' + NAME);
  await sleep(1500);
  await page.getByRole('button', { name: 'More Actions' }).click();
  await page.getByRole('button', { name: /Change Version/ }).click();
  await page.getByText(/Loading published versions/).waitFor({ state: 'hidden', timeout: 30000 }).catch(() => {});
  await page.getByPlaceholder(/Custom tag/).fill(tag);
  await sleep(1500);
  const answered = page.waitForResponse((r) => r.url().includes('/set-tag'));
  await page.getByRole('button', { name: /Change & Redeploy/ }).click();
  return answered;
};

(async () => {
  let { b, page } = await newBrowser();
  try {
    if (process.env.STEP === 'gone') {
      const compose = 'services:\n  speed:\n    image: ghcr.io/daemonless/openspeedtest:2.0.5\n    ports:\n      - "3116:3000"\n';
      const r = await page.request.post(BASE + '/api/stacks/' + NAME + '/save', { headers: { Origin: BASE, 'Content-Type': 'application/json' }, data: { compose, env: '' } });
      note('stack saved on 2.0.5', r.ok(), `${r.status()}`);
      await page.request.post(BASE + '/api/stacks/' + NAME + '/up', { headers: { Origin: BASE } });
      let d = await settled(page, '2.0.5');
      note('running 2.0.5 before the change', d?.status?.state === 'running' && /:2\.0\.5$/.test(image(d)), `${d?.status?.state}, ${image(d)}`);

      const res = await changeTo(page, 'latest');
      const body = await res.json().catch(() => ({}));
      note('set-tag answers with the update under way', res.ok() && body.updating === true, `${res.status()} ${JSON.stringify(body)}`);
      await b.close(); // the page is gone: nothing of it may be needed from here
      ({ b, page } = await newBrowser());
      d = await detail(page);
      note('fjordd is busy with the update, with no page open', !!d.busy || /:latest$/.test(image(d)), `busy=${d.busy}, ${image(d)}`);
      d = await settled(page, 'latest');
      note('the stack runs the new tag with no page having waited for it', d.status?.state === 'running' && /:latest$/.test(image(d)) && !d.state?.last_failure, `${d.status?.state}, ${image(d)}, failure=${JSON.stringify(d.state?.last_failure ?? null)}`);
      note('the compose names what runs', /openspeedtest:latest$/m.test(d.compose), (d.compose.match(/^\s*image:.*$/m) || [''])[0].trim());
      note('the rollback to 2.0.5 is recorded', /2\.0\.5/.test(d.state?.rollback?.speed?.ref || ''), JSON.stringify(d.state?.rollback?.speed ?? null));
    } else {
      const res = await changeTo(page, '2.0.5');
      note('set-tag answers with the update under way', res.ok(), `${res.status()}`);
      let text = '';
      for (let i = 0; i < 60; i++) { text = await page.locator('body').innerText(); if (/Updating…/.test(text)) break; await sleep(500); }
      note('the page says Updating… for the daemon\'s job', /Updating…/.test(text), /Updating…/.test(text) ? 'chip shown' : 'no Updating… on the page');
      for (let i = 0; i < 120; i++) { text = await page.locator('body').innerText(); if (/\$ podman/.test(text)) break; await sleep(500); }
      await page.screenshot({ path: '/out/change-version-mid.png', fullPage: true });
      note('the Output pane follows the update this page did not stream', /\$ podman/.test(text) && !/Waiting for output/.test(text), /\$ podman/.test(text) ? 'podman lines on screen' : 'no output shown');
      const d = await settled(page, '2.0.5');
      // Not busy comes when the stay-up check starts; the badge follows it
      // to the end of the check, then reads idle.
      for (let i = 0; i < 60; i++) { text = await page.locator('body').innerText(); if (/Checking it stays up/.test(text)) break; await sleep(1000); }
      note('the badge says the check is on, with the actions back', /Checking it stays up/.test(text) && !/actions come back/.test(text), /Checking it stays up/.test(text) ? 'check shown' : 'no check on screen');
      for (let i = 0; i < 60; i++) { text = await page.locator('body').innerText(); if (!/Updating…|Checking it stays up/.test(text)) break; await sleep(2000); }
      await sleep(2000);
      text = await page.locator('body').innerText();
      await page.screenshot({ path: '/out/change-version-done.png', fullPage: true });
      note('the page ends idle', !/Updating…|Checking it stays up|actions come back/.test(text), /Updating…|Checking it stays up|actions come back/.test(text) ? 'still busy on screen' : 'idle');
      note('the Change Version failed line is not shown', !/Version change failed|set-tag failed/.test(text), 'no error text');
      note('the stack runs 2.0.5 again', d.status?.state === 'running' && /:2\.0\.5$/.test(image(d)) && !d.state?.last_failure, `${d.status?.state}, ${image(d)}`);
    }
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/change-version-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
