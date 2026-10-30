// signedIn adds the session scripts/ui-tests.sh signed in with to a browser
// context's options, so a test starts inside fjord, not on its sign-in page.
// No FJORD_SESSION (sign-in off): the options as given.
function signedIn(opts = {}) {
  const value = process.env.FJORD_SESSION;
  if (!value) return opts;
  const cookie = { name: 'fjord_session', value, domain: '127.0.0.1', path: '/', httpOnly: true, sameSite: 'Strict' };
  return { ...opts, storageState: { cookies: [cookie], origins: [] } };
}

// Playwright's networkidle never comes on a fjord page: /api/events stays open
// for good (and a followed log with it), so every load waited out its 30 s.
// settled waits for the page's own requests instead -- none in flight for
// 500 ms -- and gives up quietly, as the control a test waits for next is
// the real check.
const LIVE = /\/api\/events$|[?&]follow=1/;
const inFlight = new WeakMap();
function track(page) {
  if (inFlight.has(page)) return;
  const open = new Set();
  inFlight.set(page, open);
  page.on('request', (r) => { if (!LIVE.test(r.url())) open.add(r); });
  page.on('requestfinished', (r) => open.delete(r));
  page.on('requestfailed', (r) => open.delete(r));
}
async function settled(page, timeout = 30000) {
  track(page);
  const open = inFlight.get(page);
  const end = Date.now() + timeout;
  let quietSince = Date.now();
  while (Date.now() < end) {
    if (open.size) quietSince = Date.now();
    else if (Date.now() - quietSince >= 500) return;
    await new Promise((r) => setTimeout(r, 100));
  }
}
// openPage loads a page of fjord and waits for it to settle.
async function openPage(page, url) {
  track(page);
  await page.goto(url, { waitUntil: 'load' });
  await settled(page);
}

module.exports = { signedIn, settled, openPage };
