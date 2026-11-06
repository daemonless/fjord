<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import StackDetail from './StackDetail.svelte';
  import Sidebar from './Sidebar.svelte';
  import AppStore from './AppStore.svelte';
  import Volumes from './Volumes.svelte';
  import Networks from './Networks.svelte';
  import Adopt from './Adopt.svelte';
  import System from './System.svelte';
  import Settings from './Settings.svelte';
  import { checkSession, SIGNED_IN } from './auth';
  import SetupWizard from './SetupWizard.svelte';
  import Dashboard from './Dashboard.svelte';
  import ConditionsBanner from './ConditionsBanner.svelte';
  import { type Condition } from './conditions';
  import Spinner from './Spinner.svelte';
  import { loadNames } from './names';
  import Icon from './Icon.svelte';
  import Shortcuts from './Shortcuts.svelte';
  import { match as matchShortcut, typing } from './shortcuts';
  import { splitPlan } from './planSeed';
  import { loadAppIcons } from './appIcons';
  import Toasts from './Toasts.svelte';
  import { toast } from './toast';
  import { firstLine } from './errorLine';
  import { startError } from './adopt';
  import { currentTheme, setTheme, watchSystem, type Theme } from './theme';
  import {
    BUSY_LABEL,
    mergeStackList,
    newStackDraft as makeStackDraft,
    reduceStackEvent,
    refTag,
    stackLabel as label,
    statusLabel,
    type Attachment,
    type Network,
    type ServiceUpdate,
    type Stack,
    type UpdateInfo,
  } from './stacks';
  import {
    createOutputFollower,
    createRunTracker,
    formatActionHttpError,
    readActionStream,
  } from './actions';

  let stacks: Stack[] = [];
  // Until the first list arrives, "no stacks" would tell someone with apps
  // that they are gone.
  let stacksLoaded = false;
  // Why the first list did not arrive: 0 = no answer, else the HTTP status.
  let stacksFailed: number | null = null;
  let logs: Record<string, string> = {};
  let execStatus: Record<string, 'idle' | 'running' | 'watching' | 'error'> = {};
  // Set while the Output pane follows an action this page did not start.
  let following: Record<string, boolean> = {};
  let execMessage: Record<string, string> = {};
  let selectedStack: Stack | null = null;
  let activeTab: 'compose' | 'makejail' | 'env' | 'net' = 'compose';
  let originalCompose = '';
  let originalEnv = '';
  let originalDirector = '';
  let originalMakejail = '';
  let currentView: 'stacks' | 'store' | 'volumes' | 'networks' | 'system' | 'settings' | 'adopt' = 'stacks';
  // Settings sub-tab, mirrored into the URL (#/settings/<tab>) like the views.
  let settingsTab: 'storage' | 'extensions' | 'catalogs' | 'advanced' | 'account' = 'storage';

  // Draft for the "+" button: an image that comes Up as-is, so a blank stack
  // is immediately a box you can open a shell in -- FreeBSD's base image, or
  // on Linux alpine, which needs a command to stay up. A podman draft is a
  // compose; an appjail draft is a native director spec + Makejail (no
  // compose) that runs via appjail-director from the start.
  let hostOS = '';
  let engines: { name: string; available: boolean; enabled: boolean }[] = [];
  let defaultEngine = 'podman';
  let conditions: Condition[] = [];
  async function loadConditions() {
    try {
      const r = await fetch('/api/conditions');
      if (r.ok) conditions = (await r.json()).conditions || [];
    } catch {}
  }
  async function loadEngines() {
    try {
      const r = await fetch('/api/engine');
      if (!r.ok) return;
      const d = await r.json();
      engines = d.engines ?? [];
      if (d.default) defaultEngine = d.default;
    } catch {}
  }
  const newStackDraft = (engine = defaultEngine, name = `stack-${Date.now().toString().slice(-4)}`): Stack =>
    makeStackDraft(engine, hostOS, name);
  let saving = false;
  let routeReady = false; // gate URL writes until the initial hash is restored
  // First-run setup wizard: shown until finished/skipped, or on #/setup.
  let setupOpen = false;
  async function setupDone() {
    setupOpen = false;
    currentView = 'store';
    await selectStack(null);
    location.hash = '#/store';
  }
  let daemonVersion = ''; // shown under the sidebar
  let drawerOpen = true; // bottom panel — open by default
  let stackDetail: StackDetail | null = null;
  try {
    const d = localStorage.getItem('fjord.drawerOpen');
    if (d !== null) drawerOpen = d === '1';
  } catch {}
  // Bottom panel tabs: "terminal" = action output, "logs" = live container logs.
  // Separate buffers so streaming logs doesn't clobber the last command output.
  // "output" = fjord's action output (up/down/restart/update), "logs" = the
  // app's container logs, "shell" = interactive exec. (Was "terminal".)
  let drawerTab: 'output' | 'logs' | 'shell' = 'output';
  try {
    const t = localStorage.getItem('fjord.drawerTab');
    if (t === 'terminal') drawerTab = 'output'; // migrate old value
    else if (t === 'output' || t === 'logs' || t === 'shell') drawerTab = t;
  } catch {}

  // Phones: the sidebar is a drawer behind a menu button, not a column
  // that leaves the page a sliver. narrow follows the viewport.
  let narrow = false;
  let navOpen = false;
  if (typeof window !== 'undefined' && window.matchMedia) {
    const mq = window.matchMedia('(max-width: 767px)');
    narrow = mq.matches;
    mq.addEventListener('change', (e) => { narrow = e.matches; if (!narrow) navOpen = false; });
  }
  // Picking anything in the drawer closes it.
  $: if (narrow && (selectedStack || currentView)) navOpen = false;

  // A director-backed appjail stack: its runtime spec is appjail-director.yml,
  // not compose.yaml (kept only for status/log service discovery). The primary
  // tab edits the director spec; director reconciles changes on the next up.
  $: isDirector = !!selectedStack?.director;

  async function setStackGroup(name: string, group: string) {
    const g = group.trim();
    await fetch(`/api/stacks/${name}/group`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ group: g }),
    });
    if (selectedStack?.name === name) {
      selectedStack.state = { ...(selectedStack.state || {}), group: g }; // optimistic
      selectedStack = selectedStack;
    }
    await loadStacks(); // refresh so the sidebar regroups
  }

  let networks: Network[] = [];
  // Per-service interfaces, staged here so Save can post them and the dirty
  // check can see them. Keyed by service name.
  let svcNets: Record<string, Attachment[]> = {};
  let savedSvcNets = '{}';
  $: svcNetsDirty = JSON.stringify(svcNets) !== savedSvcNets;
  // The interfaces as the stack has them on disk, and the copy the dirty check
  // compares against. Re-seeded after a network Save too: keeping the copy from
  // when the page opened made "back to what it was" look unchanged, so that
  // Save sent nothing.
  function seedSvcNets(stack: any) {
    svcNets = Object.fromEntries(
      (stack?.services ?? []).map((v: any) => {
        const rows = (v.networks ?? []).map((n: any) => ({ ...n }));
        if (rows.length) return [v.name, rows];
        // On the compose's default network: that is bridge, and says so.
        return [v.name, [{ network: v.hostNetwork ? 'host' : v.noNetwork ? 'none' : 'bridge', ip: '', mac: '' }]];
      }),
    );
    savedSvcNets = JSON.stringify(svcNets);
  }

  // svcNetsDirty belongs here: the interface table edits staged state rather
  // than the compose, so without it changing a row showed no Unsaved badge at
  // all -- the change looked like it had already happened, or like nothing had.
  $: isDirty = selectedStack
    ? selectedStack.compose !== originalCompose || selectedStack.env !== originalEnv || (selectedStack.director ?? '') !== originalDirector || (selectedStack.makejail ?? '') !== originalMakejail || svcNetsDirty
    : false;
  // A draft is a stack the server doesn't know about yet (New Stack). Detect it
  // by BOTH signals so neither one's timing can misfire: a real stack always has
  // a `dir` (set the moment its detail loads) AND appears in the stacks list.
  // Using dir alone would flag a just-saved New Stack; using list-membership
  // alone flagged a freshly-installed stack until loadStacks() caught up (the
  // "had to refresh before Start worked" bug). Draft only when neither holds.
  $: isDraft = !!selectedStack && !selectedStack.dir && !stacks.some((s) => s.name === selectedStack?.name);

  // Reflect the current view/stack into the URL hash so it's shareable,
  // survives a refresh, and the browser's Back button walks fjord's own
  // history instead of leaving the site (pushState; neither it nor
  // replaceState fires hashchange, so no loop -- Back does, via
  // restoreFromHash, and by then the hash already matches). Renaming a
  // draft only rewrites the current entry. Derive the URL the SAME way the
  // page renders (selectedStack wins over currentView) so the address bar
  // always matches what's on screen.
  $: if (routeReady && typeof location !== 'undefined') {
    // The tab is part of a stack's address (Services has none), so a refresh
    // or a link lands on it rather than back on Services.
    const h = selectedStack
      ? '#/stacks/' + encodeURIComponent(selectedStack.name) + (activeTab === 'net' || isDraft ? '' : '/' + activeTab)
      : currentView === 'store'
        ? '#/store'
        : currentView === 'volumes'
          ? '#/volumes'
          : currentView === 'networks'
            ? '#/networks'
          : currentView === 'adopt'
            ? '#/adopt'
          : currentView === 'system'
            ? '#/system'
            : currentView === 'settings'
              ? '#/settings/' + settingsTab
              : '#/stacks';
    if (!setupOpen && location.hash !== h) {
      const rename = isDraft && location.hash.startsWith('#/stacks/');
      // Another tab of the same stack replaces the entry: Back goes to the
      // page before, not through every tab clicked on the way.
      const stackOf = (x: string) => x.split('/').slice(0, 3).join('/');
      const sameStack = selectedStack && stackOf(location.hash) === stackOf(h);
      history[rename || sameStack ? 'replaceState' : 'pushState'](null, '', h);
    }
  }

  // Update-availability for the selected stack (digest drift vs the registry).
  let updateInfo: UpdateInfo | null = null;
  let updateCheckedAt = 0;
  let checkingUpdate = false;

  async function checkForUpdate(name: string) {
    checkingUpdate = true;
    updateInfo = null;
    try {
      const res = await fetch(`/api/stacks/${name}/update-check`);
      // A slow registry can answer after the operator has moved on; the
      // result belongs to the stack it was asked about, not the one on screen.
      if (res.ok && selectedStack?.name === name) {
        updateInfo = await res.json();
        updateCheckedAt = Date.now();
        // The sidebar arrow and "N updates available" read the fleet list,
        // which only a refresh replaced: after an update the page said up to
        // date while the list still said behind.
        if (updateInfo) fleet = { ...fleet, [name]: updateInfo };
      }
    } catch {
      // registry/socket unreachable -> leave it unknown, no badge
    } finally {
      checkingUpdate = false;
    }
  }

  let updatePanel = '';

  // Back to the image a service ran before its last update. Runs as an update,
  // so the recreate check and the health watch apply to it too.
  async function rollback(name: string, service: string) {
    // The daemon pins the compose before it starts the update, and the update
    // then runs for 30s+. Reload the editor right away: left on its old copy,
    // a Save during the health watch wrote the old image straight back.
    await streamAction(name, 'rollback', `Rolling back ${service}…`, { services: [service] }, () => reloadStack(name));
    if (selectedStack?.name === name) {
      await selectStack({ name } as Stack);
      checkForUpdate(name);
    }
    loadFleetUpdates(true);
  }
  async function unpin(name: string, service: string) {
    const res = await fetch(`/api/stacks/${name}/unpin`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ service }),
    });
    if (!res.ok) {
      toast(`Unpin failed: ${(await res.text()).trim()}`, { kind: 'error' });
      return;
    }
    toast(`${service} follows its tag again`, { kind: 'success' });
    if (selectedStack?.name === name) {
      await selectStack({ name } as Stack);
      checkForUpdate(name);
    }
    loadFleetUpdates(true);
  }
  async function upgradeTo(name: string, tag: string) {
    updatePanel = '';
    const res = await fetch(`/api/stacks/${name}/set-tag`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ tag, pin: false, update: true }),
    });
    if (!res.ok) {
      toast(`Version change failed: ${(await res.text()).trim()}`, { kind: 'error' });
      return;
    }
    await followUpdate(name, res);
  }
  // A version change is one job of the daemon's: set-tag writes the new
  // ref and runs the update itself, so a page gone after the click changes
  // nothing about what runs. This page only follows its output, then
  // refreshes what an update changes. The second call is for a daemon that
  // did not say it was updating.
  async function followUpdate(name: string, res: Response, services?: string[]) {
    const updating = !!(await res.json().catch(() => ({})))?.updating;
    await selectStack({ name } as Stack); // the compose now names the new tag
    if (!updating) {
      await update(name, services);
      return;
    }
    logs[name] = '';
    execMessage[name] = 'Updating…';
    if (stillWatching(name)) {
      drawerTab = 'output';
      drawerOpen = true;
    }
    await showActionOutput(name);
    if (!services?.length) needsApply[name] = false;
    await refreshState(name);
    await loadStacks();
    if (selectedStack?.name === name) checkForUpdate(name); // refresh the badge
    loadFleetUpdates(true);
  }
  // The picked services, new versions retagged first -- one service each, so
  // the database is never moved to the app's version -- then one update.
  async function updatePicked(name: string, svcs: ServiceUpdate[]) {
    updatePanel = '';
    const bumps = svcs.filter((s) => s.state === 'upgrade');
    const services = svcs.map((s) => s.service);
    for (const [i, s] of bumps.entries()) {
      const last = i === bumps.length - 1; // the last retag runs the update
      const res = await fetch(`/api/stacks/${name}/set-tag`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tag: s.newTag, service: s.service, pin: false, ...(last ? { update: true, update_services: services } : {}) }),
      });
      if (!res.ok) {
        toast(`Could not move ${s.service} to v${s.toVersion}: ${(await res.text()).trim()}`, { kind: 'error' });
        return;
      }
      if (last) {
        await followUpdate(name, res, services);
        return;
      }
    }
    await update(name, services);
  }

  // Whether the operator is still on the stack an action was started from.
  // Anything that finishes long after it was started has to check this before
  // navigating: the page they are on now is the one they chose.
  const stillWatching = (name: string) => currentView === 'stacks' && selectedStack?.name === name;

  async function selectStack(stack: Stack | null) {
    // Only when moving to a DIFFERENT stack. Re-selecting the one already open
    // is a refresh, not navigation -- and the install flow does exactly that
    // when the deploy finishes, which yanked the user off whichever tab they
    // had opened while waiting. It read as the tab refusing to be clicked for
    // the first few seconds after a create.
    // Services first: what runs, where it answers, what is behind. A draft
    // has no services yet -- it is a compose being written.
    if (stack?.name !== selectedStack?.name) activeTab = stack?.dir ? 'net' : 'compose';
    updateInfo = null;
    stackDetail?.resetLogs();
    if (!stack) {
      selectedStack = null;
      originalCompose = '';
      originalEnv = '';
      originalDirector = '';
      originalMakejail = '';
      return;
    }
    // The list's .env has its secrets blanked: the editor needs the stack's
    // own answer. Without it (fjord did not answer, the session ended) the
    // list's copy is shown but never saved -- it would blank the passwords.
    // A draft from "+" is not on the server yet: it is all here already.
    let full = !stack.dir && !stacks.some((s) => s.name === stack!.name);
    if (!full) {
      try {
        const res = await fetch(`/api/stacks/${stack.name}`);
        if (res.ok) {
          stack = await res.json();
          full = true;
        }
      } catch {}
    }
    if (!full) stack = { ...stack!, listOnly: true };
    selectedStack = stack;
    currentView = 'stacks'; // selecting a stack always means the Stacks section
    showActionOutput(stack!.name);
    originalCompose = stack!.compose;
    originalEnv = stack!.env;
    originalDirector = stack!.director ?? '';
    originalMakejail = stack!.makejail ?? '';
    // Show the network the stack is ACTUALLY on. The picker is otherwise
    // write-only and reads "bridge" for every attached stack.
    // A service on a built-in gets that as its ROW, not as an absence of rows.
    // The editor used to be replaced by a line of prose for a host-networked
    // service, which left a host stack with nothing to change: switching to
    // "per service" showed nothing and Save had nothing to save.
    seedSvcNets(stack);
    // Resources tab is engine-scoped to this stack (see loadNetworks/loadVolumes).
    loadNetworks(stack!.name);
    loadVolumes(stack!.name);
    checkForUpdate(stack!.name); // fire-and-forget; badge fills in when it returns
    // The Logs tab persists across refreshes -- if it's the active view, start
    // streaming now instead of waiting for a tab click.
    stackDetail?.followLogsIfActive(stack!.name);
  }

  async function loadStacks() {
    let res: Response;
    try {
      res = await fetch('/api/stacks');
    } catch {
      if (!stacksLoaded) stacksFailed = 0;
      return;
    }
    if (!res.ok && !stacksLoaded) stacksFailed = res.status;
    if (res.ok) {
      const fresh: Stack[] = await res.json();
      const merged = mergeStackList(selectedStack, fresh);
      stacks = merged.stacks;
      if (merged.selectedStack !== selectedStack) selectedStack = merged.selectedStack;
      stacksLoaded = true;
      stacksFailed = null;
    }
  }

  // Networks/volumes are engine-specific: when a stack is selected the Resources
  // tab must show ITS engine's resources (appjail virtualnets, not podman's), so
  // scope the query to that stack. Unscoped = the default engine's view.
  // Two of these are in flight on load -- one unscoped from onMount, one for
  // the stack the hash selected -- and they finish in no fixed order. The
  // later request wins; without this the earlier one could land last and
  // replace the list under the page.
  let netsSeq = 0;
  async function loadNetworks(stackId?: string) {
    const seq = ++netsSeq;
    try {
      const q = stackId ? '?stack=' + encodeURIComponent(stackId) : '';
      const res = await fetch('/api/networks' + q);
      if (res.ok && seq === netsSeq) networks = (await res.json()) || []; // null when none -> []
    } catch {}
  }

  // Theme. index.html already stamped <html data-theme> before paint; this
  // mirrors it for the button label and follows the OS until a choice is made.
  let theme: Theme = 'dark';
  let unwatchTheme: (() => void) | null = null;
  function toggleTheme() {
    theme = theme === 'dark' ? 'light' : 'dark';
    setTheme(theme);
  }

  // Fleet-wide update state (server-side cached; refreshed in the background).
  let fleet: Record<string, UpdateInfo> = {};
  let fleetRefreshing = false;
  let updatingAll = false;
  async function loadFleetUpdates(refresh = false) {
    try {
      const res = await fetch('/api/updates' + (refresh ? '?refresh=1' : ''));
      if (!res.ok) return;
      const d = await res.json();
      fleet = d.stacks || {};
      fleetRefreshing = d.refreshing;
      // A refresh was kicked off server-side; pick up its results shortly.
      if (d.refreshing) setTimeout(() => loadFleetUpdates(), 5000);
    } catch {}
  }

  let dashboardUpdateAllOpen = false;
  function openUpdateAll() {
    selectStack(null);
    currentView = 'stacks';
    dashboardUpdateAllOpen = true;
  }

  async function updateAll(targetStacks?: string[]) {
    if (updatingAll) return;
    updatingAll = true;
    try {
      const res = await fetch('/api/updates', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(targetStacks?.length ? { stacks: targetStacks } : {}),
      });
      if (!res.ok) {
        toast(`Update failed: ${(await res.text()).trim() || `HTTP ${res.status}`}`, { kind: 'error' });
        return;
      }
      const d = await res.json().catch(() => ({}));
      const names: string[] = d.updating || [];
      if (!names.length) {
        toast('Nothing ready to update', { kind: 'info' });
        return;
      }
      const set = new Set(names);
      stacks = stacks.map((s) => (set.has(s.name) ? { ...s, busy: 'update' } : s));
      if (selectedStack && set.has(selectedStack.name)) {
        selectedStack = { ...selectedStack, busy: 'update' };
        showActionOutput(selectedStack.name);
      }
      toast(names.length === 1 ? `Updating ${names[0]}` : `Updating ${names.length} stacks`, { kind: 'info' });
    } catch (e: any) {
      toast(`Update failed: ${e.message || e}`, { kind: 'error' });
    } finally {
      updatingAll = false;
    }
  }

  // Named volumes for the stack editor's attach picker (anonymous ones hidden).
  let namedVolumes: { name: string; kind?: string }[] = [];
  let volChoice = '';
  let volPath = '';
  let volRO = false;
  async function loadVolumes(stackId?: string) {
    try {
      const q = stackId ? '?stack=' + encodeURIComponent(stackId) : '';
      const res = await fetch('/api/volumes' + q);
      if (res.ok) namedVolumes = (await res.json()).filter((v: any) => !v.anonymous);
    } catch {}
  }

  // Restore view/stack from the URL hash so deep links + refresh work.
  async function restoreFromHash() {
    const [, section, name, tab] = location.hash.replace(/^#/, '').split('/');
    if (section === 'setup') {
      setupOpen = true;
      return;
    }
    if (section === 'store') {
      currentView = 'store';
      await selectStack(null);
    } else if (section === 'volumes') {
      currentView = 'volumes';
      await selectStack(null);
    } else if (section === 'networks') {
      currentView = 'networks';
      await selectStack(null);
    } else if (section === 'adopt') {
      currentView = 'adopt';
      await selectStack(null);
    } else if (section === 'system') {
      currentView = 'system';
      await selectStack(null);
    } else if (section === 'settings') {
      currentView = 'settings';
      // 'plugins' is the pre-rename route; keep it working for old links
      settingsTab = name === 'extensions' || name === 'plugins' ? 'extensions' : name === 'catalogs' || name === 'advanced' || name === 'account' ? name : 'storage';
      await selectStack(null);
    } else if (section === 'stacks' && name) {
      currentView = 'stacks';
      const dec = decodeURIComponent(name);
      const found = stacks.find((s) => s.name === dec);
      if (!found) {
        // A link to a stack that is not here -- mistyped, deleted, or with a
        // stray character ("mariadb:" from a pasted sentence). It used to
        // open an empty new-stack editor under that name, whose Logs tab then
        // showed a bare "HTTP 400".
        toast(`No stack named "${dec}"`, { kind: 'error' });
        currentView = 'stacks';
        await selectStack(null);
        return;
      }
      await selectStack(found);
      // Only a tab this stack has: makejail exists on a director stack alone.
      // The list carries no director file; the loaded stack does.
      if (tab === 'compose' || tab === 'env' || (tab === 'makejail' && selectedStack?.director)) activeTab = tab;
      else activeTab = 'net';
    } else {
      currentView = 'stacks';
      await selectStack(null);
    }
  }

  onMount(async () => {
    fetch('/api/about')
      .then((r) => (r.ok ? r.json() : null))
      .then((a) => {
        daemonVersion = a?.version ?? '';
        hostOS = a?.os ?? '';
      })
      .catch(() => {});
    loadAppIcons();
    try {
      const r = await fetch('/api/setup/state');
      if (r.ok && !(await r.json()).done) setupOpen = true;
    } catch {}
    // Tab title names the host so several fjords stay tellable apart.
    fetch('/api/about')
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => { if (d?.hostname) document.title = `fjord - ${d.hostname}`; })
      .catch(() => {});
    await loadStacks();
    loadNames();
    loadEngines();
    loadConditions();
    loadNetworks();
    loadVolumes();
    theme = currentTheme();
    unwatchTheme = watchSystem((t) => (theme = t));
    loadFleetUpdates();
    if (!document.hidden) subscribeEvents(); // opened in a background tab: on first view
    await restoreFromHash();
    routeReady = true;
    window.addEventListener('hashchange', restoreFromHash);
    window.addEventListener(SIGNED_IN, signedInAgain);
    document.addEventListener('visibilitychange', eventsFollowVisibility);
    // Shown while restoreFromHash was in flight: the listener missed it.
    if (!document.hidden && !eventSource) subscribeEvents();
  });

  // fjordd polls every stack while any browser holds the event stream open,
  // and a backgrounded tab holds it open indefinitely. Close the stream once
  // the tab has been hidden for a minute (a quick tab switch keeps it); on
  // return, resync what the missed events would have changed and reopen it.
  const HIDDEN_CLOSE_MS = 60_000;
  let hiddenTimer: ReturnType<typeof setTimeout> | undefined;
  function eventsFollowVisibility() {
    clearTimeout(hiddenTimer);
    if (document.hidden) {
      hiddenTimer = setTimeout(() => {
        eventSource?.close();
        eventSource = null;
      }, HIDDEN_CLOSE_MS);
    } else if (!eventSource) {
      resync();
    }
  }

  // Reload what the live stream would have kept current, then reopen it. The
  // list is awaited first so its snapshot cannot land after (and undo) a newer
  // delta from the reopened stream. The open stack gets what its stack-busy and
  // stack-removed events would have done: deleted -> deselected, busy state
  // taken from the list, an action still running followed, a finished one's
  // update badge refreshed.
  async function resync() {
    const wasBusy = selectedStack?.busy;
    await loadStacks();
    loadConditions();
    loadNetworks();
    loadVolumes();
    loadFleetUpdates();
    if (!document.hidden && (!eventSource || eventSource.readyState === EventSource.CLOSED)) subscribeEvents();
    if (!selectedStack) return;
    const found = stacks.find((s) => s.name === selectedStack!.name);
    if (!found) {
      // Only a stack that was on the server can have been deleted. A "+" draft
      // or a store install still in flight has no dir and isn't listed yet.
      if (selectedStack.dir) selectStack(null);
      return;
    }
    selectedStack = { ...selectedStack, busy: found.busy };
    if (found.busy) showActionOutput(found.name);
    else if (wasBusy === 'update' || wasBusy === 'up') checkForUpdate(found.name);
  }

  // Signed in again over this page (its session had ended): reload what
  // failed meanwhile and reopen the live stream.
  // Pages that load their own data (Settings, System, ...) are made anew;
  // the stack view's edits live here and the App store keeps an open wizard.
  let viewEpoch = 0;
  let showShortcuts = false; // the ? overlay
  async function signedInAgain() {
    viewEpoch++;
    loadEngines();
    await resync();
    if (selectedStack?.listOnly) selectStack(selectedStack);
  }

  onDestroy(() => {
    window.removeEventListener(SIGNED_IN, signedInAgain);
    document.removeEventListener('visibilitychange', eventsFollowVisibility);
    clearTimeout(hiddenTimer);
    eventSource?.close();
    unwatchTheme?.();
  });

  // Discard unsaved edits (compose/env, incl. mount-table changes) back to the
  // last saved version. Also clears the network/volume pickers and re-reads the
  function revert() {
    if (!selectedStack) return;
    selectedStack = { ...selectedStack, compose: originalCompose, env: originalEnv, ...(isDirector ? { director: originalDirector, makejail: originalMakejail } : {}) };
    toast('Reverted to the last saved version', { kind: 'success' });
  }

  // Set when a save was refused because fjord changed the compose on the
  // server (rollback, version change, unpin) after the editor loaded it.
  let saveConflict = '';
  // Re-read the stack from the server into the editor. Used when fjord
  // rewrote the compose itself, and by the conflict banner's Reload.
  async function reloadStack(name: string) {
    saveConflict = '';
    if (selectedStack?.name === name) await selectStack({ name } as Stack);
  }
  async function save() {
    if (!selectedStack) return;
    if (selectedStack.listOnly) {
      toast("This stack's files did not load, so there is nothing safe to save. Open it again.", { kind: 'error' });
      return;
    }
    saving = true;
    try {
      const body: Record<string, unknown> = {
        compose: selectedStack.compose,
        env: selectedStack.env,
        // Which version of the compose these edits were made to; the daemon
        // refuses the save if fjord has rewritten it since.
        baseHash: selectedStack.composeHash ?? '',
      };
      if (isDirector) {
        body.director = selectedStack.director;
        body.makejail = selectedStack.makejail ?? '';
      }
      if (isDraft && selectedStack.engine) body.engine = selectedStack.engine;
      if (isDraft && selectedStack.displayName && selectedStack.displayName !== selectedStack.name) body.displayName = selectedStack.displayName;
      // Attaching a volume injects a block into the compose -- a one-shot
      // action, so that picker is cleared after a successful save.
      // The network is only injected when it CHANGED: the picker now shows the
      // stack's current network, and re-injecting one the compose already
      // declares fails ("service already declares networks").
      if (svcNetsDirty) {
        // Modes travel separately from attachments: a built-in is set ON a
        // service, not joined, so posting "host" as a network name attached
        // the service to a network called host and the mode never changed.
        const { networks, modes } = splitPlan(svcNets as any);
        body.networks = networks;
        body.networkModes = modes;
      }
      if (volChoice && volPath.trim().startsWith('/')) {
        body.volume = volChoice;
        body.volumePath = volPath.trim();
        body.volumeRO = volRO;
      }
      const res = await fetch(`/api/stacks/${selectedStack.name}/save`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (res.ok) {
        saveConflict = '';
        const saved = await res.json().catch(() => ({}));
        if (saved.composeHash) selectedStack!.composeHash = saved.composeHash;
        // If we injected networks/a volume the on-disk compose changed;
        // re-fetch it so the editor shows the real (injected) compose.
        // NB "networks", plural: the singular field is gone, and testing only
        // the singular here left the Unsaved badge on forever after a network
        // save.
        if (body.networks || body.network || body.volume) {
          const detail = await fetch(`/api/stacks/${selectedStack.name}`);
          if (detail.ok) {
            selectedStack = await detail.json();
            seedSvcNets(selectedStack);
          }
          volChoice = '';
          volPath = '';
          volRO = false;
        }
        originalCompose = selectedStack!.compose;
        originalEnv = selectedStack!.env;
        originalDirector = selectedStack!.director ?? '';
        originalMakejail = selectedStack!.makejail ?? '';
        toast('Changes saved', { kind: 'success' });
        // A secret changed on a stack that has run: its database kept the
        // old one, and the app may be refused with no other explanation.
        for (const w of (saved.warnings ?? []) as string[]) toast(w, { kind: 'error', timeout: 20000 });
        // A running container keeps its creation-time config (ports, env) --
        // saved changes only apply on a recreate (`up`), NOT on restart. Flag
        // it so the UI can offer "Apply".
        if (statusLabel(selectedStack!.status) === 'running' || statusLabel(selectedStack!.status) === 'partial') {
          needsApply[selectedStack!.name] = true;
        }
        applyDismissed[selectedStack!.name] = false;
        await refreshState(selectedStack!.name);
        await loadStacks();
      } else if (res.status === 409) {
        saveConflict = selectedStack.name;
      } else {
        toast((await res.text()).trim() || `HTTP ${res.status}`, { kind: 'error' });
      }
    } finally {
      saving = false;
    }
  }

  // Run a streamed lifecycle action (up/down/restart/update) and pipe its
  // output into the terminal drawer.
  // onStart runs once the daemon has accepted the action and begun streaming --
  // for a rollback, the moment its compose is written.
  // Each run of an action on a stack has a number. One taken while another is
  // down to its stay-up check ends that check; from then on the older run must
  // not write into the newer one's output, nor say how the stack ended up.
  const runTracker = createRunTracker();
  const newRun = (name: string) => runTracker.newRun(name);

  function readRun(name: string, res: Response, mine: () => boolean): Promise<boolean> {
    return readActionStream(
      res,
      mine,
      (chunk) => {
        logs[name] += chunk;
      },
      () => {
        if (execStatus[name] === 'running') {
          execStatus[name] = 'watching';
          execMessage[name] = 'Checking it stays up';
          refreshState(name);
        }
      },
    );
  }

  async function streamAction(name: string, action: string, msg: string, body?: unknown, onStart?: () => void) {
    const mine = newRun(name);
    stackDetail?.stopLogs(); // action output goes to the Output tab, not the Logs stream
    // Only steer the drawer for the stack being looked at. Started from the
    // list, or left running while the operator moved on, this forced the tab
    // to Output over whatever they were reading.
    const watching = stillWatching(name);
    if (watching) drawerTab = 'output';
    // The backend announces its own "$ command" header lines in the stream --
    // it is the source of truth for what actually runs, whatever the engine.
    logs[name] = '';
    execStatus[name] = 'running';
    execMessage[name] = msg;
    if (watching) drawerOpen = true;
    try {
      const res = await fetch(
        `/api/stacks/${name}/${action}`,
        body === undefined
          ? { method: 'POST' }
          : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) },
      );
      if (!mine()) return;
      if (res.ok) onStart?.();
      if (!res.ok) {
        const why = (await res.text()).trim() || res.statusText;
        execStatus[name] = 'error';
        execMessage[name] = formatActionHttpError(why, res.status);
        logs[name] += `[ERROR]: ${why}\n`;
        return;
      }
      if (!(await readRun(name, res, mine))) return;
      // A streamed action answers 200 before it knows how it ends, so its
      // outcome is in the output: the daemon marks every failure "[error]".
      // Reading only the status made a failed update look like a success.
      if (/^\[error\]/m.test(logs[name])) {
        execStatus[name] = 'error';
        execMessage[name] = startError(logs[name]) || 'Failed — see Output';
        if (watching) drawerOpen = true;
      } else {
        execStatus[name] = 'idle';
        execMessage[name] = '';
      }
      await loadStacks();
      await refreshOpenStack(name);
      // The stack may still be "partial" (yellow) the instant an action
      // returns. The SSE stream (subscribeEvents) pushes the settle to
      // running/stopped as the containers finish -- no polling needed.
    } catch (err: any) {
      if (!mine()) return;
      execStatus[name] = 'error';
      execMessage[name] = firstLine(String(err?.message || err)) || 'Failed';
      logs[name] += `[ERROR]: ${err?.message || err}\n`;
    }
  }

  // After an action, the stack on screen has to be re-read: a recreate can
  // move a container to a new address, and the Open link, the addresses and
  // the Services rows all come from the detail loaded when it was opened --
  // tautulli's Open kept pointing at .200 after an update moved it to .201,
  // until a page reload. With no unsaved edits it is simply re-opened; with
  // some, only the live fields are taken, so an edit in progress survives.
  async function refreshOpenStack(name: string) {
    if (selectedStack?.name !== name || isDraft) return;
    if (!isDirty) {
      await selectStack({ name } as Stack);
      return;
    }
    try {
      const res = await fetch(`/api/stacks/${name}`);
      if (!res.ok || selectedStack?.name !== name) return;
      const fresh = await res.json();
      const { compose, env, director, makejail, composeHash, ...live } = fresh;
      selectedStack = { ...selectedStack, ...live };
    } catch {}
  }

  // Live updates: one SSE stream pushes stack state-changes, so the UI reflects
  // starts, stops and crashes (including external ones) without polling. The
  // baseline is the initial loadStacks(); this applies only deltas.
  let eventSource: EventSource | null = null;
  function subscribeEvents() {
    try {
      eventSource = new EventSource('/api/events');
      eventSource.onopen = () => {
        loadConditions();
      };
      eventSource.onmessage = (e) => {
        try {
          applyStackEvent(JSON.parse(e.data));
        } catch {}
      };
      // The browser retries a dropped stream by itself, but gives up on an
      // answer like 401: then the session may have ended.
      eventSource.onerror = () => {
        if (eventSource?.readyState === EventSource.CLOSED) checkSession();
      };
    } catch {}
  }
  const outputFollower = createOutputFollower();
  function showActionOutput(name: string): Promise<void> {
    return outputFollower.follow(name, {
      isStreamingHere: () => execStatus[name] === 'running' || execStatus[name] === 'watching',
      shouldKeepPolling: () => selectedStack?.name === name,
      onUpdate: (o, watching) => {
        if (o.text && (!logs[name] || logs[name] !== o.text)) logs[name] = o.text;
        if (o.text && !o.done) {
          execMessage[name] = watching ? 'Checking it stays up' : (BUSY_LABEL[o.action ?? ''] ?? `${o.action}…`);
        }
        following[name] = !!o.text && !o.done;
      },
      onFinish: () => {
        following[name] = false;
      },
    });
  }
  function applyStackEvent(ev: any) {
    if (ev?.type === 'conditions') {
      conditions = ev.conditions || [];
      return;
    }
    const res = reduceStackEvent(stacks, selectedStack, ev);
    if (res.stacks !== stacks) stacks = res.stacks;
    if (res.clearSelection) {
      selectStack(null);
      return;
    }
    if (res.selectedStack !== selectedStack) selectedStack = res.selectedStack;
    if (res.reloadStacks) loadStacks();
    if (res.followOutput) showActionOutput(res.followOutput);
    if (res.refreshState) refreshState(res.refreshState);
    if (res.refreshFleet) loadFleetUpdates();
    if (res.checkUpdate) checkForUpdate(res.checkUpdate);
  }

  // Stacks with saved-but-unapplied config changes (need a recreate via `up`).
  let needsApply: Record<string, boolean> = {};

  const up = async (name: string) => {
    await streamAction(name, 'up', 'Starting…');
    // Only a bring-up that succeeded applied the saved config; after a
    // refused one (port taken) the running containers still have the old.
    if (execStatus[name] !== 'error') needsApply[name] = false;
    await refreshState(name);
  };
  // fjordd's own record of what a Save left unapplied (pending_services): it
  // clears it only once the recreate is confirmed, so it is re-read after an
  // action rather than guessed from how the stream ended.
  async function refreshState(name: string) {
    const r = await fetch(`/api/stacks/${encodeURIComponent(name)}`).catch(() => null);
    if (!r?.ok) return;
    const d = await r.json();
    stacks = stacks.map((s) => (s.name === name ? { ...s, state: d.state } : s));
    if (selectedStack?.name === name) selectedStack = { ...selectedStack, state: d.state };
  }
  // "Not now" on the banner, for this session.
  let applyDismissed: Record<string, boolean> = {};
  // The last action that ended in an [error], as fjordd recorded it: the
  // install that died on a full disk left "stopped" and a stream nobody was
  // watching. Shown until the next action on the stack succeeds, or dismissed
  // for this session.
  let failureDismissed: Record<string, boolean> = {};
  // Hidden at once; fjordd forgets it too, so a reload does not bring it back.
  async function dismissFailure(name: string) {
    failureDismissed[name] = true;
    const res = await fetch(`/api/stacks/${name}/dismiss-failure`, { method: 'POST' }).catch(() => null);
    if (!res?.ok) toast(`Could not dismiss: ${res ? await res.text() : 'fjordd did not answer'}`, { kind: 'error' });
  }
  const down = (name: string) => streamAction(name, 'down', 'Stopping…');
  const restart = (name: string) => streamAction(name, 'restart', 'Restarting…');
  // services: pull and recreate only those; none = the whole stack.
  const update = async (name: string, services?: string[]) => {
    if (updatePanel === name) updatePanel = '';
    const some = !!services?.length;
    await streamAction(name, 'update', some ? `Updating ${services!.join(', ')}…` : 'Updating…', some ? { services } : undefined);
    // A whole-stack update recreates everything, so saved config is applied.
    // Updating some services leaves the rest on what they were started with.
    if (execStatus[name] !== 'error' && !some) needsApply[name] = false;
    await refreshState(name);
    if (selectedStack?.name === name) checkForUpdate(name); // refresh the badge
    loadFleetUpdates(true); // fleet badges should reflect the applied update
  };

  // Apply a version/pin change: rewrite the image ref (tag +/- digest) on the
  // server, refresh the detail, then redeploy only if the version actually
  // changed. Pin/unpin of the same version is the same bytes -- no redeploy.
  async function applyVersion(
    change: { name: string; image: string; suggest?: string; service?: string },
    tag: string,
    pin: boolean,
  ) {
    const name = change?.name;
    const curImage = change?.image ?? '';
    const service = change?.service;
    // Read before selectStack below clears updateInfo.
    const perService = !!updateInfo?.perService && !!updateInfo?.pinServices;
    if (!name) return;
    const tagChanged = tag !== refTag(curImage);
    const services = service && perService ? [service] : undefined;
    const res = await fetch(`/api/stacks/${name}/set-tag`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        tag,
        pin,
        ...(service ? { service } : {}),
        ...(tagChanged ? { update: true, ...(services ? { update_services: services } : {}) } : {}),
      }),
    });
    if (!res.ok) {
      const msg = (await res.text()).trim();
      logs[name] = (logs[name] || '') + `[ERROR]: set-tag failed: ${msg}\n`;
      execStatus[name] = 'error';
      drawerOpen = true;
      toast(`Version change failed: ${msg}`, { kind: 'error' });
      return;
    }
    if (tagChanged) {
      await followUpdate(name, res, services);
      return;
    }
    await selectStack({ name } as Stack); // re-fetch: compose now has the new ref
    checkForUpdate(name); // a pin alone runs the same bytes: nothing to recreate
  }

  // Prominent blocking overlay while a delete runs -- tearing containers down
  // can take seconds, and a small toast wasn't clear enough that work was
  // happening. `deleting` is the stack's display label; '' hides the overlay.
  let deleting = '';
  async function deleteStack(name: string, withData = false) {
    deleting = label(selectedStack) || name;
    try {
      const res = await fetch(`/api/stacks/${name}${withData ? '?data=1' : ''}`, { method: 'DELETE' });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      const gone = deleting;
      deleting = '';
      await selectStack(null);
      location.hash = '#/stacks';
      await loadStacks();
      toast(`Deleted ${gone}`, { kind: 'success' });
      // The daemon took its name away too, or it was the proxy itself.
      setTimeout(loadNames, 1500);
    } catch (e: any) {
      deleting = '';
      toast(`Failed to delete ${name}: ${e.message}`, { kind: 'error' });
    }
  }

  async function renameStack(id: string, newName: string) {
    // Instant: only the display label changes; the stack id/URL/containers stay.
    try {
      const res = await fetch(`/api/stacks/${encodeURIComponent(id)}/rename`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ newName }),
      });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      if (selectedStack?.name === id) selectedStack = { ...selectedStack, displayName: newName };
      await loadStacks();
      toast(`Renamed to ${newName}`, { kind: 'success' });
    } catch (e: any) {
      toast(`Rename failed: ${e.message}`, { kind: 'error' });
    }
  }

  async function handleInstall(e: CustomEvent<{
    name: string;
    appId: string;
    engine: string;
    manifest: string;
    values: Record<string, string>;
    paths?: Record<string, string[]>;
    appData?: string;
    tag: string;
    network: string;
    ip: string;
    mac?: string;
    /** service -> network spec ("default", "private", or a name); the
     *  daemon resolves it, since "private" names a segment it creates. */
    networkPlan?: Record<string, string>;
    /** One entry per interface, each naming the service it belongs to. The
     *  wizard's per-service editor sends these instead of networkPlan: the
     *  operator has already answered, so nothing is left to re-derive. */
    networks?: { network: string; service: string; ip: string; mac: string }[];
    /** service -> built-in mode (host/bridge/none). */
    networkModes?: Record<string, string>;
    choices?: Record<string, string>;
    /** The name to give it (radarr.apps.lan), once it runs. */
    givenName?: string;
    restart?: { policy: string; retries?: number };
    accepted?: () => void;
    refused?: (msg: string) => void;
  }>) {
    const { name, appId, engine, manifest, values, paths, appData, tag, network, ip, mac, networkPlan, networks: netList, networkModes, choices, restart } = e.detail;

    // Nothing is torn down or navigated to until the daemon has taken the
    // install. It refuses some of these outright -- an address that is a
    // network address, a name already used -- and those refusals are about
    // what was typed into the wizard, so the wizard has to still be there to
    // show them against. Closing it first threw away every other answer in
    // the form along with the one that was wrong.
    let key = '';
    let handed = false;
    let mine = () => true; // this install's run, once it has a stack to write to
    try {
      const res = await fetch('/api/apps/install', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, manifest, values, ...(paths ? { paths } : {}), ...(appData ? { appData } : {}), ...(appId ? { app_id: appId } : {}), ...(engine ? { engine } : {}), ...(tag ? { tag } : {}), ...(network ? { network, ip, ...(mac ? { mac } : {}) } : {}), ...(networkPlan ? { networkPlan } : {}), ...(netList?.length ? { networks: netList } : {}), ...(networkModes ? { networkModes } : {}), ...(choices ? { choices } : {}), ...(e.detail.givenName ? { givenName: e.detail.givenName } : {}), ...(restart ? { restart } : {}) }),
      });
      // The server sends the id as soon as the stack exists on disk -- also
      // when it then refuses to start it. So the header, not the status, says
      // whether there is a stack to go to: without one nothing was saved and
      // there is nothing to show but the wizard the request came from.
      const id = res.headers.get('X-Fjord-Stack-Id') || '';
      if (!res.ok && !id) {
        e.detail.refused?.((await res.text()).trim() || `HTTP ${res.status}`);
        return;
      }
      e.detail.accepted?.();
      // The stack is on disk now: a names provider's row and Open learn its
      // setup link at once (loadNames keeps looking while it comes up),
      // not after the install's stay-up check a minute later.
      loadNames();
      handed = true;
      // A 200 always carries the id. Keyed by a handle if one ever doesn't,
      // so the terminal still has somewhere to write.
      key = id || 'installing:' + name;
      currentView = 'stacks';
      selectedStack = { name: key, displayName: name, dir: '', compose: '', env: '' };
      originalCompose = '';
      originalEnv = '';
      originalDirector = '';
      originalMakejail = '';
      logs[key] = `Installing ${name}...\n`;
      mine = newRun(key);
      execStatus[key] = 'running';
      execMessage[key] = 'Installing...';

      if (!res.ok) {
        // Saved but not started (a folder it could not make, a network it
        // cannot use). Taken ports and addresses are refused before the save
        // and stay in the wizard. The stack is real, so land on it with the
        // reason in its output.
        const msg = (await res.text()).trim();
        execStatus[key] = 'error';
        execMessage[key] = firstLine(msg) || `HTTP ${res.status}`;
        logs[key] += `[ERROR]: ${msg}\n`;
        await loadStacks();
        const saved = stacks.find((s) => s.name === key) || null;
        // The toast already says it failed, so there is no reason to haul
        // someone off the page they chose.
        if (saved && stillWatching(key)) selectStack(saved);
        toast(`Install failed: ${msg || `HTTP ${res.status}`}`, { kind: 'error', timeout: 10000 });
        return;
      }
      // The server saves the stack BEFORE pulling, so it's readable now --
      // populate editor + sidebar instead of leaving them empty during the pull.
      (async () => {
        try {
          const d = await fetch(`/api/stacks/${encodeURIComponent(key)}`);
          if (d.ok && selectedStack?.name === key) {
            const full = await d.json();
            selectedStack = full;
            originalCompose = full.compose;
            originalEnv = full.env;
            originalDirector = full.director ?? '';
            originalMakejail = full.makejail ?? '';
          }
        } catch {}
        loadStacks();
      })();
      if (!(await readRun(key, res, mine))) return;
      execStatus[key] = 'idle';
      execMessage[key] = '';
      await loadStacks();
      const found = stacks.find((s) => s.name === key) || null;
      // Only if the operator is still looking at it. An install runs for as
      // long as the image takes to pull and the stack takes to come up, and
      // re-selecting on completion dragged whoever had moved on -- to the App
      // Store to queue the next one, typically -- back to this page. The
      // stack is saved and its status is live in the list either way.
      if (found && stillWatching(key)) selectStack(found);
      // The name the wizard asked for was given by the daemon at the end of
      // the stream (its Output says what came of it); read the names back so
      // Open and the row use it. A provider just installed shows Finish
      // setup on its row.
      await loadNames();
    } catch (err: any) {
      // Before the hand-off there is no stack and no terminal to write to;
      // the wizard is still up, so the failure belongs in it.
      if (!handed) {
        e.detail.refused?.(err?.message || String(err));
        return;
      }
      if (!mine()) return;
      execStatus[key] = 'error';
      execMessage[key] = firstLine(String(err?.message || err)) || 'Failed';
      logs[key] += `[ERROR]: ${err?.message || err}\n`;
    }
  }
