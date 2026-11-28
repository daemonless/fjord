// Automatic updates, as someone turns them on and checks the night after:
// syncthing 2.1.3, Keep up to date switched on in its Settings tab, the
// window moved in Settings -> Updates, Run now, and the last run says it was
// updated, the stack runs the newer version, and History has an automatic
// update. A second run finds nothing to do. Then a bad update: t-au2's
// newer container is killed during its health watch (autoupdate.sh), and
// the run rolls it back to 2.1.3, the stack says so with Try again and
// Keep, the next run leaves it held, and Try again takes the update.
// autoupdate.sh makes the newer versions old enough to take.
// needs: freebsd
const { chromium } = require('playwright-core');
const { signedIn, openPage, customTag } = require('./session');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-au';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const detail = async (page) => (await page.request.get(BASE + '/api/stacks/' + NAME)).json();
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext(signedIn({ viewport: { width: 1400, height: 1100 } }))).newPage();
  page.setDefaultTimeout(30000);
  try {
    // The offer, once, to a stack installed before Keep up to date existed.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks');
    const offer = page.locator('[data-autoupdate-offer]');
    await offer.waitFor({ timeout: 15000 }).catch(() => {});
    const offerText = (await offer.innerText().catch(() => '')).replace(/\s+/g, ' ');
    const offered = (await (await page.request.get(BASE + '/api/autoupdate')).json()).offer || [];
    await page.screenshot({ path: '/out/autoupdate-0-offer.png', fullPage: true });
    note('the dashboard offers it to the stacks from before', offered.includes('t-old') && /keep your apps up to date by itself/.test(offerText) && /Turn on for/.test(offerText), `${offered.join(',')}: ${offerText.slice(0, 160)}`);
    await offer.getByRole('button', { name: /^Turn on for/ }).click();
    await sleep(1500);
    const old = await (await page.request.get(BASE + '/api/stacks/t-old')).json().catch(() => ({}));
    await page.reload();
    await sleep(1500);
    note('Turn on: on for them, and not offered again', old?.state?.update_policy === 'minor' && !(await offer.isVisible().catch(() => false)),
      `t-old ${old?.state?.update_policy}, banner ${await offer.isVisible().catch(() => false)}`);

    // syncthing 2.1.3, on the bridge.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
    await page.getByPlaceholder(/Search apps/).fill('syncthing');
    await sleep(800);
    await page.getByRole('button', { name: /^Install/ }).first().click();
    const install = page.getByRole('button', { name: 'Install', exact: true });
    await install.waitFor();
    await page.getByLabel('Name', { exact: true }).fill(NAME);
    if (!(await page.locator('#WEB_PORT').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
    await page.locator('#WEB_PORT').fill('3124');
    const netSel = page.locator('table select').filter({ has: page.locator('option[value="bridge"]') });
    if ((await netSel.count()) === 0) await page.getByRole('button', { name: /\+ interface/ }).first().click();
    await netSel.first().selectOption('bridge');
    await page.getByText('Advanced', { exact: true }).click();
    await (await customTag(page)).fill('2.1.3');
    await sleep(1500);
    // The wizard starts off by default; keep this install off.
    const ask = page.locator('[data-wizard-keep-up-to-date]').getByRole('switch', { name: 'Keep up to date' });
    note('the wizard asks, off by default', (await ask.getAttribute('aria-checked').catch(() => '')) === 'false', 'unchecked');
    await install.click();
    let d;
    for (let i = 0; i < 100; i++) { d = await detail(page).catch(() => ({})); if (!d?.busy && (d?.status?.state === 'running' || d?.state?.last_failure)) break; await sleep(3000); }
    note('installed on 2.1.3', d?.status?.state === 'running' && /:2\.1\.3$/.test(d?.services?.[0]?.image || ''), `${d?.status?.state}, ${d?.services?.[0]?.image}`);

    // Keep up to date: off for a new stack until someone says so; on here.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/' + NAME);
    await page.locator('[data-tab="settings"]').click();
    const keep = page.getByRole('switch', { name: 'Keep up to date' });
    await keep.waitFor();
    note('the wizard\'s no is stored as off, not left unset', (await detail(page))?.state?.update_policy === 'off', String((await detail(page))?.state?.update_policy));
    note('off to begin with', (await keep.getAttribute('aria-checked')) === 'false', await page.locator('[data-keep-up-to-date] p').innerText());
    await keep.click();
    await sleep(1500);
    const said = await page.locator('[data-keep-up-to-date] p').innerText();
    await page.screenshot({ path: '/out/autoupdate-1-stack.png', fullPage: true });
    note('on: says what it will do and when', (await keep.getAttribute('aria-checked')) === 'true' && /Between \d\d:\d\d and \d\d:\d\d/.test(said) && /major versions wait/.test(said), said.replace(/\s+/g, ' '));
    note('stored as minor', (await detail(page))?.state?.update_policy === 'minor', String((await detail(page))?.state?.update_policy));

    // Settings -> Updates: the window, new-app default switch, and bulk confirm dialog.
    await openPage(page, BASE + '/?t=' + Date.now() + '#/settings/updates');
    await page.locator('[data-updates-settings]').waitFor();
    const defSw = page.locator('[data-au-default-for-new]').getByRole('switch', { name: 'Keep new apps up to date' });
    note('new-app default starts off in Settings', (await defSw.getAttribute('aria-checked').catch(() => '')) === 'false', 'unchecked');
    await defSw.click();
    await sleep(600);
    const defOn = await (await page.request.get(BASE + '/api/autoupdate')).json();
    note('new-app default persists on', defOn.defaultForNew === true, String(defOn.defaultForNew));
    await defSw.click();
    await sleep(600);
    await page.getByRole('button', { name: 'Turn off for all' }).click();
    const dlg = page.getByRole('dialog');
    await dlg.waitFor();
    note('bulk toggle opens confirmation dialog', /Turn Keep up to date off for all apps/.test(await dlg.innerText()), 'confirm');
    await dlg.getByRole('button', { name: 'Cancel' }).click();
    await page.locator('#au-start').fill('01:30');
    await page.locator('#au-start').dispatchEvent('change');
    await page.locator('#au-end').fill('02:45');
    await page.locator('#au-end').dispatchEvent('change');
    await sleep(1000);
    const w = await (await page.request.get(BASE + '/api/autoupdate')).json();
    note('window moved and kept', w.start === '01:30' && w.end === '02:45', `${w.start}-${w.end}, next ${w.next}`);
    note('says when the next run is', await page.locator('[data-au-next]').isVisible().catch(() => false), await page.locator('[data-au-next]').innerText().catch(() => 'nothing'));

    // Run now, and wait for the last run to name the stack.
    await page.getByRole('button', { name: 'Run now', exact: true }).click();
    let line = '';
    for (let i = 0; i < 100 && !line; i++) {
      await sleep(3000);
      const a = await (await page.request.get(BASE + '/api/autoupdate')).json().catch(() => ({}));
      if (a.running) continue;
      const r = (a.last?.results || []).find((x) => x.stack === NAME);
      if (r) line = `${r.result}: ${r.reason || (r.services || []).join(',')}`;
      else if (a.last?.at) line = 'not listed';
    }
    await page.reload();
    await page.locator('[data-au-results]').waitFor({ timeout: 10000 }).catch(() => {});
    const shown = await page.locator('[data-au-results]').innerText().catch(() => '');
    await page.screenshot({ path: '/out/autoupdate-2-last-run.png', fullPage: true });
    note('Run now updated it', /^updated/.test(line), line);
    note('Last run says so', new RegExp(NAME + '\\s+Updated \\(syncthing\\)').test(shown), shown.replace(/\s+/g, ' '));

    d = await detail(page);
    const img = d?.services?.[0]?.image || '';
    note('running the newer version', d?.status?.state === 'running' && !/:2\.1\.3$/.test(img), `${d?.status?.state}, ${img}`);
    const h = await (await page.request.get(BASE + '/api/stacks/' + NAME + '/history?limit=5')).json().catch(() => []);
    const e = (Array.isArray(h) ? h : h.entries || []).find((x) => x.action === 'update');
    note('History: an automatic update, ok', e?.trigger === 'automatic' && e?.result === 'ok', JSON.stringify(e || {}).slice(0, 200));

    // Again: nothing left to take.
    const firstAt = (await (await page.request.get(BASE + '/api/autoupdate')).json()).last?.at;
    await page.getByRole('button', { name: 'Run now', exact: true }).click();
    let again = '';
    for (let i = 0; i < 60 && !again; i++) {
      await sleep(3000);
      const a = await (await page.request.get(BASE + '/api/autoupdate')).json().catch(() => ({}));
      if (a.running || !a.last?.at || a.last.at === firstAt) continue;
      const r = (a.last.results || []).find((x) => x.stack === NAME);
      again = r ? `${r.result}: ${r.reason || ''}` : 'not listed';
    }
    note('a second run leaves it alone', again === 'not listed' || /^waiting/.test(again), again);

    // A bad update. t-au2 on 2.1.3, on, run.
    const T2 = 't-au2';
    const d2 = async () => (await page.request.get(BASE + '/api/stacks/' + T2)).json().catch(() => ({}));
    await openPage(page, BASE + '/?t=' + Date.now() + '#/store');
    await page.getByPlaceholder(/Search apps/).fill('syncthing');
    await sleep(800);
    await page.getByRole('button', { name: /^Install/ }).first().click();
    await install.waitFor();
    await page.getByLabel('Name', { exact: true }).fill(T2);
    if (!(await page.locator('#WEB_PORT').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
    await page.locator('#WEB_PORT').fill('3125');
    const netSel2 = page.locator('table select').filter({ has: page.locator('option[value="bridge"]') });
    if ((await netSel2.count()) === 0) await page.getByRole('button', { name: /\+ interface/ }).first().click();
    await netSel2.first().selectOption('bridge');
    await page.getByText('Advanced', { exact: true }).click();
    await (await customTag(page)).fill('2.1.3');
    await sleep(1500);
    await page.locator('[data-wizard-keep-up-to-date]').getByRole('switch', { name: 'Keep up to date' }).click();
    await install.click();
    let e2;
    for (let i = 0; i < 100; i++) { e2 = await d2(); if (!e2?.busy && (e2?.status?.state === 'running' || e2?.state?.last_failure)) break; await sleep(3000); }
    // Turned on in the wizard: stored as on, nothing else needed.
    note('the wizard\'s yes is stored as minor', e2?.state?.update_policy === 'minor', String(e2?.state?.update_policy));
    const lastAt = async () => (await (await page.request.get(BASE + '/api/autoupdate')).json()).last?.at;
    const runOnce = async () => {
      const before = await lastAt();
      await page.request.fetch(BASE + '/api/autoupdate/run', { method: 'POST', headers: { Origin: BASE } });
      for (let i = 0; i < 150; i++) {
        await sleep(3000);
        const a = await (await page.request.get(BASE + '/api/autoupdate')).json().catch(() => ({}));
        if (!a.running && a.last?.at && a.last.at !== before) return (a.last.results || []).find((x) => x.stack === T2);
      }
      return null;
    };
    const bad = await runOnce();
    note('the bad update was rolled back', bad?.result === 'rolled back', JSON.stringify(bad));
    e2 = await d2();
    note('2.1.3 runs again', e2?.status?.state === 'running' && /:2\.1\.3$/.test(e2?.services?.[0]?.image || ''), `${e2?.status?.state}, ${e2?.services?.[0]?.image}`);

    await openPage(page, BASE + '/?t=' + Date.now() + '#/stacks/' + T2);
    const banner = page.locator('[data-auto-rollback]');
    await banner.waitFor({ timeout: 15000 }).catch(() => {});
    const bannerText = (await banner.innerText().catch(() => '')).replace(/\s+/g, ' ');
    await page.screenshot({ path: '/out/autoupdate-3-rolled-back.png', fullPage: true });
    note('the stack says what happened, with Try again and Keep', /automatic update.*failed/i.test(bannerText) && /is back/.test(bannerText) && /Try again/.test(bannerText) && /Keep/.test(bannerText), bannerText);
    const h2 = await (await page.request.get(BASE + '/api/stacks/' + T2 + '/history?limit=5')).json().catch(() => []);
    const hu = h2.find((x) => x.action === 'update'), hr = h2.find((x) => x.action === 'rollback');
    note('History: the update failed, the rollback ok, both automatic',
      hu?.trigger === 'automatic' && hu?.result === 'failed' && hr?.trigger === 'automatic' && hr?.result === 'ok',
      `update ${hu?.result}/${hu?.trigger}, rollback ${hr?.result}/${hr?.trigger}`);

    const held = await runOnce();
    note('the next run leaves it held', held?.result === 'waiting' && /rolled back/.test(held?.reason || ''), JSON.stringify(held));

    await page.getByRole('button', { name: 'Try again', exact: true }).click();
    let e3;
    for (let i = 0; i < 100; i++) {
      await sleep(3000);
      e3 = await d2();
      if (!e3?.busy && !/:2\.1\.3$/.test(e3?.services?.[0]?.image || '') && e3?.status?.state === 'running') break;
    }
    note('Try again takes the update', e3?.status?.state === 'running' && !/:2\.1\.3$/.test(e3?.services?.[0]?.image || '') && !e3?.state?.auto_rollback,
      `${e3?.status?.state}, ${e3?.services?.[0]?.image}, banner ${!!e3?.state?.auto_rollback}`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/autoupdate-error.png', fullPage: true }).catch(() => {});
  } finally {
    await b.close();
  }
})();
