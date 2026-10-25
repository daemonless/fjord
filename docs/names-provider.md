# Names for apps: the provider contract

fjord can give every app a name like `radarr.apps.lan` instead of an
address and port. It does not do the naming itself: an app installed from
the store does, a **names provider**. The daemonless reverse proxy is one.
Anything that serves the five calls below can be one too, and gets the
same fjord UI: a name row in the install wizard, names as the Open links,
the menu beside Open, and the trust mark.

## Declaring it

The app's catalog manifest says how fjord reaches it:

```yaml
x-fjord:
  provides:
    names:
      port: 8443                      # container port of the HTTPS API
      key: ${RP_PAIRING_KEY}          # a secret fjord makes up at install
      setup_token: ${RP_SETUP_TOKEN}  # optional: opens the app's own setup
      client_url: ${RP_PAIRING_URL}   # optional: fjord's own address
      client_id: ${RP_PAIRING_ID}     # optional: fjord's permanent id
      client_name: ${RP_PAIRING_NAME} # optional: "fjord on netlab"
```

`key` and `setup_token` are variables of the manifest with `type: secret`
and no default. fjord fills them at install, passes them to the container
as usual, and keeps them in the stack's `.env`. The app must accept the
key as a bearer token from its first start; the person never sees it.
`client_url` is a plain variable: fjord fills it with the address the
browser reached fjord at (`http://netlab:3567`), so the app can show a
way back to fjord. It is a hint, not an API address. `client_id` and
`client_name` are this fjord's permanent id and its name: several fjords
can use one app, and it tells them apart by the id (the refs of the names
each fjord makes carry it, `fjord:<id>:<stack>`) and lists them by name.

## Linking a fjord that did not install the app

An app installed elsewhere is linked by hand: the app's admin makes a
one-time code (ten minutes, once) and shows it as a link,
`https://<app>/link#code=XXXX-XXXX`; the person pastes that link into
fjord (Settings, Names, Link a reverse proxy). fjord calls
`POST /api/v1/pair` with `{code, client: {id, name, url}}` and gets
`{key, domain, proxyName, needed, fingerprint, rootUrl}` back: a key of
its own, and what it needs to pin the app's root (next section). The
first call cannot be verified, since the root is what it is about to
learn; the freshly copied code is what makes the exchange the person's.

fjord reaches the API where a browser would open the stack: the host's
published port for `port`, or the container's own address. Every call is
HTTPS with `Authorization: Bearer <key>`.

## Trust

Until the app's setup is done it may serve any certificate; fjord's calls
are not verified then. Once `GET /api/v1/status` reports setup complete
and a domain, fjord calls `GET /api/v1/trust`, fetches the root
certificate it points at, checks its SHA-256 against the fingerprint in
that answer, and pins it in fjord's secrets folder. Every later call is
verified against that root, with `proxyName` as the server name. An app
with public certificates answers `needed: false` and the system roots
apply. If the pinned certificate stops matching and the app says it is
back at first run, the pin is dropped and the next setup pins afresh.

## The calls

All JSON. Errors are `{"error": "..."}` with a 4xx or 5xx status; a
refused key is 401, which fjord shows as "disconnected".

`GET /api/v1/status`

```json
{
  "setup": {"complete": true},
  "domain": "apps.lan",
  "proxyName": "proxy.apps.lan",
  "trustUrl": "http://proxy.apps.lan/trust"
}
```

`setup.complete` false means the app still wants its own first run; fjord
then offers the setup token. `domain` is the ending every name gets.
`proxyName` is the app's own name: a browser that loads
`https://<proxyName>/trust/check` trusts the app, one that loads it over
plain HTTP can at least resolve names. `trustUrl` is the page where a
person makes a device trust it.

`GET /api/v1/trust`

```json
{"needed": true, "fingerprint": "5A:16:...", "rootUrl": "/trust/root.crt"}
```

`GET /api/v1/hosts` → `{"hosts": [...]}`, every name the app serves. fjord
recognises its own by `ref`.

`POST /api/v1/hosts` with

```json
{
  "name": "radarr",
  "target": {"address": "192.168.1.20", "port": 7878, "https": false, "acceptUntrusted": false},
  "access": "home",
  "ref": "fjord:<host>:<stack>"
}
```

answers the host as `GET` lists it, with `id`, `url` (where the name
opens) and `names`. `ref` is fjord's handle: `fjord:<fjord host>:<stack>`,
or `fjord:<host>:<stack>/<service>` for one app of a stack of several.
Keep it and give it back as sent.

`PUT /api/v1/hosts/{id}` with the same body replaces the host. fjord uses
it when an app's address or port changed, so the name follows, and when a
person changes the name from the app's page (Change name beside Open):
the same host with a new `name`. Refuse a taken name with a 4xx and say
whose it is; fjord shows your words.

`DELETE /api/v1/hosts/{id}` removes the name. Keeping it restorable for a
while is the app's choice; fjord's Undo re-creates it with `POST`.

`GET /api/v1/probe?address=...&port=...`

```json
{"reachable": true, "https": false, "untrustedCert": false}
```

fjord asks before every `POST`: a name that opens nothing is worse than
none, so an app the provider cannot reach gets no name and the person is
told why. `https` and `untrustedCert` fill the target.

The reverse proxy's full API, with these calls and its own, is served by
it at `/api/v1/openapi.json`.
