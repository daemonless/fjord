<script lang="ts">
  import { onMount, createEventDispatcher } from 'svelte';
  import * as yaml from 'js-yaml';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import DirPicker from './DirPicker.svelte';
  import { addressProblem, usableRange, randomMAC } from './network';
  import ServiceResources from './ServiceResources.svelte';
  import { resolveDefault, seedInterfaces, splitPlan, joinable, type Iface } from './planSeed';

  // sources: every catalog offering this app; the user picks one (Repository)
  // when there's more than one. Each carries its own manifest_url + variants.
  export let sources: {
    catalog: string;
    catalog_name: string;
    catalog_icon?: string;
    manifest_url: string;
    version?: string;
    variants: any[];
  }[] = [];
  export let appName: string;
  export let appId = ''; // for host-path placeholder suggestions (/containers/<id>/...)
  export let appClass = ''; // "stack" = multi-image compose (no global tag rewrite)
  // Set while the daemon is deciding, and to its reason when it says no. The
  // wizard stays up for both: a refusal that arrives after the form is gone
  // costs every answer in it, not just the one that was wrong.
  export let busy = false;
  export let submitError = '';

  // App data locations (Settings): the first is the default; with more than one
  // the wizard shows a picker. The choice drives path hints + {{appdata}}.
  let appDataLocations: string[] = [];
  let appDataChoice = '';
  $: storageBase = appDataChoice || appDataLocations[0] || '/containers';
  // Path hints are seeded when the manifest loads, so the App data location
  // must be known first: loadManifest awaits this. Racing it seeded
  // "/containers/<app>/cache" on a host whose App data lives elsewhere.
  const storageReady = (async () => {
    try {
      const sres = await fetch('/api/settings/storage');
      if (sres.ok) {
        const d = await sres.json();
        appDataLocations = d.locations || [d.base];
        appDataChoice = appDataLocations[0] || '';
      }
    } catch {
      // keep the default base
    }
  })();
  // Storage slug of the install name -- mirrors the server's storageSlug():
  // lowercase, runs of non-alphanumerics collapsed to "-", app id as fallback.
  const slugOf = (name: string, fallback: string) =>
    name.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || fallback;
  // Suggested host directory for a path variable:
  // <app data>/<app folder>/<var-lowercased-sans-suffix>.
  const pathHint = (v: any) =>
    `${storageBase}/${slugOf(stackName, appId || 'app')}/${String(v.name).toLowerCase().replace(/[._]?path$/, '').replace(/[^a-z0-9]+/g, '-') || 'data'}`;
  // {{stack}} / {{appdata}} placeholders in host paths are expanded server-side
  // at install ({{base}} is the older spelling); this previews the result.
  // Reactive on purpose: a `$:`-assigned function re-evaluates in the template
  // whenever the name or base changes.
  const isTemplated = (p: string) => /\{\{/.test(p || '');
  // Split a label into text and http(s) URL segments so URLs render as links
  // without ever treating the label as HTML. Trailing punctuation stays text.
  const linkify = (text: string): { text: string; url?: string }[] =>
    (text || '').split(/(https?:\/\/[^\s<>"']+?)(?=[.,;:!?)\]]*(?:\s|$))/).map((part, i) =>
      i % 2 ? { text: part, url: part } : { text: part },
    );
  // nfs://server/export and smb://user@server/share rows become named volumes
  // at install; they're shown with a kind badge and have nothing to browse.
  const remoteKind = (p: string) => (/^(nfs|smb):\/\//i.exec(p || '')?.[1] || '').toUpperCase();
  $: previewOf = (p: string) =>
    (p || '')
      .replace(/\{\{\s*stack\s*\}\}/g, slugOf(stackName, appId || 'app'))
      .replace(/\{\{\s*(appdata|base)\s*\}\}/g, storageBase);

  // Host-mode fjordd can browse the host filesystem; a container can't.
  let hostMode = false;
  // Runtime engines available for this install (podman, appjail, ...).
  let engines: { name: string; available: boolean; default: boolean; warning?: string }[] = [];
  let engineChoice = '';
  $: availableEngines = engines.filter((e) => e.available);
  onMount(async () => {
    try {
      const res = await fetch('/api/settings/wizard');
      if (res.ok) {
        const detail = Number((await res.json()).detail) || 1;
        showOptions = detail >= 2;
        advancedOpen = detail >= 3;
      }
    } catch {
      // default disclosure
    }
    try {
      const res = await fetch('/api/about');
      if (res.ok) hostMode = (await res.json()).mode === 'host';
    } catch {
      // leave browsing off; the plain field still works
    }
    try {
      const res = await fetch('/api/engine');
      if (res.ok) {
        const d = await res.json();
        engines = d.engines || [];
        engineChoice = d.default || 'podman';
      }
    } catch {
      // no engine info -> the server falls back to its default
    }
  });

  let picking: { name: string; i: number } | null = null; // host-path row whose directory is being picked

  let stackName = appId; // editable install name; defaults to the app id
  // Mirrors the server's ValidDisplayName: a human label, not a path.
  $: validName = /^[\p{L}\p{N}][\p{L}\p{N} ._-]{0,63}$/u.test(stackName);

  let sourceIdx = 0;
  $: activeSource = sources[sourceIdx] ?? sources[0];
  $: manifestUrl = activeSource?.manifest_url ?? '';
  $: variants = activeSource?.variants ?? []; // {id, label, default, image}

  const dispatch = createEventDispatcher();

  let loading = true;
  let error: string | null = null;
  let manifestText = '';
  let variables: any[] = [];
  let formData: Record<string, string> = {};
  // Deploy target = train (build/channel, from the catalog) + version (rolling
  // or a pin, from the registry). trains[trainId] = published pins for it.
  let train = '';
  let versionTag = ''; // '' = rolling; else a specific pinned tag
  let customTag = '';
  let trains: Record<string, { version: string; tag: string }[]> = {};
  let loadingVersions = false;

  // Trains come from the registry (what's actually published); labels come from
  // the catalog variants where known. Until the registry list loads (or if it's
  // unreachable) fall back to the catalog variants.
  $: trainList =
    Object.keys(trains).length > 0
      ? (() => {
          const seen = new Set<string>();
          const list: { id: string; label: string }[] = [];
          for (const v of variants) {
            if (trains[v.id] && !seen.has(v.id)) {
              list.push({ id: v.id, label: v.label });
              seen.add(v.id);
            }
          }
          for (const id of Object.keys(trains)) {
            if (!seen.has(id)) {
              list.push({ id, label: variants.find((v) => v.id === id)?.label || id });
              seen.add(id);
            }
          }
          return list;
        })()
      : variants.map((v) => ({ id: v.id, label: v.label }));

  // Keep `train` valid as the list resolves (initial pick + fix if it drops out).
  // The catalog's default variant id may carry a version the registry's train
  // ids don't ("3.4-pkg-latest" vs train "pkg-latest"), so match on the id
  // with any leading version stripped too. Never land on a development train
  // by accident: those are last resort, after any stable one.
  const isDevTrain = (id: string) => /(^|-)(devel|dev|edge|nightly|rc|beta|alpha)(-|$)/i.test(id);
  const stripVersion = (id: string) => id.replace(/^v?\d+(\.\d+)*-/, '');
  $: if (trainList.length && !trainList.some((t) => t.id === train)) {
    const def = variants.find((v) => v.default)?.id ?? '';
    const has = (id: string) => trainList.some((t) => t.id === id);
    // `latest` is the tag the image's compose ships with -- by convention an
    // alias of the default variant (`pkg`, `3.4-pkg-latest`, ...) -- so it is
    // the first choice whenever the registry publishes it AND the image still
    // declares it. An image that drops its upstream-binary build keeps the old
    // `latest` tag in the registry forever, frozen at whatever it was: for
    // tailscale that is 1.92.3 against 1.102.4 on every train it actually
    // builds. Undeclared means leftover, not current.
    //
    // No variants at all means the catalog says nothing either way, so the
    // convention still holds -- this must not regress an image with no variant
    // metadata.
    const declared = (id: string) => !variants.length || variants.some((v: any) => v.id === id);
    const pick =
      (has('latest') && declared('latest') && 'latest') ||
      (def && has(def) && def) ||
      (def && has(stripVersion(def)) && stripVersion(def)) ||
      ['pkg-latest', 'pkg'].find(has) ||
      trainList.find((t) => !isDevTrain(t.id))?.id ||
      trainList[0].id;
    train = pick;
  }

  // The actual image tag deployed: a custom override, else the pin, else the
  // train's rolling tag (which is the train id itself).
  $: tag = customTag.trim() || versionTag || train;

  // image_tag variables (stacks): each selects the tag of one component image.
  // Changing the train re-points every untouched one whose registry actually
  // publishes that train; the primary image's var follows the full tag pick
  // (train/pin/custom). Hand-edited fields are never clobbered.
  let repoTrains: Record<string, Record<string, { version: string; tag: string }[]>> = {};
  const touched = new Set<string>();
  $: tagVars = variables.filter((v) => v.type === 'image_tag' && v.image);

  // Progressive disclosure, three levels (placement only; whether a field may
  // be left empty is `optional`, checked separately in missingRequired):
  //   primary  -- shown up front: required vars with no default, plus anything
  //               a manifest pins with `level: primary`
  //   options  -- things people change sometimes: paths, ports, image tags
  //   advanced -- change once or never: env strings (PUID/TZ), managed dirs
  // `advanced: true|false` is the older spelling. Everything else follows its type.
  const levelOf = (v: any): 'primary' | 'options' | 'advanced' => {
    if (v.level === 'primary' || v.level === 'options' || v.level === 'advanced') return v.level;
    const hasDefault = String(v.default ?? '') !== '';
    if (v.optional !== true && !hasDefault && v.type !== 'path' && v.type !== 'zfs_dataset') return 'primary';
    if (v.advanced === true) return 'advanced';
    if (v.advanced === false) return 'options';
    return v.type === 'path' || v.type === 'port' || v.type === 'image_tag' ? 'options' : 'advanced';
  };
  $: primaryVars = variables.filter((v) => levelOf(v) === 'primary');
  $: optionVars = variables.filter((v) => levelOf(v) === 'options');
  $: advancedVars = variables.filter((v) => levelOf(v) === 'advanced');
  let showOptions = false;
  let advancedOpen = false; // Settings → Advanced can pre-open these (wizard detail 2/3)
  // One line that states the defaults being accepted, so nobody has to open
  // Options just to learn what they'd get.
  // What the rolling channel tag actually resolves to, asked of the registry
  // by digest. Not the newest pin in the train: those disagree for any image
  // carrying tags from a retired scheme, and the summary then names a version
  // the install will not produce -- tailscale read "v1.92.3" while the tag it
  // would pull was 1.102.4.
  let rollingVersion = '';
  let rollingFor = '';
  async function loadRolling(img: string, t: string) {
    const key = `${img}@${t}`;
    if (!img || !t || rollingFor === key) return;
    rollingFor = key;
    rollingVersion = '';
    try {
      const r = await fetch(`/api/registry/rolling?image=${encodeURIComponent(img)}&train=${encodeURIComponent(t)}`);
      if (r.ok && rollingFor === key) rollingVersion = (await r.json()).version ?? '';
    } catch {
      // registry unreachable -> fall back to naming the tag, which is still
      // exactly what gets deployed.
    }
  }
  $: loadRolling(imageRepo(), train);

  $: summaryVersion = (() => {
    if (customTag.trim()) return customTag.trim();
    if (versionTag) return versionTag;
    // The tag itself when the version is not known: honest, and it is what
    // the install actually asks the registry for.
    return rollingVersion ? `v${rollingVersion}` : train ? `:${train}` : 'latest';
  })();
  $: summaryTrain = trainList.length > 1 ? trainList.find((t) => t.id === train)?.label || '' : '';

  // A field is required unless the manifest marks it optional; the server
  // rejects a required var whose value resolves empty. Track the unfilled ones
  // so Deploy can be blocked with a clear reason instead of a 400 after submit.
  const isEmpty = (v: any) => !String(formData[v.name] ?? '').trim();
  // Host-path variables are folder LISTS (one row per folder); every other
  // variable is a single value in formData. Deps are spelled out (formData,
  // paths): a legacy-mode `$:` only tracks variables named in the statement.
  $: missingRequired = variables.filter(
    (v) =>
      v.optional !== true &&
      !(v.type === 'path' ? (paths[v.name] || []).some((p) => p.trim()) : String(formData[v.name] ?? '').trim()),
  );

  // Folder lists per host-path variable. One folder mounts as-is; several are
  // mounted as sub-folders of the app's own mount point (server-side expansion).
  let paths: Record<string, string[]> = {};
  // Folder sets (Settings → Storage): named host-folder sets the user can drop
  // into any list. fjord never guesses which app wants which set.
  let folderSets: { id: string; name: string; folders: string[]; match?: string }[] = [];
  // Sets applied as defaults (var name -> set name), for the caption under the list.
  let defaultedFrom: Record<string, string> = {};
  // A set whose "Applies to" keywords hit the variable name seeds that field's
  // folders -- still fully editable. Runs after the manifest AND the sets have
  // loaded (either order); never touches a field the user already edited.
  function applyDefaultSets() {
    for (const v of variables) {
      if (v.type !== 'path' || touched.has(v.name) || defaultedFrom[v.name]) continue;
      const up = String(v.name).toUpperCase();
      const set = folderSets.find((s) => (s.match || '').split('|').some((k) => k && up.includes(k)) && s.folders.length);
      if (!set) continue;
      paths[v.name] = [...set.folders];
      defaultedFrom[v.name] = set.name;
    }
    paths = paths;
    defaultedFrom = defaultedFrom;
  }
  function addFolder(name: string, value = '') {
    paths[name] = [...(paths[name] || []), value];
    paths = paths;
    touched.add(name);
  }
  function removeFolder(name: string, i: number) {
    const rest = (paths[name] || []).filter((_, x) => x !== i);
    paths[name] = rest.length ? rest : [''];
    paths = paths;
    touched.add(name);
  }
  // Drop a set's folders into the list: an untouched seeded row is replaced,
  // anything the user typed is kept and the set is appended (no duplicates).
  function addFolderSet(name: string, setId: string) {
    const lib = folderSets.find((l) => l.id === setId);
    if (!lib) return;
    const cur = touched.has(name) ? (paths[name] || []).filter((p) => p.trim()) : [];
    for (const f of lib.folders) if (!cur.includes(f)) cur.push(f);
    paths[name] = cur.length ? cur : [''];
    paths = paths;
    touched.add(name);
  }
  async function loadRepoTrains() {
    for (const v of tagVars) {
      if (repoTrains[v.image]) continue;
      try {
        const res = await fetch(`/api/registry/versions?image=${encodeURIComponent(v.image)}`);
        if (res.ok) {
          repoTrains[v.image] = await res.json();
          repoTrains = repoTrains;
        }
      } catch {}
    }
  }
  $: if (tagVars.length) loadRepoTrains();
  $: if (tag || repoTrains || tagVars) syncTagVars();
  function syncTagVars() {
    for (const v of tagVars) {
      if (touched.has(v.name)) continue;
      if (v.image === imageRepo()) {
        formData[v.name] = tag;
      } else if (repoTrains[v.image]?.[train]) {
        formData[v.name] = train;
      }
    }
    formData = formData;
  }

  function imageRepo(): string {
    const img = variants[0]?.image || '';
    const slash = img.lastIndexOf('/');
    const colon = img.lastIndexOf(':');
    return colon > slash ? img.slice(0, colon) : img;
  }

  async function loadVersions() {
    if (!variants.length) return;
    loadingVersions = true;
    try {
      const res = await fetch(`/api/registry/versions?image=${encodeURIComponent(imageRepo())}`);
      if (res.ok) trains = await res.json();
    } catch {
      // registry unreachable -> rolling-only, no pins
    } finally {
      loadingVersions = false;
    }
  }

  // A stack whose services declare network_mode shares the host's network
  // stack and addresses its own parts over localhost. That used to remove the
  // picker outright, which left the section a statement with nothing to do:
  // no way to say "put it on my LAN anyway", and no hint that it can be
  // changed later. The hazard is real, so it is said plainly -- but it is the
  // operator's call, and attaching clears the mode for the services it moves.
  $: hostNetworked = /^\s*network_mode\s*:/m.test(manifestText);
  // An app that declares x-fjord.networking has said which of its services is
  // the one people open and where the rest belong, so it knows how to be put
  // on a network and the warning does not apply to it.
  // The app's services, and its own answer to where each belongs. The plan is
  // editable: what the app suggests is a default, not a decision.
  let svcNames: string[] = [];
  let netPlan: Record<string, string> = {};
  $: declaresNetworking = Object.keys(netPlan).length > 0;
  // The interfaces the operator edits, one list per service -- the same editor
  // the stack page uses, so there is one way to read and change this and not
  // two. Seeded from the app's own declaration and NOT re-seeded afterwards:
  // recomputing it on every reactive pass threw each edit away as it was made.
  let planEdits: Record<string, Iface[]> = {};
  let seededFor = '';
  $: {
    // "default" can only be resolved once the networks it has to become are
    // in hand, so the seed waits for them and re-runs if the engine (and so
    // the network list) changes.
    const key = `${svcNames.join(',')}|${JSON.stringify(netPlan)}|${engineChoice}|${netChoice}|${networksLoaded}|${hostNetworked}`;
    if (declaresNetworking && networksLoaded && key !== seededFor) {
      seededFor = key;
      // How the app already arranges itself, for the case where this host has
      // no network to offer: then it is installed as it ships rather than
      // being split across a built-in and a private segment.
      planEdits = seedInterfaces(
        svcNames,
        resolveDefault(netPlan, networks, netChoice, hostNetworked ? 'host' : 'bridge'),
      );
    }
  }
  // The services as the editor wants them. Nothing is running yet, so a name
  // is all there is to say about each.
  $: planServices = svcNames.map((name) => ({ name }));
  // Networks an app can actually be PUT on: another stack's private segment is
  // not one, and one is left behind by every multi-service install.
  $: joinableNets = joinable(networks);
  // What the blurb promises has to be what the rows say, or the screen gives
  // two answers.
  $: exposedOn = Object.entries(planEdits).find(([svc]) => netPlan[svc] === PLAN_DEFAULT)?.[1]?.[0]?.network ?? '';
  // An appjail stack is a director project, and a director cannot put a jail
  // on the host's stack: that is a jail parameter. The daemon refuses it, so
  // the picker must not offer it.
  $: isDirectorStack = /^\s{2,}appjail:\s*$/m.test(manifestText);
  $: wizardUnsupported = engineChoice === 'appjail' && isDirectorStack ? ['host'] : [];
  const PLAN_PRIVATE = 'private';
  const PLAN_DEFAULT = 'default';
  const PLAN_NONE = 'none';
  // The address and MAC describe ONE interface, so they belong to whichever
  // service sits on the chosen network -- not to "the app", which with four
  // Attachable networks (empty on hosts without them). A network with no
  // subnet gets its addresses from DHCP -- nothing here needs to know the
  // segment, and no address has to be supplied.
  type Network = { name: string; subnet?: string; static?: boolean; addressSource?: string };
  let networks: Network[] = [];
  let netChoice = '';
  // appjail: false means a director project cannot take it -- both are jail
  // parameters rather than director options, so install would refuse.
  const BUILT_IN = [
    { name: 'bridge', detail: 'A private address behind NAT, reached on the ports it publishes on this host.' },
    { name: 'host', detail: "No address or port mapping of its own — it binds this host's ports directly.", appjail: false },
    { name: 'none', detail: 'No network at all: nothing in and nothing out.' },
  ];
  const builtIn = (name: string) => BUILT_IN.some((b) => b.name === name);
  $: chosenBuiltIn = BUILT_IN.find((b) => b.name === (netChoice || 'bridge'));
  // A default of host or none is fine until the engine is appjail, which has
  // no director option for either. The option was already grayed out, but it
  // stayed SELECTED -- so the install went ahead and came back 400. Falling
  // back to bridge keeps the operator's intent (no address of its own) on an
  // engine that can honour it.
  $: if (engineChoice === 'appjail' && BUILT_IN.some((b) => b.name === netChoice && b.appjail === false)) {
    netChoice = 'bridge';
  }
  let netIP = '';
  let netMAC = '';
  // How a network hands out addresses, in the words the form uses. Read from
  // what the daemon reports -- "no subnet means DHCP" was the old tell and
  // stopped being true when DHCP networks began recording their segment.
  const allocLabel = (n: { addressSource?: string; subnet?: string }) =>
    n.addressSource === 'dhcp'
      ? n.subnet ? `DHCP · ${n.subnet}` : 'DHCP'
      : n.addressSource === 'static'
        ? n.subnet ? `static · ${n.subnet}` : 'static'
        : n.subnet || '';

  $: chosenNet = networks.find((n) => n.name === netChoice);
  // Checked here, not just at the daemon. The daemon's refusal arrives after
  // Install, and the wizard is gone by then -- a network address typed into
  // this field cost the operator every other answer in the form.
  $: ipProblem = netChoice && !builtIn(netChoice) ? addressProblem(netIP, chosenNet) : '';
  // What may go in the field, said forwards -- the subnet in the picker does
  // not answer it, since three of its addresses are spoken for.
  $: ipRange = netChoice && !builtIn(netChoice) ? usableRange(chosenNet) : '';
  // appjail cannot draw from the pool the podman side's IPAM manages, so on a
  // pool network it needs an address given to it. On DHCP nothing does.
  // Keyed on WHO allocates, which the network reports. Inferring it from "has
  // a subnet" was wrong twice over: a DHCP network records its segment, and an
  // appjail virtualnet has a CIDR that appjail itself allocates from -- so
  // both were demanding an address that neither needs.
  $: ipRequired =
    !!netChoice &&
    (chosenNet?.addressSource === 'static' ||
      (engineChoice === 'appjail' && chosenNet?.addressSource === 'pool'));
  // ...and a required field cannot hide. Networking sits in the Options
  // disclosure, collapsed at low wizard-detail settings -- and it used to sit
  // inside Advanced INSIDE Options, two disclosures deep, so a static network
  // left Deploy disabled with the field that would satisfy it out of sight and
  // no reason on screen.
  $: if (ipRequired && !netIP.trim()) showOptions = true;
  // Same for an address that won't work: Install is disabled over it, so the
  // reason cannot be folded away inside Options where nobody sees it.
  $: if (ipProblem) showOptions = true;

  // Scoped to the engine being installed on. The unscoped list spans both, so
  // it offers networks the chosen engine cannot attach to -- appjail's own
  // virtualnets to a podman install -- and the install fails at the last step
  // on something the form suggested. Re-runs when the engine changes.
  // Whether /api/networks has answered. "default" must not be resolved before
  // it has: an empty list made every app fall through to bridge, which is not
  // what the manifest meant and cannot be undone once written.
  let networksLoaded = false;
  async function loadNetworks() {
    const engine = engineChoice ? `?engine=${encodeURIComponent(engineChoice)}` : '';
    try {
      const res = await fetch(`/api/networks${engine}`);
      networks = res.ok ? await res.json() : [];
      networksLoaded = true;
    } catch {
      networks = [];
      networksLoaded = true;
    }
    // Whatever was picked may not exist for this engine.
    if (netChoice && !builtIn(netChoice) && !networks.some((n) => n.name === netChoice)) netChoice = '';
    applyDefaultNetwork();
  }
  $: engineChoice, loadNetworks();

  // The operator's default, applied once the list it has to exist in is
  // loaded. Both arrive asynchronously and in no fixed order, so each calls
  // this and it acts when both are in hand.
  // Per engine, and only per engine: a network only one engine can use cannot
  // be everyone's default, so there is no shared value to fall back to.
  let defaultFor: Record<string, string> = {};
  $: wantedNetwork = engineChoice ? defaultFor[engineChoice] || '' : '';
  let defaultNetworkKnown = false;
  function applyDefaultNetwork() {
    if (!defaultNetworkKnown || netChoice || !wantedNetwork) return;
    // Only when it exists for THIS engine -- one renamed, removed, or simply
    // not attachable here would otherwise fail every install.
    // A built-in exists on every host and for every engine, so it needs no
    // lookup -- without this, setting one as the default did nothing at all.
    if (builtIn(wantedNetwork) || networks.some((n) => n.name === wantedNetwork)) netChoice = wantedNetwork;
  }

  onMount(async () => {
    try {
      const dres = await fetch('/api/settings/network');
      if (dres.ok) {
        const d = await dres.json();
        defaultFor = d.forEngine || {};
      }
      defaultNetworkKnown = true;
      applyDefaultNetwork();
    } catch {
      // no networks -> host ports only
    }
    await storageReady;
    try {
      const lres = await fetch('/api/folder-sets');
      if (lres.ok) {
        folderSets = (await lres.json()).sets || [];
        applyDefaultSets();
      }
    } catch {
      // no folder sets -> plain path fields
    }
  });

  // (Re)load the manifest whenever the chosen source changes. All per-manifest
  // state is reset so switching repository can't leak the previous one's
  // variables, trains or pins.
  let loadedIdx = -1;
  $: if (activeSource && sourceIdx !== loadedIdx) {
    loadedIdx = sourceIdx;
    loadManifest();
  }
  async function loadManifest() {
    loading = true;
    error = null;
    manifestText = '';
    variables = [];
    formData = {};
    paths = {};
    trains = {};
    train = '';
    versionTag = '';
    customTag = '';
    repoTrains = {};
    touched.clear();
    await storageReady;
    try {
      const res = await fetch(manifestUrl, { cache: 'no-store' });
      if (!res.ok) throw new Error('Failed to fetch manifest');
      manifestText = await res.text();
      const parsed: any = yaml.load(manifestText);
      svcNames = Object.keys(parsed?.services ?? {});
      {
        const declared: Record<string, string> = parsed?.['x-fjord']?.networking ?? {};
        const star = declared['*'];
        netPlan = {};
        // "*" is expanded here so every service is a row the operator can see
        // and change, rather than a wildcard they have to reason about.
        for (const n of svcNames) netPlan[n] = declared[n] ?? star ?? '';
        if (!Object.values(netPlan).some((v) => v)) netPlan = {};
      }
      if (parsed['x-fjord'] && parsed['x-fjord'].variables) {
        variables = parsed['x-fjord'].variables;
        variables.forEach((v) => {
          // Path vars with no default get a sensible <App data>/<app>/... one
          // so installs aren't blocked on an empty required field; still fully
          // editable + browsable.
          formData[v.name] = v.default || (v.type === 'path' ? pathHint(v) : '');
          if (v.type === 'path') paths[v.name] = [formData[v.name]];
        });
        formData = formData; // seeding above mutates in place; reassign so missingRequired recomputes
        paths = paths;
        defaultedFrom = {};
        applyDefaultSets();
      }
      loadVersions();
    } catch (e: any) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  // Send the raw manifest + values to the server-side install pipeline, which
  // resolves variables, provisions host dirs, renders the stack, and brings it
  // up. No client-side compose rendering.
  function deploy() {
    // Host-path vars: the first folder is the variable's value, the whole
    // (non-empty) list rides along for multi-folder expansion.
    // What the operator left in the per-service editor, split into the two
    // shapes the daemon takes. null when the app named no services, which is
    // when the stack-wide picker above is the answer instead.
    const plan = declaresNetworking ? splitPlan(planEdits) : null;
    const values: Record<string, string> = { ...formData };
    const pathLists: Record<string, string[]> = {};
    for (const v of variables) {
      if (v.type !== 'path') continue;
      const list = (paths[v.name] || []).map((p) => p.trim()).filter(Boolean);
      pathLists[v.name] = list;
      values[v.name] = list[0] ?? '';
    }
    dispatch('deploy', {
      name: stackName.trim(),
      engine: engineChoice,
      manifest: manifestText,
      values,
      paths: pathLists,
      appData: appDataChoice,
      // Stacks: the train pick lands via the image_tag variables; a global tag
      // rewrite would stomp every service (db/redis included).
      tag: appClass === 'stack' ? '' : tag.trim(),
      // The stack-wide form only when there is no per-service list. Sending
      // both let `network: 'host'` win over the whole plan and throw it away.
      network: plan ? '' : netChoice,
      mac: plan ? '' : netMAC.trim(),
      ip: plan ? '' : netIP.trim(),
      // The two things the daemon takes: interfaces to attach, and modes to
      // set. "private" rides along as a spec -- the segment has no name until
      // the install makes one.
      networks: plan?.networks,
      networkModes: plan?.modes,
    });
  }

  function close() {
    dispatch('close');
  }
</script>

<div class="fixed inset-0 bg-black/80 backdrop-blur-sm flex items-center justify-center z-50 p-4">
  <div class="bg-fjord-bg border border-fjord-border rounded-xl shadow-2xl w-full max-w-2xl flex flex-col overflow-hidden max-h-full">
    <div class="p-6 border-b border-fjord-border flex justify-between items-center bg-fjord-card">
      <h3 class="text-2xl font-bold text-fjord-fg">Install {appName}</h3>
      <button on:click={close} title="Close" class="p-1.5 rounded-full text-fjord-fg-muted hover:text-fjord-fg hover:bg-fjord-border"
        ><Icon name="close" size={16} /></button
      >
    </div>

    <div class="p-6 overflow-y-auto flex-1">
      {#if loading}
        <div class="flex items-center gap-3 text-fjord-fg-muted"><Spinner size={18} /> Loading configuration…</div>
      {:else if error}
        <p class="text-fjord-danger">{error}</p>
      {:else}
        <div class="space-y-5">
          <div class="flex flex-col gap-1">
            <label class="text-sm font-semibold text-fjord-fg-secondary" for="stackname">Name</label>
            <input
              id="stackname"
              bind:value={stackName}
              placeholder={appId}
              class="bg-fjord-inset border rounded-md px-3 py-2 text-fjord-fg-body font-mono focus:outline-none focus:border-fjord-accent transition-colors {stackName && !validName ? 'border-fjord-danger/60' : 'border-fjord-border'}"
            />
            {#if stackName && !validName}
              <div class="text-xs text-fjord-danger">Letters, digits, spaces, dots, dashes and underscores; up to 64 characters.</div>
            {:else}
              <div class="text-xs text-fjord-fg-dim">Change it to run more than one copy, e.g. <span class="font-mono">{appId}-4k</span>.</div>
            {/if}
          </div>

          {#snippet varField(v: any)}
            <div class="flex flex-col gap-1">
              <label class="text-sm font-semibold text-fjord-fg-secondary flex items-center gap-2" for={v.name}>
                <span class="font-mono text-fjord-accent/90">{v.name}</span>
                {#if v.optional !== true}<span class="text-fjord-danger" title="Required">*</span>{/if}
                {#if v.type === 'path'}<span class="text-[10px] font-normal uppercase tracking-wide text-fjord-fg-dim border border-fjord-border rounded px-1.5 py-0.5">host path</span>{/if}
              </label>
              {#if v.label}
                <div class="text-xs text-fjord-fg-dim">{#each linkify(v.label) as seg}{#if seg.url}<a href={seg.url} target="_blank" rel="noopener noreferrer" class="text-fjord-accent hover:underline">{seg.text}</a>{:else}{seg.text}{/if}{/each}</div>
              {/if}
              {#if v.type === 'zfs_dataset'}
                <div class="text-xs text-fjord-fg-dim">Auto-provisioned by fjord ({v.host_permissions?.uid ?? 1000}:{v.host_permissions?.gid ?? 1000}); no need to change.</div>
              {:else if v.type === 'image_tag'}
                <div class="text-xs text-fjord-fg-dim">
                  Tag for <span class="font-mono">{v.image}</span> — follows the build/channel above when that
                  image publishes it. Edit to override.
                </div>
              {:else if v.type === 'path'}
                <div class="text-xs text-fjord-fg-dim">Absolute path on the host — e.g. <span class="font-mono">{pathHint(v)}</span></div>
              {/if}
              {#if v.type === 'path'}
                <div class="flex flex-col gap-1.5">
                  {#each paths[v.name] || [''] as _, i}
                    <div class="flex items-center gap-2">
                      <input
                        id={i === 0 ? v.name : `${v.name}-${i}`}
                        type="text"
                        bind:value={paths[v.name][i]}
                        on:input={() => touched.add(v.name)}
                        placeholder={pathHint(v)}
                        class="flex-1 min-w-0 bg-fjord-inset border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent transition-colors {v.optional !== true && missingRequired.includes(v) ? 'border-fjord-danger/60' : 'border-fjord-border'}"
                      />
                      {#if remoteKind(paths[v.name][i])}
                        <span class="shrink-0 text-[10px] font-semibold uppercase tracking-wide text-fjord-fg-muted border border-fjord-border rounded px-1.5 py-0.5" title="Mounted as a named volume">{remoteKind(paths[v.name][i])}</span>
                      {/if}
                      {#if hostMode && !isTemplated(paths[v.name][i]) && !remoteKind(paths[v.name][i])}
                        <button
                          type="button"
                          on:click={() => (picking = { name: v.name, i })}
                          title="Browse the host filesystem"
                          class="shrink-0 px-3 py-2 rounded-md text-sm bg-fjord-border hover:bg-fjord-border/70 text-fjord-fg-body">Browse…</button
                        >
                      {/if}
                      {#if (paths[v.name] || []).length > 1}
                        <button type="button" on:click={() => removeFolder(v.name, i)} title="Remove folder" class="shrink-0 text-fjord-fg-dim hover:text-fjord-danger transition-colors"><Icon name="trash" size={13} /></button>
                      {/if}
                    </div>
                    {#if isTemplated(paths[v.name][i])}
                      <div class="text-xs text-fjord-fg-dim font-mono pl-1">→ {previewOf(paths[v.name][i])}</div>
                    {/if}
                  {/each}
                  <div class="flex items-center gap-3">
                    <button type="button" on:click={() => addFolder(v.name)} class="text-xs text-fjord-fg-muted hover:text-fjord-fg flex items-center gap-1"><Icon name="plus" size={12} /> Add folder</button>
                    {#if folderSets.length}
                      <select
                        aria-label="Add a folder set"
                        class="text-xs bg-fjord-inset border border-fjord-border rounded-md px-2 py-1 text-fjord-fg-secondary focus:outline-none focus:border-fjord-accent"
                        on:change={(e) => { addFolderSet(v.name, e.currentTarget.value); e.currentTarget.value = ''; }}
                      >
                        <option value="">Add folder set…</option>
                        {#each folderSets as l}<option value={l.id}>{l.name}</option>{/each}
                      </select>
                    {/if}
                  </div>
                  {#if defaultedFrom[v.name]}
                    <div class="text-xs text-fjord-fg-dim">Defaults from your <b class="text-fjord-fg-muted">{defaultedFrom[v.name]}</b> folder set — edit freely.</div>
                  {/if}
                  {#if (paths[v.name] || []).filter((p) => p.trim()).length > 1}
                    <div class="text-xs text-fjord-fg-dim">Several folders: each is mounted as its own sub-folder inside the container.</div>
                  {/if}
                </div>
              {:else}
                <input
                  id={v.name}
                  type="text"
                  bind:value={formData[v.name]}
                  on:input={() => touched.add(v.name)}
                  placeholder={v.default || (v.optional !== true ? 'required' : '')}
                  class="w-full bg-fjord-inset border rounded-md px-3 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent transition-colors {v.optional !== true && isEmpty(v) ? 'border-fjord-danger/60' : 'border-fjord-border'}"
                />
              {/if}
            </div>
          {/snippet}

          {#each primaryVars as v}{@render varField(v)}{/each}

          <!-- the defaults being accepted, in one line; Options is one click -->
          <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-fjord-fg-muted border-t border-fjord-border pt-4">
            <span>
              Installs <b class="text-fjord-fg-body">{appName} {summaryVersion}</b>{#if summaryTrain}{' '}({summaryTrain}){/if}
              on <b class="text-fjord-fg-body">{engineChoice || 'podman'}</b>
              · data in <span class="font-mono text-fjord-fg-secondary">{storageBase}/{slugOf(stackName, appId || 'app')}</span>
            </span>
            <button
              type="button"
              on:click={() => (showOptions = !showOptions)}
              aria-expanded={showOptions}
              class="ml-auto flex items-center gap-1 text-sm font-medium text-fjord-accent hover:underline"
              >Options <Icon name="chevron-right" size={13} class="transition-transform {showOptions ? 'rotate-90' : ''}" /></button
            >
          </div>

          {#if showOptions}
            <div class="space-y-5">
              {#if sources.length > 1}
          <div class="flex flex-col gap-1">
            <label class="text-sm font-semibold text-fjord-fg-secondary" for="repo">Repository</label>
            <div class="flex items-center gap-2">
              {#if activeSource?.catalog_icon}
                <div class="shrink-0 w-8 h-8 rounded-md bg-fjord-bg border border-fjord-border flex items-center justify-center overflow-hidden">
                  <img src={activeSource.catalog_icon} alt={activeSource.catalog_name} class="w-5 h-5 object-contain" />
                </div>
              {/if}
              <select
                id="repo"
                bind:value={sourceIdx}
                disabled={sources.length < 2}
                title={sources.length < 2 ? 'Only one catalog offers this app' : 'Choose which catalog to install from'}
                class="flex-1 bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {#each sources as s, i}
                  <option value={i}>{s.catalog_name}</option>
                {/each}
              </select>
            </div>
          </div>
              {/if}
          {#if variants.length}
            <div class="grid grid-cols-2 gap-3">
              <div class="flex flex-col gap-1">
                <label class="text-sm font-semibold text-fjord-fg-secondary" for="train">Build / channel</label>
                <select
                  id="train"
                  bind:value={train}
                  on:change={() => (versionTag = '')}
                  class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
                >
                  {#each trainList as t}
                    <option value={t.id}>{t.label}</option>
                  {/each}
                </select>
              </div>
              <div class="flex flex-col gap-1">
                <label class="text-sm font-semibold text-fjord-fg-secondary" for="ver">Version</label>
                <select
                  id="ver"
                  bind:value={versionTag}
                  class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
                >
                  <option value="">Latest (rolling)</option>
                  {#each trains[train] || [] as v}
                    <option value={v.tag}>{v.version}</option>
                  {/each}
                </select>
                {#if loadingVersions}<span class="text-xs text-fjord-fg-dim">Loading versions…</span>{/if}
              </div>
            </div>
          {/if}
          {#if appDataLocations.length > 1}
            <div class="flex flex-col gap-1">
              <label class="text-sm font-semibold text-fjord-fg-secondary" for="appdata">App data</label>
              <p class="text-xs text-fjord-fg-dim">Where this app's own folders go — <span class="font-mono">{storageBase}/{slugOf(stackName, appId || 'app')}/…</span></p>
              <select
                id="appdata"
                bind:value={appDataChoice}
                class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent"
              >
                {#each appDataLocations as l, i}
                  <option value={l}>{l}{i === 0 ? ' (default)' : ''}</option>
                {/each}
              </select>
            </div>
          {/if}

              {#each optionVars as v}{@render varField(v)}{/each}

          <!-- Not gated on there being any named networks: bridge, host and
               none are always choices, so gating the section on networks.length
               removed the whole control on a host that defines none -- and any
               time the engine-scoped fetch came back empty. The same gate was
               wrong on the stack's own picker and was removed there. -->
          {#snippet macField()}
              <div class="flex gap-2 mt-2">
                <input
                  type="text"
                  bind:value={netMAC}
                  placeholder="MAC (optional — pin one for a DHCP reservation)"
                  class="flex-1 bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent"
                />
                <!-- Fills a blank field only, so a MAC is never replaced by
                     accident: a reservation is keyed on it. -->
                <button
                  type="button"
                  on:click={() => (netMAC = randomMAC())}
                  disabled={!!netMAC.trim()}
                  title={netMAC.trim()
                    ? 'Clear the field first — changing a MAC breaks a DHCP reservation keyed on it'
                    : 'Generate a locally-administered address'}
                  class="shrink-0 px-3 rounded-md border border-fjord-border text-sm text-fjord-fg-secondary hover:bg-fjord-border disabled:opacity-30 disabled:hover:bg-transparent"
                  >Generate</button
                >
              </div>
            {/snippet}
            <div class="pt-4 border-t border-fjord-border">
              {#if declaresNetworking}
                <label class="text-sm font-semibold text-fjord-fg-secondary" for="net">Networking</label>
                {#if joinableNets.length}
                  <p class="text-xs text-fjord-fg-dim mb-3">
                    This app says which of its parts belongs where: the one you open goes on
                    <span class="font-mono">{exposedOn || joinableNets[0].name}</span>, and its
                    database and cache go on a private segment only it can reach. Change any of it
                    here, or after installing.
                  </p>
                {:else}
                  <!-- Promising a private segment while every row below says
                       "host" is two answers on one screen, and the rows are the
                       true one. -->
                  <p class="text-xs text-fjord-fg-dim mb-3">
                    There is no network on this host to put it on, so it is installed the way it
                    ships — every part together, answering on this host's address. Add a network
                    under Networks to give the part you open one of its own.
                  </p>
                {/if}
                <ServiceResources
                  services={planServices}
                  {networks}
                  bind:edits={planEdits}
                  planning
                  unsupportedModes={wizardUnsupported}
                  on:change={() => (planEdits = planEdits)}
                />
              {:else}
                <label class="text-sm font-semibold text-fjord-fg-secondary" for="net">Networking</label>
                {#if hostNetworked && !declaresNetworking}
                  <p class="text-xs text-fjord-warning mt-1 mb-2">
                    This app expects host networking — its services reach each other over
                    <span class="font-mono">localhost</span>. Giving it an address of its own means
                    they have to find each other by name or address instead, which this app has not
                    said it can do. Install it as it is and change it afterwards if you want to try.
                  </p>
                {:else}
                  <p class="text-xs text-fjord-fg-dim mb-2">How this app reaches the network.</p>
                {/if}
                <select
                  id="net"
                  bind:value={netChoice}
                  class="w-full bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
                >
                  <!-- The built-ins by name, the same three words the Networks
                       page and each stack's own picker use. "" is the historic
                       value for bridge, from before it had a name. -->
                  {#each BUILT_IN as b}
                    <option value={b.name} disabled={b.appjail === false && engineChoice === 'appjail'}>{b.name}</option>
                  {/each}
                  {#each networks as n}
                    <!-- Name and detail, the same shape as the built-ins above
                         and as the stack's own picker. "Own IP on vlan4" was a
                         sentence where every other list is a name. -->
                    <option value={n.name}>{n.name}{allocLabel(n) ? ` (${allocLabel(n)})` : ''}</option>
                  {/each}
                </select>
                {#if !netChoice || builtIn(netChoice)}
                  <p class="text-xs text-fjord-fg-dim mt-2">{chosenBuiltIn?.detail}</p>
                {:else if chosenNet?.addressSource === 'dhcp'}
                  <p class="text-xs text-fjord-fg-dim mt-2">
                    The address comes from the DHCP server on that segment, using the MAC below.
                  </p>
                  {@render macField()}
                {:else if netChoice}
                  <input
                    type="text"
                    bind:value={netIP}
                    placeholder={ipRequired ? 'IP (required on this engine)' : 'IP (optional — auto-assign if blank)'}
                    class="w-full mt-2 bg-fjord-inset border rounded-md px-3 py-2 text-fjord-fg-body font-mono text-sm focus:outline-none focus:border-fjord-accent {ipProblem
                      ? 'border-fjord-danger/60'
                      : 'border-fjord-border'}"
                  />
                  {#if ipProblem || ipRange}
                    <p class="text-xs mt-1">
                      {#if ipProblem}<span class="text-fjord-danger">{ipProblem}.</span>{/if}
                      {#if ipRange}<span class="text-fjord-fg-dim"
                          >Usable: <span class="font-mono text-fjord-fg-secondary">{ipRange}</span></span
                        >{/if}
                    </p>
                  {/if}
                  {#if ipRequired && !netIP.trim()}
                    <p class="text-xs text-fjord-warning mt-1">
                      {#if chosenNet?.addressSource === 'static'}
                        Nothing allocates on {netChoice} — give this app an address on
                        {chosenNet.subnet || 'that segment'}.
                      {:else}
                        {netChoice} draws from a range set aside for podman's IPAM, which appjail cannot ask —
                        give this jail an address from that range, or pick a DHCP network.
                      {/if}
                    </p>
                  {/if}
                  {@render macField()}
                {/if}
              {/if}
            </div>

              {#if advancedVars.length || availableEngines.length > 1 || variants.length}
                <details class="group border-t border-fjord-border pt-4" open={advancedOpen}>
                  <summary class="flex items-center gap-1.5 cursor-pointer text-sm font-semibold text-fjord-fg-secondary hover:text-fjord-fg select-none list-none">
                    <Icon name="chevron-right" size={14} class="transition-transform group-open:rotate-90" />
                    Advanced
                  </summary>
                  <div class="mt-4 space-y-5">
          {#if availableEngines.length > 1}
            <div class="flex flex-col gap-1">
              <label class="text-sm font-semibold text-fjord-fg-secondary" for="engine">Engine</label>
              <p class="text-xs text-fjord-fg-dim">The runtime this stack runs on — fixed once installed.</p>
              <select
                id="engine"
                bind:value={engineChoice}
                class="bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
              >
                {#each availableEngines as e}
                  <option value={e.name}>{e.name}{e.default ? ' (default)' : ''}</option>
                {/each}
              </select>
              {#if engines.find((e) => e.name === engineChoice)?.warning}
                <div class="flex items-start gap-1.5 text-xs text-fjord-warning mt-1">
                  <Icon name="alert" size={12} class="shrink-0 mt-0.5" /> <span>{engines.find((e) => e.name === engineChoice)?.warning}</span>
                </div>
              {/if}
            </div>
          {/if}
                    {#if variants.length}
            <div class="flex items-center gap-2">
              <input
                bind:value={customTag}
                placeholder="Custom tag (optional override)"
                class="flex-1 bg-fjord-inset border border-fjord-border rounded-md px-3 py-1.5 text-fjord-fg-body font-mono text-xs focus:outline-none focus:border-fjord-accent"
              />
              <span class="text-xs text-fjord-fg-dim shrink-0">→ <span class="font-mono text-fjord-fg-secondary">:{tag}</span></span>
            </div>
                    {/if}
                    {#each advancedVars as v}{@render varField(v)}{/each}
                  </div>
                </details>
              {/if}
            </div>
          {/if}
        </div>
      {/if}
    </div>

    <div class="p-4 border-t border-fjord-border bg-fjord-card flex items-center justify-end gap-3">
      {#if submitError}
        <p class="mr-auto text-xs text-fjord-danger">{submitError}</p>
      {/if}
      <button
        on:click={close}
        disabled={busy}
        class="px-4 py-2 rounded-md font-medium text-fjord-fg-secondary hover:text-fjord-fg hover:bg-fjord-border transition-all disabled:opacity-50">Cancel</button
      >
      <button
        on:click={deploy}
        disabled={busy || loading || !!error || !validName || missingRequired.length > 0 || (ipRequired && !netIP.trim()) || !!ipProblem}
        title={!validName
          ? 'Enter a valid stack name'
          : missingRequired.length
            ? `Fill required: ${missingRequired.map((v) => v.name).join(', ')}`
            : ipProblem
              ? ipProblem
              : ipRequired && !netIP.trim()
                ? `${netChoice} allocates nothing — give this app an address`
                : ''}
        class="bg-fjord-accent hover:bg-fjord-accent-hover text-white px-6 py-2 rounded-md font-medium shadow-lg transition-all disabled:opacity-50 flex items-center gap-2"
      >
        {#if busy}<Spinner size={14} />{/if}
        {busy ? 'Installing…' : 'Install'}
      </button>
    </div>
  </div>
</div>

{#if picking}
  <DirPicker
    start={paths[picking.name]?.[picking.i] || pathHint({ name: picking.name })}
    on:select={(e) => {
      if (picking) {
        paths[picking.name][picking.i] = e.detail;
        paths = paths;
        touched.add(picking.name);
      }
      picking = null;
    }}
    on:close={() => (picking = null)}
  />
{/if}
