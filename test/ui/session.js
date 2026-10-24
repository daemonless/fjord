// signedIn adds the session scripts/ui-tests.sh signed in with to a browser
// context's options, so a test starts inside fjord, not on its sign-in page.
// No FJORD_SESSION (sign-in off): the options as given.
function signedIn(opts = {}) {
  const value = process.env.FJORD_SESSION;
  if (!value) return opts;
  const cookie = { name: 'fjord_session', value, domain: '127.0.0.1', path: '/', httpOnly: true, sameSite: 'Strict' };
  return { ...opts, storageState: { cookies: [cookie], origins: [] } };
}

module.exports = { signedIn };