</script>

<!-- Keys: the table in shortcuts.ts; never while typing in a field. -->
<svelte:window
  on:keydown={(e) => {
    if (typing(e)) return;
    const s = matchShortcut(e);
    if (!s) return;
    if (s.action === 'close') {
      if (showShortcuts) { showShortcuts = false; e.preventDefault(); }
      return;
    }
    e.preventDefault();
    if (s.action === 'help') showShortcuts = !showShortcuts;
    else if (s.action === 'search') (document.getElementById(currentView === 'store' ? 'app-search' : 'stack-search') as HTMLInputElement | null)?.focus();
    else if (s.action === 'refresh') { loadStacks(); loadConditions(); loadNetworks(); loadVolumes(); loadFleetUpdates(); toast('Refreshed'); }
    else if (s.go) location.hash = s.go;
  }}
/>
{#if showShortcuts}<Shortcuts version={daemonVersion} on:close={() => (showShortcuts = false)} />{/if}

{#if setupOpen}
  <SetupWizard on:done={setupDone} />
{:else}
<div class="h-screen flex text-fjord-fg-body overflow-hidden">
  <Sidebar
    bind:stacks
    {stacksLoaded}
    {stacksFailed}
    {selectedStack}
    {currentView}
    {fleet}
    {updatingAll}
    {daemonVersion}
    {narrow}
    bind:navOpen
    onReload={loadStacks}
    on:select={(e) => selectStack(e.detail)}
    on:new={() => selectStack(newStackDraft())}
    on:update-all={openUpdateAll}
    on:navigate={(e) => {
      selectStack(null);
      if (e.detail.settingsTab) settingsTab = e.detail.settingsTab;
      currentView = e.detail.view;
    }}
  />

  <main class="relative flex-1 min-w-0 bg-fjord-bg/95 flex flex-col h-screen overflow-hidden {narrow ? '' : 'pr-8'}">
    {#if narrow}
      <!-- Phone bar: the menu opens the drawer; the brand goes home. -->
      <div class="flex items-center gap-2 px-2 py-1.5 border-b border-fjord-border bg-fjord-bg shrink-0">
        <button on:click={() => (navOpen = true)} aria-label="Menu" class="w-10 h-10 grid place-items-center rounded-md text-fjord-fg-secondary hover:text-fjord-fg hover:bg-fjord-border">
          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h16"/></svg>
        </button>
        <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
        <div class="flex items-center gap-2 text-base font-bold tracking-widest text-fjord-fg cursor-pointer" on:click={() => { selectStack(null); currentView = 'stacks'; }}>
          <Icon name="mountain" size={18} class="text-fjord-accent" />
          <span><span class="text-fjord-accent">F</span>JORD</span>
        </div>
      </div>
    {/if}
    <!-- Theme toggle: floats in main's right gutter so it stays top-right in
         every view without colliding with each view's own action row. -->
    <button
      on:click={toggleTheme}
      title={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
      aria-label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
      class="absolute top-4 right-4 z-30 w-8 h-8 flex items-center justify-center rounded-lg text-fjord-fg-muted hover:text-fjord-fg hover:bg-fjord-border transition-colors"
      ><Icon name={theme === 'dark' ? 'sun' : 'moon'} size={16} /></button
    >
    <ConditionsBanner {conditions} on:changed={loadConditions} />
    {#if selectedStack}
      <StackDetail
        bind:this={stackDetail}
        bind:stack={selectedStack}
        {stacks}
        bind:activeTab
        {originalCompose}
        {originalEnv}
        {originalDirector}
        {originalMakejail}
        {hostOS}
        {engines}
        {defaultEngine}
        {saving}
        {isDirty}
        {isDraft}
        bind:drawerOpen
        bind:drawerTab
        {networks}
        bind:svcNets
        {namedVolumes}
        {updateInfo}
        {updateCheckedAt}
        {checkingUpdate}
        bind:updatePanel
        bind:saveConflict
        bind:logs
        {execStatus}
        {following}
        {execMessage}
        bind:needsApply
        bind:applyDismissed
        {failureDismissed}
        onSave={save}
        onRevert={revert}
        onReloadStack={reloadStack}
        onSelectStack={selectStack}
        onLoadStacks={loadStacks}
        onLoadVolumes={loadVolumes}
        onCheckForUpdate={checkForUpdate}
        onUp={up}
        onDown={down}
        onRestart={restart}
        onUpdate={update}
        onRollback={rollback}
        onUnpin={unpin}
        onUpgradeTo={upgradeTo}
        onUpdatePicked={updatePicked}
        onApplyVersion={applyVersion}
        onDeleteStack={deleteStack}
        onRenameStack={renameStack}
        onSetStackGroup={setStackGroup}
        onDismissFailure={dismissFailure}
      />
    {:else if currentView === 'store'}
      <div class="p-6 h-full overflow-hidden">
        <AppStore stackIds={stacks.map((s) => s.name)} on:install={handleInstall} />
      </div>
    {:else if currentView === 'volumes'}
      <div class="p-6 h-full overflow-hidden">
        {#key viewEpoch}<Volumes />{/key}
      </div>
    {:else if currentView === 'networks'}
      <div class="p-6 h-full overflow-hidden">
        {#key viewEpoch}<Networks />{/key}
      </div>
    {:else if currentView === 'adopt'}
      <div class="p-6 h-full overflow-hidden">
        {#key viewEpoch}<Adopt
          on:back={() => (currentView = 'stacks')}
          on:adopted={async (e) => {
            await loadStacks();
            if (e.detail.id) {
              const st = stacks.find((x) => x.name === e.detail.id);
              if (st) {
                await selectStack(st);
                if (e.detail.start) up(st.name);
              }
            }
          }}
        />{/key}
      </div>
    {:else if currentView === 'system'}
      <div class="p-6 h-full overflow-hidden">
        {#key viewEpoch}<System />{/key}
      </div>
    {:else if currentView === 'settings'}
      <div class="p-6 h-full overflow-hidden">
        {#key viewEpoch}<Settings tab={settingsTab} on:tab={(e) => (settingsTab = e.detail)} />{/key}
      </div>
    {:else}
      <!-- Default landing: dashboard overview of all stacks -->
      <div class="p-6 h-full overflow-hidden">
        <Dashboard
          {stacks}
          loaded={stacksLoaded}
          failed={stacksFailed}
          on:retry={loadStacks}
          {fleet}
          {fleetRefreshing}
          {updatingAll}
          bind:updateAllOpen={dashboardUpdateAllOpen}
          on:update-all={(e) => updateAll(Array.isArray(e.detail) ? e.detail : undefined)}
          on:store={() => {
            selectStack(null);
            currentView = 'store';
          }}
          on:adopt={() => {
            selectStack(null);
            currentView = 'adopt';
          }}
          on:select={(e) => {
            const f = stacks.find((s) => s.name === e.detail) ?? { name: e.detail, dir: '', compose: '', env: '' };
            selectStack(f);
          }}
          on:action={(e) => {
            const { name, action } = e.detail;
            ({ up, down, restart, update } as Record<string, (n: string) => Promise<void>>)[action]?.(name);
          }}
          on:new={() =>
            selectStack(newStackDraft())}
        />
      </div>
    {/if}
  </main>
</div>
{/if}

{#if deleting}
  <div class="fixed inset-0 bg-black/70 backdrop-blur-sm flex items-center-safe justify-center overflow-y-auto z-50 p-4">
    <div class="bg-fjord-card border border-fjord-border rounded-xl shadow-2xl w-full max-w-sm p-8 flex flex-col items-center text-center gap-4">
      <div class="w-14 h-14 rounded-full bg-fjord-danger/15 border border-fjord-danger/30 flex items-center justify-center text-fjord-danger">
        <Spinner size={26} class="text-fjord-danger" />
      </div>
      <div>
        <h3 class="text-lg font-bold text-fjord-fg">Deleting {deleting}</h3>
        <p class="text-sm text-fjord-fg-muted mt-1">Stopping and removing its services. This can take a few seconds…</p>
      </div>
    </div>
  </div>
{/if}

<Toasts />
