// A second copy of an app gets folders of its own: the wizard's defaults
// follow the stack name, and fjordd refuses a folder inside another stack's.
// immich is installed on netlab, so /containers/immich/... is someone's.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(30000);
  try {
    await page.goto(BASE + '/#/store', { waitUntil: 'networkidle' });
    await page.getByPlaceholder(/Search apps/).fill('librenms');
    await sleep(1000);
    await page.getByRole('button', { name: /^Install/ }).first().click();
    const install = page.getByRole('button', { name: 'Install', exact: true });
    await install.waitFor();
    await page.getByRole('button', { name: /^Options/ }).first().click();
    const cfg = page.locator('#CONFIG_LOCATION');
    const db = page.locator('#DB_DATA_LOCATION');
    await cfg.waitFor();
    note('first copy: the catalog folders as they are', (await cfg.inputValue()) === '/containers/librenms/config' && (await db.inputValue()) === '/containers/librenms/mariadb', `${await cfg.inputValue()}, ${await db.inputValue()}`);
    await page.getByLabel('Name').fill('t-lnms');
    await sleep(500);
    note('renamed to t-lnms: folders follow', (await cfg.inputValue()) === '/containers/t-lnms/config' && (await db.inputValue()) === '/containers/t-lnms/mariadb', `${await cfg.inputValue()}, ${await db.inputValue()}`);
    await page.getByLabel('Name').fill('Photo Box 2');
    await sleep(500);
    note('a name with spaces: slugged', (await cfg.inputValue()) === '/containers/photo-box-2/config', await cfg.inputValue());
    await page.screenshot({ path: '/out/folders-wizard.png', fullPage: true });
    // A folder typed by hand stays put through a rename.
    await cfg.fill('/mnt/lnms-config');
    await page.getByLabel('Name').fill('t-lnms');
    await sleep(500);
    note('a folder typed by hand is kept through a rename', (await cfg.inputValue()) === '/mnt/lnms-config' && (await db.inputValue()) === '/containers/t-lnms/mariadb', `${await cfg.inputValue()}, ${await db.inputValue()}`);
    // fjordd's own guard: point it into immich's folder and try to install.
    await cfg.fill('/containers/immich/config');
    await page.locator('#DB_PASSWORD').fill('lnms-db-t3st');
    await page.locator('#ADMIN_PASSWORD').fill('Fjord-t3st-9d7c2a41');
    await install.click();
    const refusal = page.getByText(/is inside immich's folder/).first();
    await refusal.waitFor({ timeout: 30000 }).catch(() => {});
    const text = await refusal.innerText().catch(() => '');
    await page.screenshot({ path: '/out/folders-refused.png', fullPage: true });
    note("fjordd refuses a folder inside another stack's, on the wizard", /\/containers\/immich\/config is inside immich's folder \(\/containers\/immich\) -- give this install a folder of its own/.test(text), `"${text}"`);
    const stacks = (await (await page.request.get(BASE + '/api/stacks')).json()).map((s) => s.name);
    note('nothing was created', !stacks.includes('t-lnms'), `stacks: ${stacks.join(', ')}`);
  } catch (e) {
    note('run', false, e.message.split('\n')[0]);
    await page.screenshot({ path: '/out/folders-error.png', fullPage: true }).catch(() => {});
  }
  await b.close();
})();
