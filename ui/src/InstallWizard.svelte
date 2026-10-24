<script lang="ts">
  import { setFor } from './folderMatch';
  import { insidePort, ownAddress } from './ownPort';
  import { publicUrlProblem, suggestedPublicUrl } from './publicUrl';
  import { onMount, createEventDispatcher } from 'svelte';
  import * as yaml from 'js-yaml';
  import Icon from './Icon.svelte';
  import Switch from './Switch.svelte';
  import { defaultPath } from './pathDefaults';
  import Spinner from './Spinner.svelte';
  import DirPicker from './DirPicker.svelte';
  import { addressProblem, usableRange, randomMAC } from './network';
  import ServiceResources from './ServiceResources.svelte';
  import { appjailMissing, askValue, chosenServices, choiceVars, isToggle, pickedPhrase } from './choices';
  import { resolveDefault, seedInterfaces, splitPlan, joinable, keepAttachable, addressesNeeded, type Iface } from './planSeed';
  import RestartChoice from './RestartChoice.svelte';
  import { sizeWords, SIZE_NOTE, type SizeAnswer } from './installSize';
  import { DEFAULT_RETRIES } from './restartPolicy';

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
  export let stackIds: string[] = []; // existing stacks: the new one's folder is its id

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
  // The new stack's id, as the server allocates it (stack.AllocateName): the
  // slug, or "<slug>-2", "-3" ... while one exists. Its folder is named by it.
  const folderOf = (name: string, fallback: string) => {
    const base = slugOf(name, fallback);
    let id = base;
    for (let n = 2; stackIds.includes(id); n++) id = `${base}-${n}`;
    return id;
  };
  // Suggested host directory for a path variable:
  // <app data>/<app folder>/<var-lowercased-sans-suffix>.
  const pathHint = (v: any) =>
    `${storageBase}/${folderOf(stackName, appId || 'app')}/${String(v.name).toLowerCase().replace(/[._]?path$/, '').replace(/[^a-z0-9]+/g, '-') || 'data'}`;
  const seedPath = (v: any, slug: string) =>
    defaultPath(v.default || '', { appId, locations: appDataLocations, base: storageBase, slug, hint: pathHint(v) });
  // The folders follow the name while it is being typed: a default seeded
  // for "librenms" is wrong the moment the name becomes "t-lnms". Only
  // fields nobody has edited or filled from a folder set.
  let seededSlug = '';
  $: reseedPaths(folderOf(stackName, appId || 'app'));
  function reseedPaths(slug: string) {
    if (!variables.length || !seededSlug || slug === seededSlug) return;
    for (const v of variables) {
      if (v.type !== 'path' || touched.has(v.name) || defaultedFrom[v.name]) continue;
      paths[v.name] = [seedPath(v, slug)];
      formData[v.name] = paths[v.name][0];
    }
    seededSlug = slug;
    paths = paths;
    formData = formData;
  }
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
      .replace(/\{\{\s*stack\s*\}\}/g, folderOf(stackName, appId || 'app'))
      .replace(/\{\{\s*(appdata|base)\s*\}\}/g, storageBase);

  // Host-mode fjordd can browse the host filesystem; a container can't.
  let hostMode = false;
  // Runtime engines available for this install (podman, appjail, ...).
  let engines: { name: string; available: boolean; default: boolean; warning?: string; restartPolicies?: string[]; restartOnExit?: boolean }[] = [];
  // The restart policy, from the engine's choices, starting from its default
  // in Settings; picking another engine starts again from that one's. An
  // engine that offers none installs without one.
  type RestartDefault = { policy: string; retries?: number };
  let restartDefaults: Record<string, RestartDefault> = {};
  let restartPolicy = 'unless-stopped';
  let restartRetries = DEFAULT_RETRIES;
  let restartFor = '';
  $: restartEngine = engines.find((e) => e.name === engineChoice);
  $: restartPolicies = restartEngine?.restartPolicies ?? [];
  $: restartOnExit = !!restartEngine?.restartOnExit;
  function restartFromDefault(eng: string) {
    restartFor = eng;
    restartPolicy = restartDefaults[eng]?.policy || 'unless-stopped';
    restartRetries = restartDefaults[eng]?.retries || DEFAULT_RETRIES;
  }
  $: if (engineChoice && engineChoice !== restartFor) restartFromDefault(engineChoice);
  let engineChoice = '';
  $: availableEngines = engines.filter((e) => e.available);
  // Until the engine list answers, the summary said "podman" and Install
  // was live; on a host defaulting to AppJail an early click, or an engine
  // picked before the answer (which then overwrote it), installed on the
  // wrong one. Asked at once, the pick kept, Install held until it is known.
  let enginesKnown = false;
  onMount(() => {
    fetch('/api/settings/restart')
      .then((res) => (res.ok ? res.json() : null))
      .then((d) => {
        if (!d) return;
        restartDefaults = d;
        if (engineChoice) restartFromDefault(engineChoice);
      })
      .catch(() => {}); // unless-stopped
    fetch('/api/settings/wizard')
      .then((res) => (res.ok ? res.json() : null))
      .then((d) => {
        if (!d) return;
        const detail = Number(d.detail) || 1;
        showOptions = detail >= 2;
        advancedOpen = detail >= 3;
      })
      .catch(() => {}); // default disclosure
    fetch('/api/about')
      .then((res) => (res.ok ? res.json() : null))
      .then((d) => {
        if (d) hostMode = d.mode === 'host';
      })
      .catch(() => {}); // leave browsing off; the plain field still works
    fetch('/api/engine')
      .then((res) => (res.ok ? res.json() : null))
      .then((d) => {
        if (!d) return;
        engines = d.engines || [];
        if (!engineChoice) engineChoice = d.default || 'podman';
      })
      .catch(() => {}) // no engine info -> the server falls back to its default
      .finally(() => (enginesKnown = true));
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

  // Whether that tag is built for this host, asked per tag picked (a registry
  // round trip each). Said here, before Install, rather than by a container
  // that cannot run.
  let noBuild = '';
  let checkedTag = '';
  $: if (tag && variants.length && tag !== checkedTag) checkBuild(tag);
  async function checkBuild(t: string) {
    checkedTag = t;
    noBuild = '';
    try {
      const r = await fetch(`/api/registry/platforms?image=${encodeURIComponent(imageRepo())}&tag=${encodeURIComponent(t)}`);
      if (!r.ok) return;
      const d = await r.json();
      if (t === checkedTag && d.runsHere === false)
        noBuild = `:${t} has no build for this host (${d.host}) — it is built for ${(d.platforms || []).join(', ')}.`;
    } catch {}
  }

  // image_tag variables (stacks): each selects the tag of one component image.
  // Changing the train re-points every untouched one whose registry actually
  // publishes that train; the primary image's var follows the full tag pick
  // (train/pin/custom). Hand-edited fields are never clobbered.
  let repoTrains: Record<string, Record<string, { version: string; tag: string }[]>> = {};
  const touched = new Set<string>();
  // The stack's choices (x-fjord.choices): a question each, answered by a
  // pick; the compose is the default answer. What an option asks for shows
  // only when it is picked, and is required then.
  type Ask = { name: string; label?: string; default?: string; type?: string; values?: Record<string, string> };
  type ChoiceOption = { id: string; label: string; doc?: string; ask?: Ask[]; drop?: string[]; services?: string; appjail?: unknown };
  type Choice = { id: string; label: string; doc?: string; default: string; options: ChoiceOption[] };
  let choices: Choice[] = [];
  let picks: Record<string, string> = {};
  // picks is named here, not only inside picked(): legacy reactivity tracks
  // what a statement mentions, and a helper hides it.
  $: askedNow = choices.flatMap((c) => c.options.find((o) => o.id === picks[c.id])?.ask ?? []);
  function pick(c: Choice, id: string) {
    picks = { ...picks, [c.id]: id };
    for (const a of c.options.find((o) => o.id === id)?.ask ?? []) formData[a.name] = askValue(a, formData[a.name]);
    formData = formData;
  }
  // Not bind:value: a binding to formData[...] inside the asks loop makes the
  // compiler tie formData to the loop variable, and every other write to
  // formData (seeding defaults) then threw "a is not defined".
  function setAsked(name: string, value: string) {
    formData[name] = value;
    formData = formData;
  }
  // An option that adds services runs on AppJail only with its AppJail form
  // (the jails it adds); a catalog built before forms has none. Greyed, with
  // the reason, rather than refused at Install.
  $: noAppjail = (o: ChoiceOption) => engineChoice === 'appjail' && !!o.services && !o.appjail;
  // Switched to AppJail with such an option picked: back to the default.
  $: if (engineChoice === 'appjail') {
    for (const c of choices) {
      const o = c.options.find((x) => x.id === picks[c.id]);
      if (o && noAppjail(o)) picks = { ...picks, [c.id]: c.default };
    }
  }
  // Non-default answers, in words, for the summary line.
  $: pickedWords = pickedPhrase(choices, picks);

  // What the install downloads, for the summary line. fjordd resolves the
  // images as install does, so it follows the choices, the tag and the
  // image_tag fields; asked a moment after they settle.
  let size: SizeAnswer | null = null;
  let sizeAsked = '';
  let sizeTimer: ReturnType<typeof setTimeout> | undefined;
  $: sizeBody = JSON.stringify({
    manifest: manifestText,
    app_id: appId,
    tag: appClass === 'stack' ? '' : tag.trim(),
    choices: picks,
    engine: engineChoice,
    values: Object.fromEntries(tagVars.map((v) => [v.name, formData[v.name] ?? ''])),
  });
  $: if (manifestText && enginesKnown && sizeBody !== sizeAsked) {
    sizeAsked = sizeBody;
    clearTimeout(sizeTimer);
    sizeTimer = setTimeout(() => askSize(sizeBody), 400);
  }
  async function askSize(body: string) {
    try {
      const res = await fetch('/api/apps/size', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body });
      const answer: SizeAnswer | null = res.ok ? await res.json() : null;
      if (body === sizeAsked) size = answer;
    } catch {
      if (body === sizeAsked) size = null; // no size said is no error
    }
  }
  $: sizeLine = sizeWords(size);
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
    if (madeUp(v)) return 'options'; // nothing to do, but there to set one's own
    if (given(v) && v.optional !== true) return 'primary';
    const hasDefault = String(v.default ?? '') !== '';
    if (v.optional !== true && !hasDefault && v.type !== 'path' && v.type !== 'zfs_dataset') return 'primary';
    if (v.advanced === true) return 'advanced';
    if (v.advanced === false) return 'options';
    return v.type === 'path' || v.type === 'port' || v.type === 'image_tag' ? 'options' : 'advanced';
  };
  // A variable a choice sets is the choice's to show (see choiceVars): the
  // row above Options, and its asks when an option has them.
  $: byChoice = choiceVars(choices);
  $: listedVars = variables.filter((v) => !byChoice.has(v.name));
  // A required address nothing can fill in (the app's own address on a DHCP
  // network is known once it starts) is up front: woodpecker's
  // WOODPECKER_HOST held Install from inside the folded Advanced section.
  $: mustType = (v: any) => v.type === 'public_url' && v.optional !== true && !!webOwnAddress && !webIP;
  $: primaryVars = listedVars.filter((v) => mustType(v) || levelOf(v) === 'primary');
  $: optionVars = listedVars.filter((v) => !mustType(v) && levelOf(v) === 'options');
  $: advancedVars = listedVars.filter((v) => !mustType(v) && levelOf(v) === 'advanced');
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
  // A required secret nobody has to bring is made up at install when left
  // empty (manifest.MadeUp): the catalog default is a placeholder like
  // "changeme", and it was what got installed. `generate: false` marks one
  // only its owner has (a tunnel token), typed as before.
  // A port another stack, container or program already holds: start on the
  // nearest free one and say why, instead of Install refusing it (every
  // linuxserver desktop app wants 3000 and 3001).
  let portNotes: Record<string, string> = {};
  async function freePorts() {
    portNotes = {};
    const vs = variables.filter((v: any) => v.type === 'port' && /^\d+$/.test(String(formData[v.name] ?? '')));
    if (!vs.length) return;
    const proto = (n: string) => (new RegExp('\\$\\{' + n + '\\}:\\d+/udp').test(manifestText) ? 'udp' : 'tcp');
    const keys = vs.map((v: any) => `${formData[v.name]}/${proto(v.name)}`);
    try {
      const r = await fetch('/api/ports/free?ports=' + encodeURIComponent(keys.join(',')));
      if (!r.ok) return;
      const got: Record<string, { heldBy: string; free?: number }> = await r.json();
      vs.forEach((v: any, i: number) => {
        const t = got[keys[i]];
        if (!t) return;
        const was = formData[v.name];
        if (Number(was) < 1024) {
          // A well-known port is found by its number (DNS on 53, HTTP on
          // 80): moved to 54, nothing would ask there.
          portNotes[v.name] = `${was} is in use by ${t.heldBy}, and clients look for this app on ${was}: give it its own address on a LAN network (Networking, below).`;
        } else if (t.free) {
          formData[v.name] = String(t.free);
          portNotes[v.name] = `${was} is in use by ${t.heldBy}, so ${t.free}.`;
        } else portNotes[v.name] = `${was} is in use by ${t.heldBy}.`;
      });
      formData = formData;
      portNotes = portNotes;
    } catch {
      // pre-flight still refuses a taken port at Install
    }
  }
  const madeUp = (v: any) => v.type === 'secret' && v.optional !== true && v.generate !== false;
  // One only its owner has starts empty and up front: its catalog default is
  // a placeholder too (cloudflared's YOUR_CLOUDFLARE_TOKEN_HERE), and filled
  // in, under Advanced, it installed and crash-looped.
  const given = (v: any) => v.type === 'secret' && v.generate === false;
  // Host-path variables are folder LISTS (one row per folder); every other
  // variable is a single value in formData. Deps are spelled out (formData,
  // paths): a legacy-mode `$:` only tracks variables named in the statement.
  $: missingRequired = [
    ...listedVars.filter(
      (v) =>
        v.optional !== true &&
        !madeUp(v) &&
        !(v.type === 'path' ? (paths[v.name] || []).some((p) => p.trim()) : String(formData[v.name] ?? '').trim()),
    ),
    ...askedNow.filter((a) => !String(formData[a.name] ?? '').trim()).map((a) => ({ name: a.label || a.name })),
  ];

  // Why Install is held, on screen beside it: in a tooltip alone nobody saw it
  // (woodpecker's empty WOODPECKER_HOST read as a dead button).
  $: blockReason = !validName
    ? 'Enter a valid stack name'
    : !enginesKnown
      ? 'Checking the engines…'
    : noBuild
      ? noBuild
    : noJail.length
      ? `${appId} can't run on AppJail yet: its AppJail bundle has no jail for ${noJail.join(', ')}. Install it on podman.`
      : missingRequired.length
        ? `Fill in ${missingRequired.map((v) => v.name).join(', ')}`
        : badUrls.length
          ? `Fix the address in ${badUrls.map((v) => v.name).join(', ')}`
          : ipProblem
            ? ipProblem
            : ipRequired && !netIP.trim()
              ? `${netChoice} allocates nothing — give this app an address`
              : needAddress.length
                ? `Give an address: ${needAddress.join(', ')} (in Networking above)`
                : '';

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
      const set = setFor(String(v.name), v.label, folderSets);
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
  // The services the picks install (a choice drops some, adds others), and
  // the app's declaration, "*" expanded so every service is a row the
  // operator can see and change rather than a wildcard to reason about.
  let baseSvcNames: string[] = [];
  let declaredNet: Record<string, string> = {};
  // The AppJail bundle's director: a service it has no jail for is refused
  // by the daemon, so Install is held with that reason before the click.
  let appjailDirector = '';
  $: noJail = engineChoice === 'appjail' ? appjailMissing(baseSvcNames, choices, picks, appjailDirector) : [];
  $: svcNames = chosenServices(baseSvcNames, choices, picks);
  let netPlan: Record<string, string> = {};
  $: {
    const plan: Record<string, string> = {};
    for (const n of svcNames) plan[n] = declaredNet[n] ?? declaredNet['*'] ?? '';
    netPlan = Object.values(plan).some((v) => v) ? plan : {};
  }
  $: declaresNetworking = Object.keys(netPlan).length > 0;
  // A one-service app gets the same per-service editor as the stack page, so
  // networking reads the same before and after install. Seeded directly, not
  // through resolveDefault/seedInterfaces: those pick the first LAN when no
  // default is set (the old picker meant bridge) and add a private segment for
  // parts that must reach each other, which a single service has none of.
  // Undeclared multi-service apps keep the single picker: the daemon decides
  // which of their services takes the network.
  $: singleService = !declaresNetworking && svcNames.length === 1;
  $: perService = declaresNetworking || singleService;
  // The interfaces the operator edits, one list per service -- the same editor
  // the stack page uses, so there is one way to read and change this and not
  // two. Seeded from the app's own declaration and NOT re-seeded afterwards:
  // recomputing it on every reactive pass threw each edit away as it was made.
  let planEdits: Record<string, Iface[]> = {};
  let seededFor = '';
  let seededApp = '';
  let lastSeed = ''; // what the last seed wrote, to tell an edited table from an untouched one
  $: {
    // "default" can only be resolved once the networks it has to become are
    // in hand, so the seed waits for them and re-runs if the engine (and so
    // the network list) changes.
    // app is the app, not the picks: a choice that adds or drops a service
    // keeps the rows edited on the others.
    const app = `${baseSvcNames.join(',')}|${JSON.stringify(declaredNet)}|${hostNetworked}`;
    const key = `${app}|${svcNames.join(',')}|${JSON.stringify(netPlan)}|${engineChoice}|${netChoice}|${networksLoaded}`;
    if ((singleService || declaresNetworking) && networksLoaded && key !== seededFor) {
      seededFor = key;
      let fresh: Record<string, Iface[]>;
      if (singleService) {
        const choice = netChoice || (hostNetworked ? 'host' : 'bridge');
        fresh = { [svcNames[0]]: [builtIn(choice) ? { network: choice } : { network: choice, ip: '', mac: '' }] };
      } else {
        // How the app already arranges itself, for the case where this host has
        // no network to offer: then it is installed as it ships rather than
        // being split across a built-in and a private segment.
        fresh = seedInterfaces(svcNames, resolveDefault(netPlan, networks, netChoice, hostNetworked ? 'host' : 'bridge'));
      }
      // The same app on another engine: what the operator picked stays,
      // minus what this engine cannot attach. Re-seeding threw a network
      // chosen a moment before away as soon as the engine changed.
      const edited = app === seededApp && lastSeed !== '' && JSON.stringify(planEdits) !== lastSeed;
      planEdits = edited ? keepAttachable(planEdits, fresh, networks) : fresh;
      seededApp = app;
      lastSeed = JSON.stringify(fresh);
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
  // The networking blurb says what the rows below do, for this app: which of
  // its parts go where. It used to promise "its database and cache" for every
  // app, seven web apps with neither included.
  $: openParts = svcNames.filter((n) => netPlan[n] === PLAN_DEFAULT);
  $: privateParts = svcNames.filter((n) => netPlan[n] === PLAN_PRIVATE);
  const listed = (xs: string[]) => (xs.length < 2 ? xs.join('') : `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`);
  // The network the app's web service lands on. With an address of its own
  // there, its port variables publish nothing (see ownPort.ts).
  $: webNet = perService ? exposedOn || planEdits[svcNames[0]]?.[0]?.network || '' : netChoice;
  $: webOwnAddress = ownAddress(webNet, networks);
  // The app's own ports, where it answers at that address: said once under
  // Networking, in place of port fields that would publish nothing.
  $: ownPorts = webOwnAddress
    ? [...new Set(variables.filter((v: any) => v.type === 'port').map((v: any) => insidePort(manifestText, v.name)).filter(Boolean))]
    : [];
  // public_url fields follow where the app lands until someone types in one.
  let webPortHint = '';
  let webHttps = false;
  $: webIP = perService
    ? (Object.entries(planEdits).find(([svc]) => netPlan[svc] === PLAN_DEFAULT)?.[1] ?? planEdits[svcNames[0]] ?? [])[0]?.ip ?? ''
    : netIP;
  $: fillPublicUrls(variables, formData, webPortHint, webHttps, webOwnAddress, webIP);
  // Only a value this put there is replaced: anything else was typed.
  let suggestedUrl: Record<string, string> = {};
  function fillPublicUrls(vars: any[], values: Record<string, string>, port: string, https: boolean, own: boolean, ip: string) {
    let changed = false;
    for (const v of vars) {
      if (v.type !== 'public_url' || touched.has(v.name)) continue;
      if ((values[v.name] ?? '') !== (suggestedUrl[v.name] ?? v.default ?? '')) continue;
      const portVar = port.match(/^\$\{([A-Za-z_][A-Za-z0-9_]*)/)?.[1] ?? '';
      const url = suggestedPublicUrl({
        webPort: port, https, values, host: location.hostname, ownAddress: own, ownIP: ip,
        insidePort: portVar ? insidePort(manifestText, portVar) : '',
      });
      suggestedUrl[v.name] = url;
      if (values[v.name] !== url) {
        values[v.name] = url;
        changed = true;
      }
    }
    if (changed) formData = formData;
  }
  $: badUrls = variables.filter((v) => v.type === 'public_url' && publicUrlProblem(formData[v.name] ?? ''));
  const PLAN_PRIVATE = 'private';
  const PLAN_DEFAULT = 'default';
  const PLAN_NONE = 'none';
  // The address and MAC describe ONE interface, so they belong to whichever
  // service sits on the chosen network -- not to "the app", which with four
  // Attachable networks (empty on hosts without them). A network with no
  // subnet gets its addresses from DHCP -- nothing here needs to know the
  // segment, and no address has to be supplied.
  type Network = { name: string; driver?: string; subnet?: string; static?: boolean; addressSource?: string };
  let networks: Network[] = [];
  let netChoice = '';
  const BUILT_IN = [
    { name: 'bridge', detail: 'A private address behind NAT, reached on the ports it publishes on this host.' },
    { name: 'host', detail: "No address or port mapping of its own — it binds this host's ports directly." },
    { name: 'none', detail: 'No network at all: nothing in and nothing out.' },
  ];
  const builtIn = (name: string) => BUILT_IN.some((b) => b.name === name);
  // What an empty choice installs as: the manifest's own networking. On podman
  // a host-networked app stays on host -- the picker said "bridge" over it. On
  // appjail the app's bundle decides, and bundles ship on appjail's NAT bridge.
  $: shownChoice = netChoice || (hostNetworked && engineChoice !== 'appjail' ? 'host' : 'bridge');
  $: chosenBuiltIn = BUILT_IN.find((b) => b.name === shownChoice);
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
  $: ipProblem = !perService && netChoice && !builtIn(netChoice) ? addressProblem(netIP, chosenNet) : '';
  // What may go in the field, said forwards -- the subnet in the picker does
  // not answer it, since three of its addresses are spoken for.
  $: ipRange = netChoice && !builtIn(netChoice) ? usableRange(chosenNet) : '';
  // appjail cannot draw from the pool the podman side's IPAM manages, so on a
  // pool network it needs an address given to it. On DHCP nothing does.
  // Keyed on WHO allocates, which the network reports. Inferring it from "has
  // a subnet" was wrong twice over: a DHCP network records its segment, and an
  // appjail virtualnet has a CIDR that appjail itself allocates from -- so
  // both were demanding an address that neither needs.
  $: needAddress = perService ? addressesNeeded(planEdits, networks, engineChoice) : [];
  // Stack-wide form only; per service, the rows say (addressesNeeded).
  $: ipRequired =
    !perService &&
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
    choices = [];
    picks = {};
    touched.clear();
    await storageReady;
    try {
      const res = await fetch(manifestUrl, { cache: 'no-store' });
      if (!res.ok) throw new Error('Failed to fetch manifest');
      manifestText = await res.text();
      const parsed: any = yaml.load(manifestText);
      baseSvcNames = Object.keys(parsed?.services ?? {});
      appjailDirector = String(parsed?.['x-fjord']?.appjail?.director ?? '');
      declaredNet = parsed?.['x-fjord']?.networking ?? {};
      choices = parsed?.['x-fjord']?.choices ?? [];
      webPortHint = String(parsed?.['x-fjord']?.info?.web_port ?? '');
      webHttps = parsed?.['x-fjord']?.info?.web_https === true;
      for (const c of choices) picks[c.id] = c.default;
      picks = picks;
      if (parsed['x-fjord'] && parsed['x-fjord'].variables) {
        variables = parsed['x-fjord'].variables;
        variables.forEach((v) => {
          // Path vars get this stack's own <App data>/<stack>/... folder: a
          // catalog default that names the app is re-rooted, none at all
          // becomes a hint; still fully editable + browsable.
          formData[v.name] = v.type === 'path' ? seedPath(v, folderOf(stackName, appId || 'app')) : madeUp(v) || given(v) ? '' : v.default || '';
          if (v.type === 'path') paths[v.name] = [formData[v.name]];
        });
        seededSlug = folderOf(stackName, appId || 'app');
        formData = formData; // seeding above mutates in place; reassign so missingRequired recomputes
        paths = paths;
        defaultedFrom = {};
        applyDefaultSets();
        freePorts();
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
    const plan = perService ? splitPlan(planEdits) : null;
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
      choices: picks,
      restart: restartPolicies.includes(restartPolicy) ? { policy: restartPolicy, retries: restartPolicy === 'on-failure' ? restartRetries : 0 } : undefined,
    });
  }

  function close() {
    dispatch('close');
  }
</script>

<div class="fixed inset-0 bg-black/80 backdrop-blur-sm flex items-center-safe justify-center overflow-y-auto z-50 p-4">
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

          {#snippet reachedAt()}
            {#if ownPorts.length}
              <p class="text-sm text-fjord-fg-body mt-1 mb-2">
                It answers at its own address on <span class="font-mono">{webNet}</span>{#if webIP} (<span class="font-mono">{webIP}</span>){/if},
                on {ownPorts.length > 1 ? 'ports' : 'port'} <span class="font-mono">{ownPorts.join(', ')}</span>. Nothing is published on this host.
              </p>
            {/if}
          {/snippet}

          {#snippet varField(v: any)}
            {#if v.type === 'port' && webOwnAddress && insidePort(manifestText, v.name)}
              <!-- Nothing to choose: with its own address nothing is published
                   on this host. Networking says where it answers instead. -->
            {:else}
            <div class="flex flex-col gap-1">
              <label class="text-sm font-semibold text-fjord-fg-secondary flex items-center gap-2" for={v.name}>
                <span class="font-mono text-fjord-accent/90">{v.name}</span>
                {#if v.optional !== true}<span class="text-fjord-danger" title="Required">*</span>{:else}<span class="text-xs font-normal text-fjord-fg-dim" data-optional>(optional)</span>{/if}
                {#if v.type === 'path'}<span class="text-[10px] font-normal uppercase tracking-wide text-fjord-fg-dim border border-fjord-border rounded px-1.5 py-0.5">host path</span>{/if}
              </label>
              {#if portNotes[v.name]}
                <div class="text-xs text-fjord-warning">{portNotes[v.name]}</div>
              {/if}
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
                {#if v.type === 'public_url'}
                  {#if webOwnAddress && !webIP && v.optional !== true && isEmpty(v)}
                    <div class="text-xs text-fjord-warning">
                      Its address on {webNet} is only known once it starts, so it can't be filled in for you. Type the address you will open it at
                      (a name or address and its port), or put it on bridge in Networking.
                    </div>
                  {:else}
                    <div class="text-xs text-fjord-fg-dim">
                      Filled in from where this app will be reachable{#if webOwnAddress && !webIP} (its address on {webNet} is only known once it starts){/if}.
                      Change it if you open it another way, a domain or a reverse proxy.
                    </div>
                  {/if}
                {/if}
                <input
                  id={v.name}
                  type="text"
                  bind:value={formData[v.name]}
                  on:input={() => touched.add(v.name)}
                  placeholder={madeUp(v) ? 'made up at install — or type your own' : given(v) ? (v.optional !== true ? 'required' : '') : v.default || (v.optional !== true ? 'required' : '')}
                  class="w-full bg-fjord-inset border rounded-md px-3 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent transition-colors {(v.optional !== true && !madeUp(v) && isEmpty(v)) || (v.type === 'public_url' && publicUrlProblem(formData[v.name] ?? '')) ? 'border-fjord-danger/60' : 'border-fjord-border'}"
                />
                {#if v.type === 'public_url' && publicUrlProblem(formData[v.name] ?? '')}
                  <div class="text-xs text-fjord-danger">{publicUrlProblem(formData[v.name] ?? '')}</div>
                {/if}
              {/if}
            </div>
            {/if}
          {/snippet}

          {#each choices as c (c.id)}
            {@const chosen = c.options.find((o) => o.id === picks[c.id])}
            {@const asks = chosen?.ask ?? []}
            <div class="space-y-2 border border-fjord-border rounded-lg p-3 bg-fjord-card/40">
              {#if isToggle(c)}
                {@const onOpt = c.options.find((o) => o.id === 'on')}
                <div class="flex items-center gap-4">
                  <div class="flex-1 min-w-0">
                    <div class="text-sm font-medium text-fjord-fg">{c.label}</div>
                    {#if c.doc}<div class="text-xs text-fjord-fg-muted">{c.doc}</div>{/if}
                  </div>
                  <Switch
                    checked={picks[c.id] === 'on'}
                    label={c.label}
                    disabled={picks[c.id] !== 'on' && !!onOpt && noAppjail(onOpt)}
                    title={picks[c.id] !== 'on' && onOpt && noAppjail(onOpt) ? 'Not on AppJail with this catalog: it has no AppJail form for this part.' : ''}
                    on:change={(e) => pick(c, e.detail ? 'on' : 'off')}
                  />
                </div>
              {:else}
              <div class="flex flex-wrap items-baseline gap-x-2">
                <span class="text-sm font-medium text-fjord-fg">{c.label}</span>
                {#if c.doc}<span class="text-xs text-fjord-fg-muted">{c.doc}</span>{/if}
              </div>
              <div class="flex flex-wrap gap-1.5" role="radiogroup" aria-label={c.label}>
                {#each c.options as o (o.id)}
                  <button
                    type="button"
                    role="radio"
                    aria-checked={picks[c.id] === o.id}
                    disabled={noAppjail(o)}
                    title={noAppjail(o) ? 'Not on AppJail with this catalog: it has no AppJail form for this option. Install on podman, or refresh the catalog.' : o.doc || ''}
                    on:click={() => pick(c, o.id)}
                    class="px-3 py-1.5 rounded-full text-sm border transition-colors disabled:opacity-40 disabled:cursor-not-allowed {picks[c.id] === o.id
                      ? 'bg-fjord-accent text-white border-fjord-accent'
                      : 'bg-fjord-inset text-fjord-fg-secondary border-fjord-border hover:text-fjord-fg'}">{o.label}</button
                  >
                {/each}
              </div>
              {/if}
              {#if chosen?.doc}
                <p class="text-xs text-fjord-fg-muted">{chosen.doc}</p>
              {/if}
              {#if asks.length}
                <div class="grid gap-3 sm:grid-cols-2 pt-1">
                  {#each asks as a (a.name)}
                    <div class="space-y-1">
                      <label for="ask-{a.name}" class="block text-xs font-medium text-fjord-fg-secondary">{a.label || a.name}</label>
                      {#if a.values && Object.keys(a.values).length}
                        <select
                          id="ask-{a.name}"
                          value={formData[a.name] ?? ''}
                          on:change={(e) => setAsked(a.name, e.currentTarget.value)}
                          class="w-full bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
                        >
                          {#if Object.keys(a.values).length > 1}<option value="">—</option>{/if}
                          {#each Object.entries(a.values) as entry (entry[0])}<option value={entry[0]}>{entry[1]}</option>{/each}
                        </select>
                      {:else}
                        <input
                          id="ask-{a.name}"
                          type={a.type === 'secret' ? 'password' : 'text'}
                          value={formData[a.name] ?? ''}
                          on:input={(e) => setAsked(a.name, e.currentTarget.value)}
                          placeholder="required"
                          class="w-full bg-fjord-inset border rounded-md px-3 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent transition-colors {String(formData[a.name] ?? '').trim() ? 'border-fjord-border' : 'border-fjord-danger/60'}"
                        />
                      {/if}
                    </div>
                  {/each}
                </div>
              {/if}
            </div>
          {/each}

          {#each primaryVars as v}{@render varField(v)}{/each}

          <!-- the defaults being accepted, one fact a row; Options is one click -->
          <div class="flex flex-wrap items-end gap-x-4 gap-y-2 text-sm border-t border-fjord-border pt-4">
            <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 min-w-0" data-install-summary>
              <dt class="text-fjord-fg-dim">Version</dt>
              <dd class="text-fjord-fg-body min-w-0"><b>{summaryVersion}</b>{#if summaryTrain}<span class="text-fjord-fg-muted"> · {summaryTrain}</span>{/if}</dd>
              <dt class="text-fjord-fg-dim">Engine</dt>
              <dd class="text-fjord-fg-body">{engineChoice || (enginesKnown ? 'the default engine' : 'checking engines…')}</dd>
              {#if pickedWords}
                <dt class="text-fjord-fg-dim">With</dt>
                <dd class="text-fjord-fg-body">{pickedWords}</dd>
              {/if}
              <dt class="text-fjord-fg-dim">Data</dt>
              <dd class="font-mono text-fjord-fg-secondary break-all">{storageBase}/{folderOf(stackName, appId || 'app')}</dd>
              {#if sizeLine}
                <dt class="text-fjord-fg-dim">Download</dt>
                <dd class="text-fjord-fg-body" title={SIZE_NOTE} data-install-size>{sizeLine}</dd>
              {/if}
            </dl>
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
                {#if noBuild}<span class="text-xs text-fjord-warning">{noBuild}</span>{/if}
              </div>
            </div>
          {/if}
          {#if appDataLocations.length > 1}
            <div class="flex flex-col gap-1">
              <label class="text-sm font-semibold text-fjord-fg-secondary" for="appdata">App data</label>
              <p class="text-xs text-fjord-fg-dim">Where this app's own folders go — <span class="font-mono">{storageBase}/{folderOf(stackName, appId || 'app')}/…</span></p>
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
              {#if perService}
                <label class="text-sm font-semibold text-fjord-fg-secondary" for="net">Networking</label>
                {@render reachedAt()}
                {#if singleService}
                  {#if hostNetworked}
                    <p class="text-xs text-fjord-warning mt-1 mb-3">
                      This app expects host networking. Giving it an address of its own is possible
                      below; install it as it is and change it afterwards if you want to try.
                    </p>
                  {:else}
                    <p class="text-xs text-fjord-fg-dim mb-3">How this app reaches the network — the same editor as its Services tab after install.</p>
                  {/if}
                {:else if joinableNets.length}
                  <p class="text-xs text-fjord-fg-dim mb-3">
                    {#if builtIn(exposedOn)}
                      New installs use <span class="font-mono">{exposedOn}</span>, so this app goes as it ships.
                    {:else if openParts.length}
                      {listed(openParts)}
                      {openParts.length === 1 ? 'goes' : 'go'} on <span class="font-mono">{exposedOn || joinableNets[0].name}</span>{#if privateParts.length},
                        and {listed(privateParts)} on a network of their own, not reachable from your LAN{/if}.
                    {:else if privateParts.length}
                      {listed(privateParts)} {privateParts.length === 1 ? 'goes' : 'go'} on a network of its own, not reachable from your LAN.
                    {/if}
                    Change any of it here, or after installing.
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
                  oneBridgePerService={engineChoice === 'appjail'}
                  bridgeAlone={engineChoice === 'appjail'}
                  on:change={() => (planEdits = planEdits)}
                />
              {:else}
                <label class="text-sm font-semibold text-fjord-fg-secondary" for="net">Networking</label>
                {@render reachedAt()}
                {#if hostNetworked && !declaresNetworking && engineChoice !== 'appjail'}
                  <p class="text-xs text-fjord-warning mt-1 mb-2">
                    This app expects host networking — its services reach each other over
                    <span class="font-mono">localhost</span>. Giving it an address of its own means
                    they have to find each other by name or address instead, which this app has not
                    said it can do. Install it as it is and change it afterwards if you want to try.
                  </p>
                {:else}
                  <p class="text-xs text-fjord-fg-dim mb-2">How this app reaches the network.</p>
                {/if}
                <!-- Shows bridge while nothing is chosen, without choosing it:
                     netChoice stays "" so the operator's default network can
                     still land once the list loads (applyDefaultNetwork only
                     fills an empty choice). Bound directly, "" matched no
                     option and the box was blank above text describing bridge. -->
                <select
                  id="net"
                  value={shownChoice}
                  on:change={(e) => (netChoice = e.currentTarget.value)}
                  class="w-full bg-fjord-inset border border-fjord-border rounded-md px-3 py-2 text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
                >
                  <!-- The built-ins by name, the same three words the Networks
                       page and each stack's own picker use. "" is the historic
                       value for bridge, from before it had a name. -->
                  {#each BUILT_IN as b}
                    <option value={b.name}>{b.name}</option>
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

            <div class="flex flex-col gap-1">
              <label class="text-sm font-semibold text-fjord-fg-secondary" for="restart">{restartOnExit || !restartPolicies.length ? 'Restart' : 'At boot'}</label>
              <RestartChoice
                id="restart"
                policies={restartPolicies}
                onExit={restartOnExit}
                bind:policy={restartPolicy}
                bind:retries={restartRetries}
                disabled={!restartPolicies.length}
                note="{engineChoice || 'This engine'} does not take a restart policy from fjord."
              />
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
      {#if blockReason && !busy && !loading && !error}
        <span class="text-xs text-fjord-warning">{blockReason}</span>
      {/if}
      <button
        on:click={deploy}
        disabled={busy || loading || !enginesKnown || !!error || !validName || missingRequired.length > 0 || badUrls.length > 0 || (ipRequired && !netIP.trim()) || !!ipProblem || needAddress.length > 0 || !!noBuild}
        title={blockReason || undefined}
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
