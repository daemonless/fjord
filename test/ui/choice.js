// A stack's choices in the wizard (x-fjord.choices), then an install that
// answers one: vikunja from a catalog that declares its Database choice.
// The rows show, "Your own" asks for its fields and holds Install until
// they are filled, and an install on PostgreSQL runs with the database it
// brought, the answer recorded on the stack.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const NAME = 't-choice';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1300, height: 1000 } })).newPage();
  page.setDefaultTimeout(30000);
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  try {
    await page.goto(BASE + '/?t=' + Date.now() + '#/store', { waitUntil: 'networkidle' });
    await page.getByPlaceholder(/Search apps/).fill('vikunja');
    await sleep(800);
    await page.getByRole('button', { name: /^Install/ }).last().click();
    const install = page.getByRole('button', { name: 'Install', exact: true });
    await install.waitFor();
    // The catalog that carries the choices; the published one may not yet.
    if (!(await page.locator('#repo').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
    const repos = await page.locator('#repo option').allInnerTexts();
    const withChoices = process.env.CHOICE_CATALOG || 'proof';
    if (repos.includes(withChoices)) await page.locator('#repo').selectOption({ label: withChoices });
    await sleep(2000);
    const body = () => page.locator('body').innerText();

    const options = await page.getByRole('radio').allInnerTexts();
    note('rows', options.join(',') === 'SQLite,PostgreSQL,MariaDB,Your own', `options: ${options.join(', ') || 'none'}`);
    note('no page error', errors.length === 0 && !/not defined/.test(await body()), errors[0] || 'clean');

    await page.getByRole('radio', { name: 'Your own' }).click();
    await sleep(400);
    const hostField = page.locator('#ask-VIKUNJA_DATABASE_HOST');
    note('your own asks', await hostField.isVisible(), (await hostField.isVisible()) ? 'its fields are on screen' : 'no fields');
    note('held until filled', await install.isDisabled(), (await install.isDisabled()) ? 'Install held' : 'Install enabled with nothing typed');

    await page.getByRole('radio', { name: 'PostgreSQL' }).click();
    await sleep(300);
    const summary = (await body()).split('\n').find((l) => l.startsWith('Installs')) || '';
    note('summary', /postgresql/i.test(summary), summary || 'no summary line');

    await page.getByLabel('Name').fill(NAME);
    if (!(await page.locator('#WEB_PORT').isVisible().catch(() => false))) await page.getByRole('button', { name: /^Options/ }).first().click();
    await page.locator('#WEB_PORT').fill('3461');
    const netSel = page.locator('table select').filter({ has: page.locator('option[value="bridge"]') });
    if ((await netSel.count()) === 0) await page.getByRole('button', { name: /\+ interface/ }).first().click().catch(() => {});
    if ((await netSel.count()) > 0) await netSel.first().selectOption('bridge');
    await install.click();

    let d = {};
    for (let i = 0; i < 100; i++) {
      d = await (await page.request.get(BASE + '/api/stacks/' + NAME)).json().catch(() => ({}));
      const svcs = d?.status?.containers || [];
      if (!d.busy && svcs.length && svcs.every((c) => c.state === 'running')) break;
      if (d?.state?.last_failure) break;
      await sleep(3000);
    }
    const svcs = (d?.status?.containers || []).map((c) => `${c.service}=${c.state}`);
    note('installed with its database', svcs.includes('postgres=running') && svcs.includes('vikunja=running'), svcs.join(', ') || 'no containers');
    note('answer recorded', d?.state?.choices?.database === 'postgres', JSON.stringify(d?.state?.choices || {}));
    await page.screenshot({ path: '/out/choice.png', fullPage: true });
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/choice-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
