// New Network asks the wire, and fjord flags networks that do not fit it --
// through fjord's pages on netlab. lanbridge has no host address and DHCP on
// VLAN 4; v6lab answers no DHCPv4; lan-range (192.168.86.0/24 on lanbridge)
// is the wrong network left from before.
const { chromium } = require('playwright-core');
const BASE = process.env.FJORD || 'http://127.0.0.1:3567';

const results = [];
const note = (id, ok, msg) => { results.push({ id, ok, msg }); console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`); };
const shot = (page, name) => page.screenshot({ path: `/out/${name}.png`, fullPage: true }).catch(() => {});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const api = async (page, path, opts) => {
  const r = await page.request.fetch(BASE + path, { ...opts, headers: { Origin: BASE, 'Content-Type': 'application/json' } });
  try { return await r.json(); } catch { return null; }
};

async function openForm(page, name, bridge) {
  await page.goto(BASE + '/#/networks', { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: /New network/i }).first().click();
  await page.getByRole('button', { name: 'epair', exact: true }).click();
  await page.getByRole('button', { name: 'both', exact: true }).click();
  await page.getByPlaceholder(/vlan4, lan/).fill(name);
  await page.locator('#n-parent').selectOption(bridge);
}
const infoLine = (page) => page.locator('#n-parent + p');
const createBtn = (page) => page.getByRole('button', { name: 'Create', exact: true });
const warning = async (page) => (await page.locator('.text-fjord-warning').allInnerTexts()).join(' ').trim();

(async () => {
  const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-setuid-sandbox'] });
  const page = await (await browser.newContext({ viewport: { width: 1400, height: 1100 } })).newPage();
  page.setDefaultTimeout(20000);
  const errs = [];
  page.on('pageerror', (e) => errs.push(e.message));
  try {
    // 1. A bridge with no host address: the form asks its DHCP server.
    await openForm(page, 't-wire', 'lanbridge');
    const asking = await infoLine(page).innerText();
    await page.getByText(/from lanbridge's DHCP server/).waitFor({ timeout: 20000 });
    const told = await infoLine(page).innerText();
    note('New Network asks lanbridge\'s DHCP server', /asking/.test(asking) && /192\.168\.4\.0\/24/.test(told) && /192\.168\.4\.1/.test(told),
      `while asking: "${asking.trim()}"; then: "${told.trim()}"`);

    await page.getByRole('button', { name: 'Range', exact: true }).click();
    await sleep(400);
    const subnet = await page.locator('#n-subnet').inputValue();
    note('Range: subnet filled in from the answer', subnet === '192.168.4.0/24', `subnet box: "${subnet}"`);

    // 2. The wrong segment is refused, and says why next to Create.
    await page.locator('#n-subnet').fill('192.168.86.0/24');
    await page.locator('#n-subnet').blur();
    await sleep(300);
    const blocked = await createBtn(page).isDisabled();
    const why = await warning(page);
    await shot(page, 'wire-refused');
    note('192.168.86.0/24 on lanbridge is refused, reason on screen', blocked && /not on that wire/.test(why), `Create disabled ${blocked}; "${why}"`);

    // 3. The right one creates.
    await page.locator('#n-subnet').fill('192.168.4.0/24');
    await page.locator('#n-subnet').blur();
    await sleep(300);
    const okNow = !(await createBtn(page).isDisabled());
    await createBtn(page).click();
    await sleep(2500);
    const made = ((await api(page, '/api/networks')) || []).find((n) => n.name === 't-wire');
    note('192.168.4.0/24 on lanbridge creates', okNow && made?.subnet === '192.168.4.0/24', made ? `created, ${made.subnet}` : 'not created');

    // 4. The Networks page flags lan-range now that the wire is known.
    await page.goto(BASE + '/#/networks', { waitUntil: 'networkidle' });
    await sleep(1000);
    const row = page.locator('div').filter({ hasText: /^lan-range/ }).first();
    const pageText = await page.locator('body').innerText();
    const flagged = /lanbridge is on 192\.168\.4\.0\/24 \(its DHCP server says so\), not 192\.168\.86\.0\/24/.test(pageText);
    await shot(page, 'wire-networks-page');
    note('Networks page flags lan-range (192.168.86.0/24 on the VLAN 4 wire)', flagged, flagged ? 'warning shown on its row' : 'no warning on the page');
    void row;

    // 5. A bridge where nothing answers: said plainly, and the subnet is typed.
    await openForm(page, 't-quiet', 'v6lab');
    await page.getByText(/nothing answered DHCP on v6lab/).waitFor({ timeout: 25000 });
    const quiet = await infoLine(page).innerText();
    await page.getByRole('button', { name: 'Range', exact: true }).click();
    await page.locator('#n-subnet').fill('10.77.0.0/24');
    await page.locator('#n-subnet').blur();
    await sleep(300);
    const typedOk = !(await createBtn(page).isDisabled());
    await shot(page, 'wire-quiet');
    note('v6lab: says nothing answered, still lets you type a subnet', /could not check it/.test(quiet) && typedOk, `"${quiet.trim()}"; Create enabled ${typedOk}`);
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  } catch (e) {
    await shot(page, 'wire-error');
    note('run', false, 'error: ' + e.message.split('\n')[0]);
  } finally {
    await api(page, '/api/networks/t-wire?engine=podman', { method: 'DELETE' });
    note('no page errors', errs.length === 0, errs.length ? errs.join(' | ') : 'none');
    const pass = results.filter((r) => r.ok).length;
    console.log(`\n==== UI: ${pass}/${results.length} passed ====`);
    await browser.close();
  }
})();
