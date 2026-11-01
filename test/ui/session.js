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
  await keepStreams(page);
  await page.goto(url, { waitUntil: 'load' });
  await settled(page);
}

// An action's output (Start, Apply, Update...) is one streamed answer the
// page reads as it comes. Playwright's response.text() gets nothing for it,
// so the page keeps a copy as it reads: fetch is wrapped to tee every POST
// under /api/stacks/, handing the page the same stream.
const keeping = new WeakSet();
async function keepStreams(page) {
  if (keeping.has(page)) return;
  keeping.add(page);
  await page.addInitScript(() => {
    const fetch0 = window.fetch;
    window.__streams = [];
    window.fetch = async (input, init) => {
      const res = await fetch0(input, init);
      const url = typeof input === 'string' ? input : input.url;
      const method = (init?.method || (typeof input === 'string' ? 'GET' : input.method) || 'GET').toUpperCase();
      if (method !== 'POST' || !/\/api\/stacks\//.test(url) || !res.body) return res;
      const [mine, kept] = res.body.tee();
      const rec = { url: new URL(url, location.href).pathname, text: '', done: false };
      window.__streams.push(rec);
      (async () => {
        const r = kept.getReader();
        const d = new TextDecoder();
        for (;;) {
          const { done, value } = await r.read();
          if (done) break;
          rec.text += d.decode(value, { stream: true });
        }
      })().catch(() => {}).finally(() => (rec.done = true));
      return new Response(mine, { status: res.status, statusText: res.statusText, headers: res.headers });
    };
  });
}
// streamed is the whole output of the last POST whose path matches re, once
// it has ended: '' if there was none or it has not ended within timeout.
async function streamed(page, re, timeout = 180000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const rec = await page.evaluate((src) => {
      const re = new RegExp(src);
      return (window.__streams || []).filter((s) => re.test(s.url)).pop() || null;
    }, re.source).catch(() => null);
    if (rec?.done) return rec.text;
    await new Promise((r) => setTimeout(r, 500));
  }
  return '';
}

module.exports = { signedIn, settled, openPage, keepStreams, streamed };
