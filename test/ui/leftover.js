// A private network whose stack is gone reads as left over on the Networks
// page, and Delete removes it -- through the page.
const { chromium } = require('playwright-core');
const BASE = 'http://127.0.0.1:3567';
const NET = process.env.NET || 't-left_priv';
const note = (id, ok, msg) => console.log(`${ok ? 'PASS' : 'FAIL'} ${id}: ${msg}`);
const rowOf = (page, name) =>
  page.locator('span.font-mono', { hasText: new RegExp('^' + name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '$') }).first().locator('xpath=ancestor::div[contains(@class,"gap-3") and contains(@class,"px-4")][1]');
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await b.newContext({ viewport: { width: 1400, height: 1000 } })).newPage();
  page.setDefaultTimeout(20000);
  try {
    await page.goto(BASE + '/#/networks', { waitUntil: 'networkidle' });
    const disclosure = page.getByRole('button', { name: /not reachable from your LAN/ }).first();
    const summary = await disclosure.innerText();
    await disclosure.click();
    const row = rowOf(page, NET);
    await row.waitFor();
    const whose = await row.getByText(/left over|engine's own|made by/).first().innerText().catch(() => '');
    const del = row.getByRole('button', { name: 'Delete', exact: true });
    const greyed = await row.locator('span', { hasText: /^Delete$/ }).count();
    await page.screenshot({ path: '/out/leftover-row.png', fullPage: true });
    note('summary counts it as left over', /left over from a deleted stack/.test(summary), `"${summary.replace(/\s+/g, ' ').trim()}"`);
    note('row says left over from a deleted stack', /^left over from a deleted stack/.test(whose), `"${whose}"`);
    note('Delete is a button, not greyed', (await del.count()) === 1 && greyed === 0, `${await del.count()} button(s), ${greyed} greyed`);
    // ajnet, the engine's own, stays as it was.
    const aj = rowOf(page, 'ajnet');
    const ajWhose = await aj.getByText(/left over|engine's own|made by/).first().innerText().catch(() => '');
    note("ajnet still the engine's own, Delete greyed", /engine's own/.test(ajWhose) && (await aj.locator('span', { hasText: /^Delete$/ }).count()) === 1, `"${ajWhose}"`);

    await del.click();
    await row.getByRole('button', { name: 'Delete', exact: true }).click(); // confirm
    await row.waitFor({ state: 'hidden', timeout: 30000 });
    await page.screenshot({ path: '/out/leftover-after.png', fullPage: true });
    const names = (await (await page.request.get(BASE + '/api/networks')).json()).map((n) => n.name);
    note('deleted: gone from the page and the API', !names.includes(NET), `networks now: ${names.join(', ')}`);
  } catch (e) {
    await page.screenshot({ path: '/out/leftover-fail.png', fullPage: true }).catch(() => {});
    note('run', false, e.message.split('\n')[0]);
  }
  await b.close();
})();
