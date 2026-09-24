<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte';
  import RemoteFolderForm from './RemoteFolderForm.svelte';
  import { ensureRemoteVolume, remoteKind, type RemoteKind } from './remote';
  import Editor from './Editor.svelte';
  import Terminal from './Terminal.svelte';
  import AppStore from './AppStore.svelte';
  import Volumes from './Volumes.svelte';
  import Networks from './Networks.svelte';
  import Adopt from './Adopt.svelte';
  import System from './System.svelte';
  import Settings from './Settings.svelte';
  import SetupWizard from './SetupWizard.svelte';
  import Dashboard from './Dashboard.svelte';
  import LogView from './LogView.svelte';
  import Shell from './Shell.svelte';
  import ChangeVersionModal from './ChangeVersionModal.svelte';
  import DirPicker from './DirPicker.svelte';
  import Spinner from './Spinner.svelte';
  import Icon from './Icon.svelte';
  import ServiceResources from './ServiceResources.svelte';
  import { splitPlan } from './planSeed';
  import EngineMark from './EngineMark.svelte';
  import { appIcons, loadAppIcons, iconFor, tile, initial } from './appIcons';
  import Toasts from './Toasts.svelte';
  import { toast, dismissToast } from './toast';
  import { expandVars } from './expand';
  import { appUrl } from './appUrl';
  import { currentTheme, setTheme, watchSystem, type Theme } from './theme';
  import { addressProblem, usableRange, networkLabel, HOST_NETWORK, DEFAULT_NETWORK, randomMAC } from './network';

  type ContainerStatus = {
    name: string;
    service?: string; // the compose service it runs
    state: string;
    ports?: { hostPort: number; containerPort: number; protocol?: string }[];
    detail?: string; // one-line reason for a non-running state (appjail: crash-looping app)
    address?: string; // the container's own IP on an attachable network
  };
  type StackStatus = { state: string; containers: ContainerStatus[] };
  type StackState = { group?: string; desired_state?: string; engine?: string; order?: number; origin?: { app_id?: string } };
  type Stack = { name: string; displayName?: string; icon?: string; dir: string; compose: string; env: string; director?: string; makejail?: string; engine?: string; status?: StackStatus; state?: StackState; services?: any[]; composeHash?: string };
  // What the UI shows for a stack: its label, falling back to the id.
  const label = (s: { name: string; displayName?: string } | null | undefined) => s?.displayName || s?.name || '';

  const STATUS_STYLE: Record<string, string> = {
    running: 'bg-fjord-success/10 border-fjord-success/20 text-fjord-success',
    partial: 'bg-fjord-warning/10 border-fjord-warning/20 text-fjord-warning',
    stopped: 'bg-fjord-neutral/10 border-fjord-neutral/20 text-fjord-fg-muted',
    unknown: 'bg-fjord-danger/10 border-fjord-danger/20 text-fjord-danger',
  };

  function statusLabel(status: StackStatus | undefined): string {
    return status?.state ?? 'unknown';
  }

  function statusStyle(status: StackStatus | undefined): string {
    return STATUS_STYLE[statusLabel(status)] ?? STATUS_STYLE.unknown;
  }

  const DOT: Record<string, string> = {
    running: 'bg-fjord-success',
    partial: 'bg-fjord-warning',
    stopped: 'bg-fjord-neutral',
    unknown: 'bg-fjord-danger',
  };
  function dot(status: StackStatus | undefined): string {
    return DOT[statusLabel(status)] ?? DOT.unknown;
  }

  let stacks: Stack[] = [];
  let logs: Record<string, string> = {};
  let execStatus: Record<string, 'idle' | 'running' | 'error'> = {};
  let execMessage: Record<string, string> = {};
  let selectedStack: Stack | null = null;
  let activeTab: 'compose' | 'makejail' | 'env' | 'net' = 'compose';
  let originalCompose = '';
  let originalEnv = '';
  let originalDirector = '';
  let originalMakejail = '';
  let currentView: 'stacks' | 'store' | 'volumes' | 'networks' | 'system' | 'settings' | 'adopt' = 'stacks';
  // Settings sub-tab, mirrored into the URL (#/settings/<tab>) like the views.
  let settingsTab: 'storage' | 'extensions' | 'catalogs' | 'advanced' = 'storage';

  // Draft for the "+" button: a FreeBSD base image that comes Up as-is, so a
  // blank stack is immediately a box you can open a shell in. A podman draft is
  // a compose; an appjail draft is a native director spec + Makejail (no
  // compose) that runs via appjail-director from the start.
  const NEW_STACK_IMAGE = 'ghcr.io/daemonless/base:15.1-latest';
  const NEW_STACK_COMPOSE = `services:\n  app:\n    image: ${NEW_STACK_IMAGE}\n`;
  const NEW_STACK_MAKEJAIL = `OPTION container=boot\nOPTION overwrite=force\nOPTION from=${NEW_STACK_IMAGE}\n`;
  // Jail name is namespaced by the stack id, like catalog installs.
  const newStackDirector = (name: string) =>
    `options:\n  - virtualnet: ':<random> default'\n  - nat:\nservices:\n  app:\n    name: ${name.replace(/[^A-Za-z0-9_]/g, '_')}_app\n    oci:\n      environment:\n        - TZ: !ENV '\${TZ}'\n`;
  let engines: { name: string; available: boolean; enabled: boolean }[] = [];
  let defaultEngine = 'podman';
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
    engine === 'appjail'
      ? { name, dir: '', compose: '', env: `DIRECTOR_PROJECT=${name}\nTZ=UTC\n`, director: newStackDirector(name), makejail: NEW_STACK_MAKEJAIL, engine }
      : { name, dir: '', compose: NEW_STACK_COMPOSE, env: 'TZ=UTC\n', engine };
  // Switch a draft's runtime: swap in that engine's starter files.
  function setDraftEngine(engine: string) {
    if (!selectedStack || !isDraft) return;
    selectedStack = newStackDraft(engine, selectedStack.name);
    activeTab = 'compose';
  }
  let saving = false;
  let actionsMenuOpen = false; // stack header overflow menu
  let editingName = false; // inline click-to-rename on the stack title
  let nameEdit = '';
  $: nameEditValid = /^[\p{L}\p{N}][\p{L}\p{N} ._-]{0,63}$/u.test(nameEdit);

  // Commit the inline rename: updates the stack's display label. Identity (id,
  // URL, containers) is unchanged, so it's instant -- no navigation, no restart.
  function commitRename() {
    editingName = false;
    const id = selectedStack?.name;
    const next = nameEdit.trim();
    if (!id || !next || next === label(selectedStack) || !nameEditValid) return;
    if (isDraft) {
      // Nothing on the server yet: keep the label on the draft; Create sends it.
      selectedStack = { ...selectedStack!, displayName: next };
      return;
    }
    renameStack(id, next);
  }
  let routeReady = false; // gate URL writes until the initial hash is restored
  // First-run setup wizard: shown until finished/skipped, or on #/setup.
  let setupOpen = false;
  async function setupDone() {
    setupOpen = false;
    currentView = 'store';
    await selectStack(null);
    location.hash = '#/store';
  }
  let search = ''; // sidebar stack filter
  let drawerOpen = true; // bottom panel — open by default
  try {
    const d = localStorage.getItem('fjord.drawerOpen');
    if (d !== null) drawerOpen = d === '1';
  } catch {}
  function setDrawerOpen(v: boolean) {
    drawerOpen = v;
    try {
      localStorage.setItem('fjord.drawerOpen', v ? '1' : '0');
    } catch {}
  }
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
  let containerLogs: Record<string, string> = {};

  // Resizable panels, persisted across refreshes. Mutations go through named
  // functions (not inline arrows in the action param) so Svelte 5 instruments
  // the assignment reactively AND the localStorage save actually runs.
  const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
  const loadSize = (key: string, def: number) => {
    try {
      return Number(localStorage.getItem(key)) || def;
    } catch {
      return def;
    }
  };
  const saveSize = (key: string, v: number) => {
    try {
      localStorage.setItem(key, String(v));
    } catch {}
  };
  let sidebarWidth = loadSize('fjord.sidebarW', 288); // w-72
  let drawerHeight = loadSize('fjord.drawerH', 256); // h-64
  function setSidebar(w: number) {
    sidebarWidth = clamp(w, 200, 640);
    saveSize('fjord.sidebarW', sidebarWidth);
  }
  function setDrawer(h: number) {
    drawerHeight = clamp(h, 120, 640);
    saveSize('fjord.drawerH', drawerHeight);
  }

  // Drag-to-resize action: reports incremental pointer deltas to onMove.
  function resizer(node: HTMLElement, opts: { axis: 'x' | 'y'; onMove: (delta: number) => void }) {
    let last = 0;
    const pos = (e: PointerEvent) => (opts.axis === 'x' ? e.clientX : e.clientY);
    function move(e: PointerEvent) {
      const cur = pos(e);
      opts.onMove(cur - last);
      last = cur;
    }
    function up(e: PointerEvent) {
      node.releasePointerCapture(e.pointerId);
      node.removeEventListener('pointermove', move);
      node.removeEventListener('pointerup', up);
      document.body.style.cursor = '';
      document.body.style.userSelect = '';
    }
    function down(e: PointerEvent) {
      e.preventDefault();
      last = pos(e);
      node.setPointerCapture(e.pointerId);
      node.addEventListener('pointermove', move);
      node.addEventListener('pointerup', up);
      document.body.style.cursor = opts.axis === 'x' ? 'col-resize' : 'row-resize';
      document.body.style.userSelect = 'none';
    }
    node.addEventListener('pointerdown', down);
    return { destroy: () => node.removeEventListener('pointerdown', down) };
  }
  // Change-version modal: {name, image} of the stack being retagged, or null.
  // service set: that service alone (the Services tab); unset: the whole
  // single-image stack (the ⋮ menu).
  let changeVersion: { name: string; image: string; suggest?: string; service?: string } | null = null;

  // Pull the first service image out of a stack's compose (for the retag modal).
  function stackImage(compose: string): string {
    const m = compose.match(/^\s*image:\s*["']?([^\s"'#]+)/m);
    return m ? m[1] : '';
  }
  // Multi-image stacks (e.g. app+db+redis) have no single version to change --
  // the Change Version modal would retag every service. Hidden for those.
  $: multiImage = (selectedStack?.compose.match(/^\s*image:/gm) || []).length > 1;
  // A director-backed appjail stack: its runtime spec is appjail-director.yml,
  // not compose.yaml (kept only for status/log service discovery). The primary
  // tab edits the director spec; director reconciles changes on the next up.
  $: isDirector = !!selectedStack?.director;

  $: filtered = stacks.filter((s) => s.name.toLowerCase().includes(search.toLowerCase()));

  // Sidebar grouping by state.group. Ungrouped ('') sorts last, no header.
  let collapsedGroups: Record<string, boolean> = {};
  try {
    collapsedGroups = JSON.parse(localStorage.getItem('fjord.collapsedGroups') || '{}');
  } catch {}
  function toggleGroup(g: string) {
    collapsedGroups[g] = !collapsedGroups[g];
    collapsedGroups = collapsedGroups;
    try {
      localStorage.setItem('fjord.collapsedGroups', JSON.stringify(collapsedGroups));
    } catch {}
  }
  // Manual sidebar order: state.order (1-based) wins; unset (0) sorts last by
  // numeric id so fresh installs land at the bottom in a stable order.
  const orderKey = (s: Stack) => (s.state?.order && s.state.order > 0 ? s.state.order : Number.MAX_SAFE_INTEGER);
  $: ordered = [...filtered].sort(
    (a, b) => orderKey(a) - orderKey(b) || a.name.localeCompare(b.name, undefined, { numeric: true }),
  );
  // Sidebar grouping mode: user-defined groups, by engine, network, status,
  // or flat.
  // Only 'groups' is drag-editable (the others are derived buckets).
  type GroupMode = 'groups' | 'engine' | 'app' | 'network' | 'status' | 'flat';
  let groupMode: GroupMode = 'groups';
  let groupMenuOpen = false;
  try {
    const m = localStorage.getItem('fjord.groupMode') as GroupMode;
    if (['groups', 'engine', 'app', 'network', 'status', 'flat'].includes(m)) groupMode = m;
  } catch {}
  function setGroupMode(m: GroupMode) {
    groupMode = m;
    try {
      localStorage.setItem('fjord.groupMode', m);
    } catch {}
  }
  const GROUP_MODES: { id: GroupMode; label: string }[] = [
    { id: 'groups', label: 'Groups' },
    { id: 'engine', label: 'Engine' },
    { id: 'app', label: 'App' },
    { id: 'network', label: 'Network' },
    { id: 'status', label: 'Status' },
    { id: 'flat', label: 'Flat' },
  ];
  // Which bucket a stack falls in for the active mode.
  const bucketOf = (s: Stack): string => {
    if (groupMode === 'engine') return s.state?.engine || 'podman';
    if (groupMode === 'status') return s.status?.state || 'unknown';
    if (groupMode === 'network') return networkLabel(s.compose);
    if (groupMode === 'flat') return '';
    // By app: the catalog app id (two installs of the same app group together),
    // falling back to the display name for stacks with no recorded origin (BYO).
    if (groupMode === 'app') return s.state?.origin?.app_id || s.displayName || s.name;
    return (s.state?.group || '').trim();
  };
  const STATUS_RANK: Record<string, number> = { running: 0, partial: 1, stopped: 2, unknown: 3 };
  $: groupedStacks = (() => {
    const m = new Map<string, Stack[]>();
    for (const s of ordered) {
      const k = bucketOf(s);
      if (!m.has(k)) m.set(k, []);
      m.get(k)!.push(s);
    }
    if (groupMode === 'groups') {
      // Guarantee an (even empty) ungrouped bucket as a drop-out zone when any
      // group is in use.
      const named = [...m.keys()].some((k) => k !== '');
      if (named && !m.has('')) m.set('', []);
      return [...m.entries()].sort((a, b) => (a[0] === '' ? 1 : b[0] === '' ? -1 : a[0].localeCompare(b[0])));
    }
    if (groupMode === 'status') {
      return [...m.entries()].sort((a, b) => (STATUS_RANK[a[0]] ?? 9) - (STATUS_RANK[b[0]] ?? 9));
    }
    if (groupMode === 'network') {
      // Named networks first -- those are the interesting buckets; the generic
      // default bridge, host mode and unparseable compose sink to the bottom.
      const rank = (n: string) =>
        n === 'unknown' ? 3 : n === HOST_NETWORK ? 2 : n === DEFAULT_NETWORK ? 1 : 0;
      return [...m.entries()].sort((a, b) => rank(a[0]) - rank(b[0]) || a[0].localeCompare(b[0]));
    }
    return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0])); // engine / app / flat
  })();
  // Headers show for user groups (when present) and always for engine/status
  // buckets; flat has no headers. Drag-to-regroup is groups-only.
  $: hasGroups = groupMode === 'groups' && groupedStacks.some(([g]) => g !== '');
  $: draggableStacks = groupMode === 'groups';
  $: existingGroups = [...new Set(stacks.map((s) => (s.state?.group || '').trim()).filter(Boolean))].sort();

  // ---- sidebar drag-and-drop: reorder stacks and move them between groups ----
  let dragName = ''; // stack being dragged
  let dropTarget = ''; // row currently hovered ('' = none)
  let dropAfter = false; // insert below (vs above) the hovered row
  let dropGroup: string | null = null; // group header/zone hovered ('' = ungrouped, null = none)
  const groupOf = (name: string) => (stacks.find((s) => s.name === name)?.state?.group || '').trim();

  function onRowDragOver(e: DragEvent, name: string) {
    if (!dragName) return;
    e.preventDefault();
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    dropAfter = e.clientY > r.top + r.height / 2;
    dropTarget = name;
    dropGroup = null;
  }
  function onGroupDragOver(e: DragEvent, group: string) {
    if (!dragName) return;
    e.preventDefault();
    dropGroup = group;
    dropTarget = '';
  }
  function clearDrag() {
    dragName = '';
    dropTarget = '';
    dropGroup = null;
  }

  // Commit a drop: rebuild the flat top-to-bottom order (dragged item removed,
  // then reinserted at the target) and the dragged item's new group, persist the
  // whole layout, and update locally so the sidebar doesn't flicker.
  async function commitReorder(targetName: string | null, after: boolean, group: string) {
    const name = dragName;
    if (!name) return;
    if (targetName === name && group === groupOf(name)) return clearDrag(); // dropped on itself
    const flat = ordered.map((s) => s.name).filter((n) => n !== name);
    let idx: number;
    if (targetName && targetName !== name) {
      idx = flat.indexOf(targetName) + (after ? 1 : 0);
    } else {
      // Dropped on a group header/zone: append after that group's last member.
      const last = ordered.filter((s) => (s.state?.group || '').trim() === group && s.name !== name).pop();
      idx = last ? flat.indexOf(last.name) + 1 : flat.length;
    }
    flat.splice(idx < 0 ? flat.length : idx, 0, name);

    // Optimistic local update: stamp order by new position + the dragged group.
    const pos = new Map(flat.map((n, i) => [n, i + 1]));
    stacks = stacks.map((s) => {
      if (!pos.has(s.name)) return s;
      const state = { ...(s.state || {}), order: pos.get(s.name)! };
      if (s.name === name) state.group = group;
      return { ...s, state };
    });
    clearDrag();

    const items = flat.map((n) => ({ name: n, group: n === name ? group : groupOf(n) }));
    try {
      await fetch('/api/stacks/reorder', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ items }),
      });
    } catch {}
    await loadStacks(); // reconcile with server truth
  }

  // Derive a clickable URL for a webapp stack: its own IP when it has one,
  // else the host you're browsing fjord on, plus its primary published web
  // port. Empty when the stack publishes nothing web-ish.

  $: openUrl = appUrl(selectedStack);
  // Attached to a network but holding no address: the reason the Open button
  // is missing, taken from whichever container reported it.
  $: noAddress =
    selectedStack && (selectedStack as any).ownAddress && !openUrl
      ? (selectedStack.status?.containers || []).find((c: any) => c.detail?.includes('no address'))?.detail ||
        `no address on ${(selectedStack as any).network} yet`
      : '';

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

  // Inline confirmation for destructive stack actions (Stop / Delete): a
  // banner under the stack header, not a modal, so the rest of the app stays
  // usable and the question stays visible. Cleared when the selection changes.
  let pendingAction: { kind: 'stop' | 'delete' | 'recreate'; stack: string } | null = null;
  let pendingConfirmBtn: HTMLButtonElement | null = null;
  $: if (pendingAction && selectedStack?.name !== pendingAction.stack) pendingAction = null;
  // Focus the confirm button so Enter confirms and Escape cancels from the keyboard.
  $: if (pendingAction) tick().then(() => pendingConfirmBtn?.focus());
  function confirmPending() {
    const a = pendingAction;
    pendingAction = null;
    if (!a) return;
    if (a.kind === 'stop') down(a.stack);
    else if (a.kind === 'recreate') update(a.stack);
    else deleteStack(a.stack);
  }

  // Networks a stack can be given an address on (empty on a host with none).
  // problem: the daemon's reason a network cannot be used here (a CNI plugin
  // that is not installed, typically). The picker grays those out rather than
  // offering an attachment Save would refuse.
  type Network = { name: string; driver: string; subnet: string; gateway: string; problem?: string; bridge?: string; addressSource?: string };
  let networks: Network[] = [];
  type Attachment = { network: string; service?: string; ip?: string; mac?: string; iface?: string };
  // Per-service interfaces, staged here so Save can post them and the dirty
  // check can see them. Keyed by service name.
  let svcNets: Record<string, Attachment[]> = {};
  let savedSvcNets = '{}';
  $: svcNetsDirty = JSON.stringify(svcNets) !== savedSvcNets;

  // Which built-ins this engine cannot give a service. An appjail director
  // project takes bridge -- that is its own NAT virtualnet -- but not host or
  // none, which are jail parameters rather than director options. The rows are
  // where a mode is chosen now, so this goes to them.
  $: unsupportedModes = ((selectedStack as any)?.unsupportedModes ?? []) as string[];

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
    const h = selectedStack
      ? '#/stacks/' + encodeURIComponent(selectedStack.name)
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
      history[rename ? 'replaceState' : 'pushState'](null, '', h);
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

  // The Update panel. Update used to act on click -- pull everything and
  // recreate every container whether or not anything had changed, with no
  // word about which part of the stack was behind. Now the button opens this
  // list first, and acting on it is a second, deliberate click.
  let updatePanel = '';
  $: if (updatePanel && selectedStack?.name !== updatePanel) updatePanel = '';
  // Older than this and the panel asks again before offering to act on it.
  const UPDATE_FRESH_MS = 2 * 60 * 1000;
  function openUpdatePanel(name: string) {
    updatePanel = name;
    unpicked = {};
    if (!updateInfo || Date.now() - updateCheckedAt > UPDATE_FRESH_MS) checkForUpdate(name);
  }
  $: updatable = (updateInfo?.services ?? []).filter((s) => s.state === 'available');
  // What the panel can take one service at a time: a new build, or a new
  // version (the service is retagged first). All ticked by default; the ones
  // unticked are remembered, so a re-check keeps the choice.
  $: offered = updateInfo?.perService
    ? (updateInfo.services ?? []).filter((s) => s.state === 'available' || (s.state === 'upgrade' && !!s.newTag))
    : [];
  let unpicked: Record<string, boolean> = {};
  $: picked = offered.filter((s) => !unpicked[s.service]);
  $: svcPinned = Object.fromEntries(
    (updateInfo?.services ?? []).filter((s) => s.state === 'pinned').map((s) => [s.service, true]),
  ) as Record<string, boolean>;
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
  // Service -> its pending update (new image or new version), for the rows.
  $: svcUpdates = Object.fromEntries(
    (updateInfo?.services ?? []).filter((s) => s.state === 'available' || s.state === 'upgrade').map((s) => [s.service, s]),
  ) as Record<string, ServiceUpdate>;
  // Update applies a moved tag. A new VERSION needs the compose to name a
  // different tag -- Change Version -- and nothing is gained by recreating.
  $: updateButtonReason =
    updateInfo?.state === 'current'
      ? 'Up to date — nothing to pull'
      : updateInfo?.state === 'pinned'
        ? 'Pinned to an exact image — nothing to pull'
        : updateInfo?.state === 'upgrade' && multiImage && !updateInfo.perService
          ? `A newer version is published (v${updateInfo.toVersion}) — set it in the compose editor`
          : '';
  // A single-image stack behind by a VERSION: Update takes it there. It used
  // to be refused ("use Change Version"), so a downgraded stack showed an
  // update next to a greyed-out Update button. Multi-image stacks cannot be
  // retagged in one go (set-tag refuses them), so there it stays a note.
  $: versionStep =
    updateInfo?.state === 'upgrade' && !multiImage && !updateInfo.perService && updateInfo.newTag
      ? { tag: updateInfo.newTag, from: updateInfo.fromVersion, to: updateInfo.toVersion }
      : null;
  async function upgradeTo(name: string, tag: string) {
    updatePanel = '';
    const res = await fetch(`/api/stacks/${name}/set-tag`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ tag, pin: false }),
    });
    if (!res.ok) {
      toast(`Version change failed: ${(await res.text()).trim()}`, { kind: 'error' });
      return;
    }
    await selectStack({ name } as Stack); // the compose now names the new tag
    await update(name);
  }
  // The picked services, new versions retagged first -- one service each, so
  // the database is never moved to the app's version -- then one update.
  async function updatePicked(name: string, svcs: ServiceUpdate[]) {
    updatePanel = '';
    const bumps = svcs.filter((s) => s.state === 'upgrade');
    for (const s of bumps) {
      const res = await fetch(`/api/stacks/${name}/set-tag`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tag: s.newTag, service: s.service, pin: false }),
      });
      if (!res.ok) {
        toast(`Could not move ${s.service} to v${s.toVersion}: ${(await res.text()).trim()}`, { kind: 'error' });
        return;
      }
    }
    if (bumps.length) await selectStack({ name } as Stack); // the compose names the new tags
    await update(name, svcs.map((s) => s.service));
  }
  const shortDigest = (d?: string) => (d ?? '').replace('sha256:', '').slice(0, 12);

  // What each pending update changes, from what the two images say about
  // themselves in the registry (SBOM, else version labels). Fetched when the
  // panel shows a service as behind; keyed by stack:service:target so a new
  // check result asks again.
  type Changes = {
    class?: string; // rebuild | patch | minor | major | unknown
    versionFrom?: string;
    versionTo?: string;
    createdFrom?: string;
    createdTo?: string;
    packages: boolean;
    changed?: { name: string; from: string; to: string }[];
    added?: { name: string; version: string }[];
    removed?: { name: string; version: string }[];
  };
  let changes: Record<string, Changes | 'loading' | 'none'> = {};
  let changesOpen: Record<string, boolean> = {};
  const changesKey = (stack: string, s: ServiceUpdate) => `${stack}:${s.service}:${s.latest ?? s.newTag ?? ''}`;
  $: if (updatePanel && updateInfo && !checkingUpdate) {
    for (const s of updateInfo.services ?? []) {
      if (s.state !== 'available' && s.state !== 'upgrade') continue;
      const k = changesKey(updatePanel, s);
      if (changes[k]) continue;
      changes[k] = 'loading';
      fetch(`/api/stacks/${updatePanel}/changes?service=${encodeURIComponent(s.service)}`)
        .then((r) => (r.ok ? r.json() : 'none'))
        .catch(() => 'none')
        .then((d) => (changes = { ...changes, [k]: d }));
    }
  }
  const day = (iso?: string) => (iso ? new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) : '');
  // One line: the version move (or "same version"), then what the SBOMs say.
  function changesLine(c: Changes): string {
    // The class first: it is what an update policy acts on, and "rebuild"
    // says more at a glance than the version pair that follows it.
    const parts: string[] = c.class && c.class !== 'unknown' ? [c.class] : [];
    if (c.versionFrom && c.versionTo) {
      parts.push(c.versionFrom === c.versionTo ? `same version (${c.versionTo})` : `${c.versionFrom} → ${c.versionTo}`);
    } else if (c.versionTo) {
      parts.push(c.versionTo);
    }
    if (c.createdFrom && c.createdTo && day(c.createdFrom) !== day(c.createdTo)) {
      parts.push(`built ${day(c.createdFrom)} → ${day(c.createdTo)}`);
    }
    if (c.packages) {
      const n = (c.changed?.length ?? 0) + (c.added?.length ?? 0) + (c.removed?.length ?? 0);
      if (!n) parts.push('no package changes');
      else {
        const bits = [];
        if (c.changed?.length) bits.push(`${c.changed.length} changed`);
        if (c.added?.length) bits.push(`${c.added.length} added`);
        if (c.removed?.length) bits.push(`${c.removed.length} removed`);
        parts.push(`packages: ${bits.join(', ')}`);
      }
    }
    return parts.join(' · ');
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
    stopLogs(); // don't keep tailing a stack we're navigating away from
    logsSel = {}; // logs scope is per-stack
    logsMenuOpen = false;
    if (!stack) {
      selectedStack = null;
      originalCompose = '';
      originalEnv = '';
      originalDirector = '';
      originalMakejail = '';
      return;
    }
    // The list endpoint omits compose/env content; fetch the full stack detail
    // so the editor actually shows the compose.yaml + .env.
    try {
      const res = await fetch(`/api/stacks/${stack.name}`);
      if (res.ok) stack = await res.json();
    } catch {}
    selectedStack = stack;
    currentView = 'stacks'; // selecting a stack always means the Stacks section
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
    svcNets = Object.fromEntries(
      ((stack as any)?.services ?? []).map((v: any) => {
        const rows = (v.networks ?? []).map((n: any) => ({ ...n }));
        if (rows.length) return [v.name, rows];
        return [v.name, [{ network: v.hostNetwork ? 'host' : '', ip: '', mac: '' }]];
      }),
    );
    savedSvcNets = JSON.stringify(svcNets);
    // Resources tab is engine-scoped to this stack (see loadNetworks/loadVolumes).
    loadNetworks(stack!.name);
    loadVolumes(stack!.name);
    checkForUpdate(stack!.name); // fire-and-forget; badge fills in when it returns
    // The Logs tab persists across refreshes -- if it's the active view, start
    // streaming now instead of waiting for a tab click.
    if (drawerOpen && drawerTab === 'logs') streamLogs(stack!.name);
  }

  async function loadStacks() {
    const res = await fetch('/api/stacks');
    if (res.ok) {
      stacks = await res.json();
      if (selectedStack) {
        const found = stacks.find((s) => s.name === selectedStack!.name);
        if (found) {
          // Refresh live status/state only -- never overwrite the editor's
          // compose/env with the list's empty values. services goes with them:
          // it is what each service is running right now, not something the
          // editor holds an unsaved version of.
          selectedStack = {
            ...selectedStack,
            status: found.status,
            state: found.state,
            services: found.services ?? selectedStack.services,
          };
        }
      }
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
  type ServiceUpdate = {
    service: string;
    image: string;
    state: string;
    tag?: string;
    running?: string;
    latest?: string;
    newTag?: string;
    fromVersion?: string;
    toVersion?: string;
    detail?: string;
  };
  type UpdateInfo = {
    perService?: boolean;
    rollback?: Record<string, { ref: string; at: string }>;
    restartsWith?: Record<string, string[]>;
    state: string;
    tag?: string;
    latest?: string;
    newTag?: string;
    fromVersion?: string;
    toVersion?: string;
    detail?: string;
    services?: ServiceUpdate[];
  };
  let fleet: Record<string, UpdateInfo> = {};
  let fleetRefreshing = false;
  const behind = (u?: UpdateInfo) => u?.state === 'available' || u?.state === 'upgrade';
  // Count only stacks that still exist: the cache can outlive a deleted one.
  $: fleetBehindCount = stacks.filter((s) => behind(fleet[s.name])).length;
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

  // ---- Resources → Storage: mounts table over the compose (source of truth) ----
  // The backend (/api/compose/mounts) is stateless: it parses / transforms the
  // editor's in-memory compose text and hands it back, so adds/removes just edit
  // selectedStack.compose (marking it dirty) and Save persists + recreates.
  // The add form lives on the service row and owns its own fields. What stays
  // here is the host path the DirPicker fills in (it is a page-level modal) and
  // which service's form is open.
  let addSource = '';
  let addingTo = '';
  let pickingMount = false; // host-path DirPicker open
  // Folder sets (Settings) for "Add folder set…": N folders become N binds under
  // the container path. Placeholders resolve against THIS stack's folder name and
  // the default App data location (an installed stack has no recorded pick).
  let folderSets: { id: string; name: string; folders: string[] }[] = [];
  let appDataDefault = '';
  const slugOfName = (name: string) =>
    name.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
  function expandSetPath(p: string): string {
    const slug = slugOfName(label(selectedStack)) || selectedStack?.name || 'app';
    return p.replace(/\{\{\s*stack\s*\}\}/g, slug).replace(/\{\{\s*(appdata|base)\s*\}\}/g, appDataDefault || '/containers');
  }
  async function loadFolderSets() {
    try {
      const [sr, fr] = await Promise.all([fetch('/api/folder-sets'), fetch('/api/settings/storage')]);
      if (sr.ok) folderSets = (await sr.json()).sets || [];
      if (fr.ok) appDataDefault = (await fr.json()).base || '';
    } catch {
      folderSets = [];
    }
  }
  async function addFolderSet(service: string, setId: string, at: string, readOnly: boolean) {
    const set = folderSets.find((s) => s.id === setId);
    if (!selectedStack || !set) return;
    // No container path typed: mount the set at /<set-name> ("Movies" ->
    // /movies), which is what the set is for; type a path first to override.
    let dest = at.trim().replace(/\/+$/, '');
    if (!dest) {
      dest = '/' + (slugOfName(set.name) || set.id);
      toast(`Mounting "${set.name}" at ${dest} — type a container path first to choose another`, { kind: 'info' });
    }
    const folders = set.folders.map(expandSetPath).filter(Boolean);
    // Sub-folder names: basename, else parent-basename, else a numeric suffix,
    // so two folders ending in "alice" can't claim the same destination.
    const safe = (x: string) => x.toLowerCase().replace(/[^a-z0-9._-]+/g, '-');
    const used = new Set<string>();
    const subs = folders.map((f) => {
      const parts = f.replace(/^[a-z]+:\/\//i, '').replace(/\/+$/, '').split('/').filter(Boolean);
      const base = safe(parts[parts.length - 1] || 'folder');
      const cands = [base, parts.length > 1 ? `${safe(parts[parts.length - 2])}-${base}` : ''].filter(Boolean);
      let pick = cands.find((c) => !used.has(c));
      for (let n = 2; !pick; n++) if (!used.has(`${base}-${n}`)) pick = `${base}-${n}`;
      used.add(pick);
      return pick;
    });
    try {
      for (const [idx, f] of folders.entries()) {
        // One folder mounts at the path; several become sub-folders of it.
        const at = folders.length === 1 ? dest : `${dest}/${subs[idx]}`;
        let kind = 'bind';
        let source = f;
        if (remoteKind(f)) {
          // Remote folder: resolve (and create on first use) its named volume.
          source = await ensureRemoteVolume(f, { stack: selectedStack.name });
          kind = 'volume';
        }
        adopt(await mountsAPI({ op: 'add', kind, service, source, dest: at, readOnly: readOnly }));
      }
      addSource = '';
      addingTo = '';
    } catch (e: any) {
      toast(e.message || 'Add failed', { kind: 'error' });
    }
  }
  // appjail binds host paths via fstab and has no podman named volumes.
  $: canUseVolumes = !selectedStack?.state?.engine || selectedStack.state.engine === 'podman';

  async function mountsAPI(body: Record<string, unknown>) {
    const res = await fetch('/api/compose/mounts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ compose: selectedStack?.compose ?? '', env: selectedStack?.env ?? '', ...body }),
    });
    if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
    return res.json();
  }
  async function addMount(d: { service: string; kind: string; source: string; dest: string; readOnly: boolean }) {
    if (!selectedStack || !d.source.trim() || !d.dest.trim()) return;
    try {
      adopt(await mountsAPI({ op: 'add', ...d }));
      addSource = '';
      addingTo = '';
    } catch (e: any) {
      toast(e.message || 'Add failed', { kind: 'error' });
    }
  }
  // NFS/SMB from the Resources tab: same row format + credential store as the
  // folder-set editor (remote.ts), then mounted as its named volume.
  async function addRemoteMount(d: { service: string; row: string; dest: string; readOnly: boolean }) {
    if (!selectedStack) return;
    if (!d.dest) {
      toast('Enter the container path to mount it at', { kind: 'error' });
      return;
    }
    try {
      const source = await ensureRemoteVolume(d.row, { stack: selectedStack.name });
      adopt(await mountsAPI({ op: 'add', kind: 'volume', service: d.service, source, dest: d.dest, readOnly: d.readOnly }));
      addingTo = '';
      loadVolumes(selectedStack.name);
    } catch (e: any) {
      toast(e.message || 'Add failed', { kind: 'error' });
    }
  }
  // A mount change is an EDIT, not a save: the daemon transforms the compose
  // text it was sent and hands it back for the editor to hold until Save. It
  // returns the per-service view of that new compose alongside it, because the
  // Storage list under each service is derived on that side.
  //
  // Both are adopted together. Re-reading the stack instead returns what is on
  // disk, which threw the edit away -- adding a bind mount looked like it did
  // nothing at all.
  function adopt(res: { compose: string; services?: unknown[] }) {
    if (!selectedStack) return;
    selectedStack = { ...selectedStack, compose: res.compose, services: res.services ?? selectedStack.services };
  }

  // Removing a mount rewrites the compose straight away -- no Save, no revert --
  // so it asks first (in the row itself). The data is untouched, but putting
  // the source path back is the user's problem.
  async function removeMount(d: { service: string; dest: string }) {
    if (!selectedStack) return;
    try {
      adopt(await mountsAPI({ op: 'remove', service: d.service, dest: d.dest }));
    } catch (e: any) {
      toast(e.message || 'Remove failed', { kind: 'error' });
    }
  }

  // Restore view/stack from the URL hash so deep links + refresh work.
  async function restoreFromHash() {
    const [, section, name] = location.hash.replace(/^#/, '').split('/');
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
      settingsTab = name === 'extensions' || name === 'plugins' ? 'extensions' : name === 'catalogs' || name === 'advanced' ? name : 'storage';
      await selectStack(null);
    } else if (section === 'stacks' && name) {
      currentView = 'stacks';
      const dec = decodeURIComponent(name);
      const found = stacks.find((s) => s.name === dec) ?? { name: dec, dir: '', compose: '', env: '' };
      await selectStack(found);
    } else {
      currentView = 'stacks';
      await selectStack(null);
    }
  }

  onMount(async () => {
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
    loadEngines();
    loadNetworks();
    loadVolumes();
    theme = currentTheme();
    unwatchTheme = watchSystem((t) => (theme = t));
    loadFleetUpdates();
    subscribeEvents();
    await restoreFromHash();
    routeReady = true;
    window.addEventListener('hashchange', restoreFromHash);
  });

  onDestroy(() => {
    eventSource?.close();
    unwatchTheme?.();
  });

  // Discard unsaved edits (compose/env, incl. mount-table changes) back to the
  // last saved version. Also clears the network/volume pickers and re-reads the
  function revert() {
    if (!selectedStack) return;
    selectedStack = { ...selectedStack, compose: originalCompose, env: originalEnv, ...(isDirector ? { director: originalDirector, makejail: originalMakejail } : {}) };
    addSource = '';
    addingTo = '';
    toast('Reverted to the last saved version', { kind: 'success' });
  }

  // Set when a save was refused because fjord changed the compose on the
  // server (rollback, version change, unpin) after the editor loaded it.
  let saveConflict = '';
  $: if (saveConflict && selectedStack?.name !== saveConflict) saveConflict = '';
  // Re-read the stack from the server into the editor. Used when fjord
  // rewrote the compose itself, and by the conflict banner's Reload.
  async function reloadStack(name: string) {
    saveConflict = '';
    if (selectedStack?.name === name) await selectStack({ name } as Stack);
  }
  async function save() {
    if (!selectedStack) return;
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
          if (detail.ok) selectedStack = await detail.json();
          volChoice = '';
          volPath = '';
          volRO = false;
        }
        originalCompose = selectedStack!.compose;
        originalEnv = selectedStack!.env;
        originalDirector = selectedStack!.director ?? '';
        originalMakejail = selectedStack!.makejail ?? '';
        toast('Changes saved', { kind: 'success' });
        // A running container keeps its creation-time config (ports, env) --
        // saved changes only apply on a recreate (`up`), NOT on restart. Flag
        // it so the UI can offer "Apply".
        if (statusLabel(selectedStack!.status) === 'running' || statusLabel(selectedStack!.status) === 'partial') {
          needsApply[selectedStack!.name] = true;
        }
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
  async function streamAction(name: string, action: string, msg: string, body?: unknown, onStart?: () => void) {
    stopLogs(); // action output goes to the Output tab, not the Logs stream
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
      if (res.ok) onStart?.();
      if (!res.ok) {
        execStatus[name] = 'error';
        execMessage[name] = `HTTP ${res.status}`;
        logs[name] += `[ERROR]: ${(await res.text()).trim() || res.statusText}\n`;
        return;
      }
      const reader = res.body?.getReader();
      if (reader) {
        const decoder = new TextDecoder('utf-8');
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          logs[name] += decoder.decode(value);
        }
      }
      // A streamed action answers 200 before it knows how it ends, so its
      // outcome is in the output: the daemon marks every failure "[error]".
      // Reading only the status made a failed update look like a success.
      if (/^\[error\]/m.test(logs[name])) {
        execStatus[name] = 'error';
        execMessage[name] = 'Failed — see Output';
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
      execStatus[name] = 'error';
      execMessage[name] = 'Failed';
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
      eventSource.onmessage = (e) => {
        try {
          applyStackEvent(JSON.parse(e.data));
        } catch {}
      };
    } catch {}
  }
  function applyStackEvent(ev: any) {
    if (ev?.type === 'stack-removed') {
      stacks = stacks.filter((s) => s.name !== ev.name);
      if (selectedStack?.name === ev.name) selectStack(null);
      return;
    }
    if (ev?.type === 'stack') {
      if (!stacks.some((s) => s.name === ev.name)) {
        loadStacks(); // a stack this tab doesn't know yet (installed elsewhere)
        return;
      }
      // Status only -- never touch compose/env, so an open editor is safe.
      stacks = stacks.map((s) => (s.name === ev.name ? { ...s, status: ev.status } : s));
      if (selectedStack && selectedStack.name === ev.name) selectedStack = { ...selectedStack, status: ev.status };
    }
  }

  // Live log streaming. `podman compose logs -f` never exits, so it's driven by
  // an AbortController rather than the action flow -- aborting (close drawer /
  // switch stacks / re-click) disconnects, which kills the podman process.
  let logController: AbortController | null = null;
  let logStacking = ''; // which stack we're tailing, '' when idle

  // Logs scope: per-service checkboxes, all checked by default (a missing key
  // counts as checked, so new containers join automatically).
  let logsSel: Record<string, boolean> = {};
  let logsMenuOpen = false;
  $: logsSelected = shellContainers.filter((c) => logsSel[c.name] !== false).map((c) => c.name);
  $: logsAll = shellContainers.length > 0 && logsSelected.length === shellContainers.length;

  async function streamLogs(name: string) {
    logController?.abort();
    logController = new AbortController();
    logStacking = name;
    containerLogs[name] = ''; // backend announces its own "$ command" header
    drawerOpen = true;
    // Compute the scope HERE, not from the $: derived vars -- checkbox
    // handlers call this before Svelte reruns reactives, so those are stale.
    const selected = shellContainers.filter((c) => logsSel[c.name] !== false).map((c) => c.name);
    const all = selected.length === shellContainers.length;
    if (shellContainers.length > 1 && selected.length === 0) {
      containerLogs[name] = '[no services selected]\n';
      logStacking = '';
      return;
    }
    try {
      const scope = all ? '' : selected.map((c) => `&container=${encodeURIComponent(c)}`).join('');
      const res = await fetch(`/api/stacks/${name}/logs?follow=1&tail=200${scope}`, { signal: logController.signal });
      if (!res.ok) {
        containerLogs[name] += `[ERROR]: HTTP ${res.status}\n`;
        return;
      }
      const reader = res.body?.getReader();
      if (reader) {
        const decoder = new TextDecoder('utf-8');
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          containerLogs[name] += decoder.decode(value);
        }
      }
    } catch (err: any) {
      if (err?.name !== 'AbortError') containerLogs[name] += `[ERROR]: ${err?.message || err}\n`;
    } finally {
      if (logStacking === name) logStacking = '';
    }
  }

  function stopLogs() {
    logController?.abort();
    logController = null;
    logStacking = '';
  }

  // Switch the bottom panel's tab. Selecting Logs starts the live stream for
  // this stack; leaving it stops the follow (killed server-side on disconnect).
  function showTab(name: string, tab: 'output' | 'logs' | 'shell') {
    drawerTab = tab;
    drawerOpen = true;
    try {
      localStorage.setItem('fjord.drawerTab', tab);
    } catch {}
    if (tab === 'logs') {
      if (logStacking !== name) streamLogs(name);
    } else {
      stopLogs(); // shell/terminal don't use the logs stream
    }
  }

  // The container a shell exec's into: the stack's first (primary) container.
  // Shell target container: the user's pick if still valid, else the stack's
  // first container. Falls back automatically when switching stacks.
  let shellPick = '';
  $: shellContainers = selectedStack?.status?.containers ?? [];
  // Default shell target: the compose's FIRST service (authors list the
  // primary app first; deps like db/redis/ml follow). The runtime's container
  // list order is arbitrary, so containers[0] would land you in redis.
  // Short service label for pickers: <project>_<service>_1 -> <service>.
  // Mirrors the backend's log-prefix derivation (pkg/engine/podman Logs).
  function serviceLabel(stackName: string, containerName: string): string {
    let label = containerName;
    const prefix = stackName.toLowerCase() + '_';
    if (label.toLowerCase().startsWith(prefix)) label = label.slice(prefix.length);
    const i = label.lastIndexOf('_');
    if (i > 0 && /^\d+$/.test(label.slice(i + 1))) label = label.slice(0, i);
    return label || containerName;
  }

  function primaryContainer(compose: string, containers: { name: string }[]): string {
    const first = (compose.match(/^services:\s*\n\s{2}([\w.-]+):/m) || [])[1];
    if (first) {
      const hit = containers.find((c) => c.name.includes(`_${first}_`) || c.name === first);
      if (hit) return hit.name;
    }
    return containers[0]?.name ?? '';
  }
  $: shellContainer =
    shellPick && shellContainers.some((c) => c.name === shellPick)
      ? shellPick
      : primaryContainer(selectedStack?.compose ?? '', shellContainers);
  // A shell needs a live container/jail: exec into a stopped one fails
  // instantly ("Cannot find the jail") and would just reconnect in a loop.
  $: shellContainerUp = (shellContainers.find((c) => c.name === shellContainer)?.state ?? 'stopped') !== 'stopped';

  // Stacks with saved-but-unapplied config changes (need a recreate via `up`).
  let needsApply: Record<string, boolean> = {};

  const up = async (name: string) => {
    await streamAction(name, 'up', 'Starting…');
    // Only a bring-up that succeeded applied the saved config; after a
    // refused one (port taken) the running containers still have the old.
    if (execStatus[name] !== 'error') needsApply[name] = false;
  };
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
    if (selectedStack?.name === name) checkForUpdate(name); // refresh the badge
    loadFleetUpdates(true); // fleet badges should reflect the applied update
  };

  // Parse the tag out of an image ref, ignoring any @sha256 digest.
  function refTag(image: string): string {
    const b = image.includes('@') ? image.slice(0, image.indexOf('@')) : image;
    const s = b.lastIndexOf('/');
    const c = b.lastIndexOf(':');
    return c > s ? b.slice(c + 1) : 'latest';
  }

  // Apply a version/pin change: rewrite the image ref (tag +/- digest) on the
  // server, refresh the detail, then redeploy only if the version actually
  // changed. Pin/unpin of the same version is the same bytes -- no redeploy.
  async function applyVersion(e: CustomEvent<{ tag: string; pin: boolean }>) {
    const name = changeVersion?.name;
    const curImage = changeVersion?.image ?? '';
    const service = changeVersion?.service;
    // Read before selectStack below clears updateInfo.
    const perService = !!updateInfo?.perService;
    changeVersion = null;
    if (!name) return;
    const tagChanged = e.detail.tag !== refTag(curImage);
    const res = await fetch(`/api/stacks/${name}/set-tag`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ tag: e.detail.tag, pin: e.detail.pin, ...(service ? { service } : {}) }),
    });
    if (!res.ok) {
      const msg = (await res.text()).trim();
      logs[name] = (logs[name] || '') + `[ERROR]: set-tag failed: ${msg}\n`;
      execStatus[name] = 'error';
      drawerOpen = true;
      toast(`Version change failed: ${msg}`, { kind: 'error' });
      return;
    }
    await selectStack({ name } as Stack); // re-fetch: compose now has the new ref
    if (tagChanged) await update(name, service && perService ? [service] : undefined);
    else checkForUpdate(name); // a pin alone runs the same bytes: nothing to recreate
  }

  // Prominent blocking overlay while a delete runs -- tearing containers down
  // can take seconds, and a small toast wasn't clear enough that work was
  // happening. `deleting` is the stack's display label; '' hides the overlay.
  let deleting = '';
  async function deleteStack(name: string) {
    deleting = label(selectedStack) || name;
    try {
      const res = await fetch(`/api/stacks/${name}`, { method: 'DELETE' });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      const gone = deleting;
      deleting = '';
      await selectStack(null);
      location.hash = '#/stacks';
      await loadStacks();
      toast(`Deleted ${gone}`, { kind: 'success' });
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
    accepted?: () => void;
    refused?: (msg: string) => void;
  }>) {
    const { name, appId, engine, manifest, values, paths, appData, tag, network, ip, mac, networkPlan, networks: netList, networkModes } = e.detail;

    // Nothing is torn down or navigated to until the daemon has taken the
    // install. It refuses some of these outright -- an address that is a
    // network address, a name already used -- and those refusals are about
    // what was typed into the wizard, so the wizard has to still be there to
    // show them against. Closing it first threw away every other answer in
    // the form along with the one that was wrong.
    let key = '';
    let handed = false;
    try {
      const res = await fetch('/api/apps/install', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, manifest, values, ...(paths ? { paths } : {}), ...(appData ? { appData } : {}), ...(appId ? { app_id: appId } : {}), ...(engine ? { engine } : {}), ...(tag ? { tag } : {}), ...(network ? { network, ip, ...(mac ? { mac } : {}) } : {}), ...(networkPlan ? { networkPlan } : {}), ...(netList?.length ? { networks: netList } : {}), ...(networkModes ? { networkModes } : {}) }),
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
      execStatus[key] = 'running';
      execMessage[key] = 'Installing...';

      if (!res.ok) {
        // Saved but not started (taken ports, a refused bring-up). The stack
        // is real, so land on it with the reason in its output.
        const msg = (await res.text()).trim();
        execStatus[key] = 'error';
        execMessage[key] = `HTTP ${res.status}`;
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
      const reader = res.body?.getReader();
      if (reader) {
        const decoder = new TextDecoder('utf-8');
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          logs[key] += decoder.decode(value);
        }
      }
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
    } catch (err: any) {
      // Before the hand-off there is no stack and no terminal to write to;
      // the wizard is still up, so the failure belongs in it.
      if (!handed) {
        e.detail.refused?.(err?.message || String(err));
        return;
      }
      execStatus[key] = 'error';
      execMessage[key] = 'Failed';
      logs[key] += `[ERROR]: ${err?.message || err}\n`;
    }
  }
</script>

{#if setupOpen}
  <SetupWizard on:done={setupDone} />
{:else}
<div class="h-screen flex text-fjord-fg-body overflow-hidden">
  <!-- Sidebar: stack list (Dockge-style) -->
  <aside class="shrink-0 bg-fjord-bg flex flex-col" style="width:{sidebarWidth}px">
    <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
    <div
      class="flex items-center gap-2.5 text-xl font-bold tracking-widest text-fjord-fg cursor-pointer px-5 py-4 border-b border-fjord-border"
      on:click={() => {
        selectStack(null);
        currentView = 'stacks';
      }}
    >
      <Icon name="mountain" size={20} class="text-fjord-accent" />
      <span><span class="text-fjord-accent">F</span>JORD</span>
    </div>

    <div class="p-3 flex gap-2 border-b border-fjord-border">
      <input
        bind:value={search}
        placeholder="Search stacks…"
        class="flex-1 min-w-0 bg-fjord-card border border-fjord-border rounded-md px-3 py-1.5 text-sm text-fjord-fg-body focus:outline-none focus:border-fjord-accent"
      />
      <button
        title="New Stack"
        on:click={() =>
          selectStack(newStackDraft())}
        class="shrink-0 w-9 flex items-center justify-center bg-fjord-accent hover:bg-fjord-accent-hover text-white rounded-md"
        ><Icon name="plus" /></button
      >
    </div>

    <!-- group-by selector: how the stack list is bucketed -->
    <div class="relative px-3 pb-1 pt-0.5">
      <button
        on:click={() => (groupMenuOpen = !groupMenuOpen)}
        class="flex items-center gap-1.5 text-[11px] font-medium text-fjord-fg-dim hover:text-fjord-fg-secondary transition-colors"
      >
        <span class="text-[10px] uppercase tracking-wide text-fjord-fg-faint">Group by</span>
        <span class="text-fjord-fg-secondary">{GROUP_MODES.find((g) => g.id === groupMode)?.label}</span>
        <Icon name={groupMenuOpen ? 'chevron-up' : 'chevron-down'} size={11} />
      </button>
      {#if groupMenuOpen}
        <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
        <div class="fixed inset-0 z-20" on:click={() => (groupMenuOpen = false)}></div>
        <div class="absolute z-30 left-3 top-full mt-1 bg-fjord-card border border-fjord-border rounded-lg shadow-2xl p-1 min-w-36">
          {#each GROUP_MODES as gm}
            <button
              on:click={() => { setGroupMode(gm.id); groupMenuOpen = false; }}
              class="w-full flex items-center justify-between gap-3 px-3 py-1.5 rounded-md text-sm transition-colors {groupMode ===
              gm.id
                ? 'bg-fjord-border text-fjord-fg'
                : 'text-fjord-fg-secondary hover:bg-fjord-border/50'}"
            >
              {gm.label}
              {#if groupMode === gm.id}<Icon name="check" size={13} class="text-fjord-accent" />{/if}
            </button>
          {/each}
        </div>
      {/if}
    </div>

    <div class="flex-1 overflow-y-auto py-2">
      {#each groupedStacks as [group, items] (group)}
        {#if group || hasGroups}
          <!-- svelte-ignore a11y-no-static-element-interactions -->
          <button
            on:click={() => group && toggleGroup(group)}
            on:dragover={(e) => onGroupDragOver(e, group)}
            on:drop={() => commitReorder(null, false, group)}
            class="w-full flex items-center gap-1.5 px-3 pt-3 pb-1 text-[11px] font-semibold uppercase tracking-wide rounded {group
              ? 'text-fjord-fg-dim hover:text-fjord-fg-secondary'
              : 'text-fjord-fg-faint'} {dropGroup === group ? '!bg-fjord-accent/20 ring-1 ring-fjord-accent/50 !text-fjord-fg-body' : ''}"
          >
            {#if group}
              <Icon name={collapsedGroups[group] ? 'chevron-right' : 'chevron-down'} size={11} />
            {/if}
            {#if groupMode === 'engine' && group}
              <EngineMark engine={group} size={13} strokeWidth={2.25} />
            {/if}
            <span class="truncate">{group || 'Ungrouped'}</span>
            <span class="text-fjord-fg-faint normal-case">{items.length}</span>
          </button>
        {/if}
        {#if !collapsedGroups[group]}
          <div class={group ? 'ml-3.5 pl-1 border-l border-fjord-border' : ''}>
            {#if items.length === 0 && groupMode === 'groups'}
              <!-- svelte-ignore a11y-no-static-element-interactions -->
              <div
                on:dragover={(e) => onGroupDragOver(e, group)}
                on:drop={() => commitReorder(null, false, group)}
                class="mx-3 my-1 px-3 py-3 text-[11px] text-center rounded border border-dashed transition-colors {dropGroup ===
                group
                  ? 'border-fjord-accent text-fjord-fg-body bg-fjord-accent/10'
                  : 'border-fjord-border text-fjord-fg-faint'}"
              >
                Drag here to remove from group
              </div>
            {/if}
            {#each items as stack (stack.name)}
              <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
              <div
                draggable={draggableStacks}
                on:dragstart={() => (dragName = stack.name)}
                on:dragend={clearDrag}
                on:dragover={(e) => onRowDragOver(e, stack.name)}
                on:drop={() => commitReorder(stack.name, dropAfter, groupOf(stack.name))}
                on:click={() => selectStack(stack)}
                class="flex items-center gap-3 py-2 pl-4 pr-4 {draggableStacks
                  ? 'cursor-grab active:cursor-grabbing'
                  : 'cursor-pointer'} text-sm transition-colors border-y-2 border-transparent {selectedStack?.name ===
                  stack.name && currentView === 'stacks'
                  ? 'bg-fjord-border text-fjord-fg'
                  : 'text-fjord-fg-secondary hover:bg-fjord-border/40'} {dragName === stack.name
                  ? 'opacity-40'
                  : ''} {dropTarget === stack.name && !dropAfter
                  ? '!border-t-fjord-accent'
                  : ''} {dropTarget === stack.name && dropAfter ? '!border-b-fjord-accent' : ''}"
              >
                <span class="w-2 h-2 rounded-full shrink-0 {dot(stack.status)}"></span>
                {#if iconFor($appIcons, stack)}
                  <img src={iconFor($appIcons, stack)} alt="" class="w-4 h-4 rounded shrink-0 object-contain" />
                {:else}
                  <span class="w-4 h-4 rounded shrink-0 flex items-center justify-center text-[9px] font-bold text-fjord-fg" style="background:{tile(label(stack))}">{initial(label(stack))}</span>
                {/if}
                <span class="truncate">{label(stack)}</span>
                {#if behind(fleet[stack.name])}
                  <span
                    class="ml-auto shrink-0 text-fjord-warning"
                    title={fleet[stack.name].state === 'upgrade'
                      ? `${fleet[stack.name].fromVersion ? `v${fleet[stack.name].fromVersion} → ` : ''}v${fleet[stack.name].toVersion} available`
                      : 'Update available'}><Icon name="arrow-up" size={12} /></span
                  >
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      {/each}
      {#if fleetBehindCount > 0}
        <div class="px-4 pt-3 flex items-center gap-1.5 text-[11px] text-fjord-warning/80">
          <Icon name="arrow-up" size={11} />
          {fleetBehindCount} update{fleetBehindCount === 1 ? '' : 's'} available
        </div>
      {/if}
      {#if filtered.length === 0}
        <p class="px-4 py-2 text-xs text-fjord-fg-faint italic">No stacks{search ? ' match' : ' yet'}.</p>
      {/if}
    </div>

    <nav class="border-t border-fjord-border p-2 flex flex-col gap-1">
      <button
        on:click={() => {
          selectStack(null);
          currentView = 'store';
        }}
        class="flex items-center gap-2.5 text-left px-3 py-2 rounded-md text-sm font-medium transition-colors {currentView ===
        'store'
          ? 'bg-fjord-border text-fjord-fg'
          : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="store" size={15} /> App Store</button
      >
      <button
        on:click={() => {
          selectStack(null);
          currentView = 'volumes';
        }}
        class="flex items-center gap-2.5 text-left px-3 py-2 rounded-md text-sm font-medium transition-colors {currentView ===
        'volumes'
          ? 'bg-fjord-border text-fjord-fg'
          : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="drive" size={15} /> Volumes</button
      >
      <button
        on:click={() => {
          selectStack(null);
          currentView = 'networks';
        }}
        class="flex items-center gap-2.5 text-left px-3 py-2 rounded-md text-sm font-medium transition-colors {currentView ===
        'networks'
          ? 'bg-fjord-border text-fjord-fg'
          : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="globe" size={15} /> Networks</button
      >
      <button
        on:click={() => {
          selectStack(null);
          currentView = 'system';
        }}
        class="flex items-center gap-2.5 text-left px-3 py-2 rounded-md text-sm font-medium transition-colors {currentView ===
        'system'
          ? 'bg-fjord-border text-fjord-fg'
          : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="activity" size={15} /> System</button
      >
      <button
        on:click={() => {
          selectStack(null);
          currentView = 'settings';
        }}
        class="flex items-center gap-2.5 text-left px-3 py-2 rounded-md text-sm font-medium transition-colors {currentView ===
        'settings'
          ? 'bg-fjord-border text-fjord-fg'
          : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="settings" size={15} /> Settings</button
      >
    </nav>
  </aside>

  <!-- sidebar↔main sash: visible divider, widens + highlights on hover -->
  <div
    class="group relative w-px shrink-0 bg-fjord-border cursor-col-resize"
    use:resizer={{ axis: 'x', onMove: (d) => setSidebar(sidebarWidth + d) }}
    on:dblclick={() => setSidebar(288)}
    title="Drag to resize · double-click to reset"
  >
    <div class="absolute inset-y-0 -left-1 -right-1 group-hover:bg-fjord-accent/60 transition-colors"></div>
  </div>

  <main class="relative flex-1 min-w-0 bg-fjord-bg/95 flex flex-col h-screen overflow-hidden pr-8">
    <!-- Theme toggle: floats in main's right gutter so it stays top-right in
         every view without colliding with each view's own action row. -->
    <button
      on:click={toggleTheme}
      title={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
      aria-label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
      class="absolute top-4 right-4 z-30 w-8 h-8 flex items-center justify-center rounded-lg text-fjord-fg-muted hover:text-fjord-fg hover:bg-fjord-border transition-colors"
      ><Icon name={theme === 'dark' ? 'sun' : 'moon'} size={16} /></button
    >
    {#if selectedStack}
      <div class="flex flex-col h-full overflow-hidden p-6">
        <!-- Header -->
        <header class="flex justify-between items-center mb-4 shrink-0">
          <div class="flex items-center gap-3 min-w-0">
            {#if editingName}
              <!-- svelte-ignore a11y-autofocus -->
              <input
                autofocus
                bind:value={nameEdit}
                on:keydown={(e) => {
                  if (e.key === 'Enter' && nameEditValid) commitRename();
                  else if (e.key === 'Escape') editingName = false;
                }}
                on:blur={() => (editingName = false)}
                class="text-2xl font-semibold text-fjord-fg bg-fjord-inset border rounded-md px-2 py-0.5 min-w-0 focus:outline-none {nameEdit && !nameEditValid ? 'border-fjord-danger/60' : 'border-fjord-accent'}"
              />
            {:else}
              <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
              <h2
                on:click={() => { editingName = true; nameEdit = label(selectedStack); }}
                title="Click to rename (id: {selectedStack.name})"
                class="text-2xl font-semibold text-fjord-fg truncate cursor-text hover:bg-fjord-border/40 rounded px-1 -mx-1 transition-colors"
              >{label(selectedStack)}</h2>
            {/if}
            <span
              title={(selectedStack.status?.containers || []).filter((c) => c.detail).map((c) => `${c.name}: ${c.detail}`).join('\n') || ''}
              class="shrink-0 px-2.5 py-0.5 border rounded-full text-[11px] font-semibold capitalize {statusStyle(
                selectedStack.status,
              )}">{statusLabel(selectedStack.status)}</span
            >
            {#each (selectedStack.status?.containers || []).filter((c) => c.detail) as c}
              <span class="shrink-0 text-xs text-fjord-warning truncate" title={c.name}>{c.detail}</span>
            {/each}
            {#if selectedStack.state?.engine}
              <span class="shrink-0 flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium bg-fjord-bg border border-fjord-border text-fjord-fg-muted" title="Runtime engine"
                ><EngineMark engine={selectedStack.state.engine} size={12} strokeWidth={2.25} />{selectedStack.state.engine}</span
              >
            {/if}
            {#if noAddress}
              <span
                class="shrink-0 flex items-center gap-1 text-sm text-fjord-fg-muted"
                title="A stack on its own IP publishes nothing on the host, so there is no link to open until the network gives it an address."
                ><Icon name="external" size={13} /> {noAddress}</span
              >
            {/if}
            {#if openUrl}
              <a
                href={openUrl}
                target="_blank"
                rel="noreferrer"
                title="Open {openUrl}"
                class="shrink-0 flex items-center gap-1 text-sm font-medium text-fjord-accent hover:text-fjord-accent-hover hover:underline {statusLabel(
                  selectedStack.status,
                ) === 'running'
                  ? ''
                  : 'opacity-40 pointer-events-none'}"><Icon name="external" size={13} /> Open</a
              >
            {/if}
            <input
              list="fjord-groups"
              value={selectedStack.state?.group ?? ''}
              on:change={(e) => setStackGroup(selectedStack!.name, e.currentTarget.value)}
              placeholder="+ group"
              title="Assign this stack to a sidebar group"
              class="shrink-0 w-28 bg-fjord-card border border-fjord-border rounded-full px-3 py-0.5 text-[11px] text-fjord-fg-muted placeholder:text-fjord-fg-faint focus:outline-none focus:border-fjord-accent"
            />
            <datalist id="fjord-groups">
              {#each existingGroups as g}<option value={g}></option>{/each}
            </datalist>
            {#if isDirty}
              <span class="shrink-0 text-fjord-warning text-xs bg-fjord-warning/10 border border-fjord-warning/20 px-2 py-0.5 rounded"
                >Unsaved</span
              >
            {/if}
          </div>
          <div class="flex items-center gap-3 shrink-0">
            {#if isDraft && engines.length > 1}
              <!-- Runtime for a new stack: podman = compose, appjail = native director + Makejail. -->
              <label class="flex items-center gap-2 text-xs text-fjord-fg-muted">
                Engine
                <select
                  value={selectedStack.engine ?? defaultEngine}
                  on:change={(e) => setDraftEngine(e.currentTarget.value)}
                  class="bg-fjord-inset border border-fjord-border rounded-lg px-2 py-1 text-sm text-fjord-fg-body focus:border-fjord-accent outline-none"
                >
                  {#each engines.filter((e) => e.available && e.enabled) as e}
                    <option value={e.name}>{e.name}</option>
                  {/each}
                </select>
              </label>
            {/if}
            {#if isDirty}
              <button
                on:click={revert}
                title="Discard unsaved changes and restore the last saved version"
                class="text-fjord-fg-muted hover:text-fjord-fg text-sm font-medium py-1.5 px-3 rounded-lg border border-fjord-border hover:border-fjord-accent/40 transition-colors"
                >Revert</button
              >
            {/if}
            <button
              on:click={save}
              disabled={saving}
              class="bg-fjord-accent hover:bg-fjord-accent-hover text-white text-sm font-medium py-1.5 px-4 rounded-lg disabled:opacity-50"
              >{saving ? (isDraft ? 'Creating…' : 'Saving…') : isDraft ? 'Create' : 'Save'}</button
            >
          </div>
        </header>

        <!-- Inline confirmation for Stop / Delete: visible, but nothing is blocked -->
        {#if pendingAction && pendingAction.stack === selectedStack.name}
          <div
            role="alertdialog"
            aria-live="polite"
            class="flex items-center gap-3 mb-4 shrink-0 px-4 py-2.5 rounded-lg bg-fjord-danger/10 border border-fjord-danger/30 text-sm text-fjord-fg-body"
          >
            <span class="flex-1">
              {#if pendingAction.kind === 'delete'}
                <b>Delete {label(selectedStack)}?</b> Stops the stack and removes it from fjord. Bind-mounted data stays on disk.
              {:else if pendingAction.kind === 'recreate'}
                <b>Pull &amp; recreate {label(selectedStack)}?</b> Pulls every image and recreates every container, including
                ones already up to date.
              {:else}
                <b>Stop {label(selectedStack)}?</b> Stops and removes the containers. Your data is preserved.
              {/if}
            </span>
            <button
              on:click={() => (pendingAction = null)}
              class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium text-fjord-fg-muted hover:text-fjord-fg transition-colors"
              >Cancel</button
            >
            <button
              bind:this={pendingConfirmBtn}
              on:click={confirmPending}
              on:keydown={(e) => e.key === 'Escape' && (pendingAction = null)}
              disabled={execStatus[selectedStack.name] === 'running'}
              class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-danger text-white hover:bg-fjord-danger-hover transition-colors disabled:opacity-40"
              >{pendingAction.kind === 'delete' ? 'Delete' : pendingAction.kind === 'recreate' ? 'Recreate' : 'Stop'}</button
            >
          </div>
        {/if}

        <!-- Update panel: what an update would change, per service, before it runs -->
        {#if updatePanel === selectedStack.name}
          <div class="mb-4 shrink-0 px-4 py-3 rounded-lg bg-fjord-card border border-fjord-border text-sm text-fjord-fg-body">
            {#if checkingUpdate || !updateInfo}
              <div class="flex items-center gap-2 text-fjord-fg-dim"><Spinner size={14} /> Checking the registry…</div>
            {:else}
              <div class="overflow-x-auto">
                <table class="w-full text-xs">
                  <thead class="text-fjord-fg-dim">
                    <tr>
                      {#if offered.length > 1}<th class="w-6 pb-1.5"></th>{/if}
                      <th class="text-left font-medium pb-1.5">Service</th>
                      <th class="text-left font-medium pb-1.5">Tag</th>
                      <th class="text-left font-medium pb-1.5">State</th>
                      <th class="text-left font-medium pb-1.5">Change</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each updateInfo.services ?? [] as s}
                      <tr class="border-t border-fjord-border/60 align-top">
                        {#if offered.length > 1}
                          <td class="py-1.5 pr-1">
                            {#if offered.includes(s)}
                              <input
                                type="checkbox"
                                checked={!unpicked[s.service]}
                                on:change={(e) => (unpicked = { ...unpicked, [s.service]: !e.currentTarget.checked })}
                                aria-label="Update {s.service}"
                                class="accent-fjord-accent"
                              />
                            {/if}
                          </td>
                        {/if}
                        <td class="py-1.5 pr-3 font-medium text-fjord-fg">{s.service}</td>
                        <td class="py-1.5 pr-3 font-mono text-fjord-fg-secondary">{s.tag ?? ''}</td>
                        <td class="py-1.5 pr-3 whitespace-nowrap">
                          {#if s.state === 'available'}<span class="text-fjord-warning">update</span>
                          {:else if s.state === 'upgrade'}<span class="text-fjord-warning">new version</span>
                          {:else if s.state === 'current'}<span class="text-fjord-success">up to date</span>
                          {:else if s.state === 'pinned'}<span class="text-fjord-accent">pinned</span>
                          {:else}<span class="text-fjord-fg-dim">unknown</span>{/if}
                        </td>
                        <td class="py-1.5 font-mono text-fjord-fg-secondary">
                          {#if s.state === 'available'}
                            {shortDigest(s.running) || 'local'} → {shortDigest(s.latest)}
                          {:else if s.state === 'upgrade'}
                            v{s.fromVersion} → v{s.toVersion}
                            {#if multiImage && !updateInfo.perService}<span class="font-sans text-fjord-fg-dim">(set in the compose)</span>{/if}
                          {:else if s.state === 'unknown'}
                            <span class="font-sans text-fjord-fg-dim">{s.detail ?? ''}</span>
                          {/if}
                        </td>
                      </tr>
                      {#if s.state === 'available' || s.state === 'upgrade'}
                        {@const k = changesKey(selectedStack.name, s)}
                        {@const c = changes[k]}
                        {#if c === 'loading'}
                          <tr>{#if offered.length > 1}<td></td>{/if}<td></td><td colspan="3" class="pb-1.5 text-fjord-fg-dim">Reading what changed…</td></tr>
                        {:else if c && c !== 'none' && changesLine(c)}
                          {@const n = (c.changed?.length ?? 0) + (c.added?.length ?? 0) + (c.removed?.length ?? 0)}
                          <tr>
                            {#if offered.length > 1}<td></td>{/if}
                            <td></td>
                            <td colspan="3" class="pb-1.5 text-fjord-fg-secondary">
                              {changesLine(c)}
                              {#if c.packages && n}
                                <button
                                  on:click={() => (changesOpen = { ...changesOpen, [k]: !changesOpen[k] })}
                                  class="ml-1 text-fjord-accent hover:underline">{changesOpen[k] ? 'hide' : 'show'}</button
                                >
                              {/if}
                              {#if changesOpen[k]}
                                <div class="mt-1 font-mono text-[11px] leading-5 max-h-48 overflow-y-auto">
                                  {#each c.changed ?? [] as p}<div>{p.name} <span class="text-fjord-fg-dim">{p.from} →</span> {p.to}</div>{/each}
                                  {#each c.added ?? [] as p}<div class="text-fjord-success">+ {p.name} {p.version}</div>{/each}
                                  {#each c.removed ?? [] as p}<div class="text-fjord-danger">− {p.name} {p.version}</div>{/each}
                                </div>
                              {/if}
                            </td>
                          </tr>
                        {/if}
                      {/if}
                    {/each}
                  </tbody>
                </table>
              </div>
              <div class="flex items-center gap-3 mt-3">
                <span class="flex-1 text-xs text-fjord-fg-dim">
                  {#if versionStep}
                    Switches to {versionStep.tag} and recreates the container.
                  {:else if updateInfo.perService && offered.length && !picked.length}
                    Tick the services to update.
                  {:else if updateInfo.perService && picked.length}
                    {@const also = [...new Set(picked.flatMap((s) => updateInfo?.restartsWith?.[s.service] ?? []))].filter(
                      (n) => !picked.some((s) => s.service === n),
                    )}
                    {@const bumps = picked.filter((s) => s.state === 'upgrade')}
                    {#if bumps.length}Moves {bumps.map((s) => `${s.service} to v${s.toVersion}`).join(', ')} in the compose, then pulls{:else}Pulls{/if}
                    and recreates only {picked.map((s) => s.service).join(', ')}{#if also.length}; {also.join(', ')}
                      {also.length === 1 ? 'restarts' : 'restart'} with it (depends on it){/if}. The rest keep running.
                  {:else if updatable.length}
                    {updatable.length} of {updateInfo.services?.length ?? 0}
                    {updateInfo.services?.length === 1 ? 'service has' : 'services have'} an update. This engine updates the
                    whole stack: every image is pulled and every container recreated.
                  {:else}
                    Nothing here is pulled by Update.
                  {/if}
                </span>
                <button
                  on:click={() => (updatePanel = '')}
                  class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium text-fjord-fg-muted hover:text-fjord-fg transition-colors"
                  >Cancel</button
                >
                <button
                  on:click={() =>
                    versionStep
                      ? upgradeTo(selectedStack!.name, versionStep.tag)
                      : updateInfo?.perService
                        ? updatePicked(selectedStack!.name, picked)
                        : update(selectedStack!.name)}
                  disabled={(updateInfo.perService ? !picked.length : !updatable.length && !versionStep) ||
                    execStatus[selectedStack.name] === 'running'}
                  class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-accent text-white hover:bg-fjord-accent-hover transition-colors disabled:opacity-40"
                  >{versionStep
                    ? `Update to v${versionStep.to}`
                    : updateInfo.perService && picked.length
                      ? `Update ${picked.length} service${picked.length === 1 ? '' : 's'}`
                      : 'Update'}</button
                >
              </div>
            {/if}
          </div>
        {/if}

        <!-- A save refused because fjord rewrote the compose after it was opened -->
        {#if saveConflict === selectedStack.name}
          <div
            class="flex items-center gap-3 mb-4 shrink-0 px-4 py-2.5 rounded-lg bg-fjord-danger/10 border border-fjord-danger/30 text-sm text-fjord-fg-body"
          >
            <span class="flex-1"
              ><b>Not saved.</b> The compose changed on the server after you opened it — a rollback, version change or
              unpin, or another tab. Reload to see it; copy your edits first, reloading replaces them.</span
            >
            <button
              on:click={() => (saveConflict = '')}
              class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium text-fjord-fg-muted hover:text-fjord-fg transition-colors"
              >Dismiss</button
            >
            <button
              on:click={() => reloadStack(selectedStack!.name)}
              class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-danger text-white hover:bg-fjord-danger-hover transition-colors"
              >Reload</button
            >
          </div>
        {/if}

        <!-- HIG banner: persistent saved-but-unapplied state, with its action.
             Hidden while an action runs: that action applies the config, and a
             greyed-out Apply next to "Updating…" only read as broken. -->
        {#if needsApply[selectedStack.name] && execStatus[selectedStack.name] !== 'running'}
          <div
            class="flex items-center gap-3 mb-4 shrink-0 px-4 py-2.5 rounded-lg bg-fjord-warning/10 border border-fjord-warning/25 text-sm text-fjord-fg-body"
          >
            <span class="flex-1"
              >The running containers still use the old configuration — apply the saved changes to recreate them.</span
            >
            <button
              on:click={() => (needsApply[selectedStack!.name] = false)}
              title="Keep the running containers as-is; the saved config applies next time you Start/recreate"
              class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium text-fjord-fg-muted hover:text-fjord-fg transition-colors"
              >Not now</button
            >
            <button
              on:click={() => up(selectedStack!.name)}
              disabled={execStatus[selectedStack.name] === 'running'}
              class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-warning/15 text-fjord-warning border border-fjord-warning/40 hover:bg-fjord-warning/25 transition-colors disabled:opacity-40"
              >Apply Changes</button
            >
          </div>
        {/if}

        <!-- Action bar: primary actions visible, the rest in the overflow menu.
             Hidden until the stack exists on the server -- a draft can only be
             created (the Save button reads "Create"), not started/updated. -->
        {#if !isDraft}
        <div class="flex flex-wrap items-center gap-2 mb-4 shrink-0">
          <button
            on:click={() => up(selectedStack!.name)}
            disabled={execStatus[selectedStack.name] === 'running'}
            class="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium bg-fjord-border hover:bg-fjord-success/80 hover:text-white transition-colors disabled:opacity-40"
            ><Icon name="play" size={14} /> Start</button
          >
          <button
            on:click={() => (pendingAction = { kind: 'stop', stack: selectedStack!.name })}
            disabled={execStatus[selectedStack.name] === 'running'}
            class="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium bg-fjord-border hover:bg-fjord-danger hover:text-white transition-colors disabled:opacity-40"
            ><Icon name="stop" size={14} /> Stop…</button
          >
          <button
            on:click={() => (updatePanel === selectedStack!.name ? (updatePanel = '') : openUpdatePanel(selectedStack!.name))}
            disabled={execStatus[selectedStack.name] === 'running' || !!updateButtonReason}
            title={updateButtonReason ||
              (updateInfo?.state === 'available'
                ? `${updatable.map((s) => s.service).join(', ')} ${updatable.length === 1 ? 'has' : 'have'} an update`
                : 'Check for updates')}
            class="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium transition-colors disabled:opacity-40 {updateInfo?.state ===
            'available'
              ? 'bg-fjord-warning/15 text-fjord-warning border border-fjord-warning/40 hover:bg-fjord-warning/25'
              : 'bg-fjord-border hover:bg-fjord-accent hover:text-white'}"
            ><Icon name="update" size={14} /> Update</button
          >
          <!-- update-availability badge -->
          {#if checkingUpdate}
            <span class="text-xs text-fjord-fg-dim self-center">Checking…</span>
          {:else if updateInfo?.state === 'available'}
            <span class="text-xs text-fjord-warning self-center" title="Registry has a newer image than what's pulled"
              >Update available</span
            >
          {:else if updateInfo?.state === 'upgrade'}
            <button
              on:click={() =>
                (changeVersion = {
                  name: selectedStack!.name,
                  image: stackImage(selectedStack!.compose),
                  suggest: updateInfo!.newTag,
                })}
              class="text-xs text-fjord-warning self-center underline decoration-dotted hover:text-fjord-warning/80"
              title="A newer version is published — switch to it via Change Version"
              >{updateInfo!.fromVersion ? `v${updateInfo!.fromVersion} → ` : ''}v{updateInfo!.toVersion} available</button
            >
          {:else if updateInfo?.state === 'current'}
            <!-- The button is off when there is nothing to pull, so asking
                 again lives here, on the claim it would refresh. -->
            <button
              on:click={() => checkForUpdate(selectedStack!.name)}
              title="Check again"
              class="flex items-center gap-1 text-xs text-fjord-success self-center hover:underline decoration-dotted"
              ><Icon name="check" size={12} /> Up to date</button
            >
          {:else if updateInfo?.state === 'pinned'}
            <span class="flex items-center gap-1 text-xs text-fjord-accent self-center"
              ><Icon name="pin" size={12} /> Pinned</span
            >
          {/if}
          <div class="flex-1"></div>
          <div class="relative">
            <button
              on:click={() => (actionsMenuOpen = !actionsMenuOpen)}
              disabled={execStatus[selectedStack.name] === 'running'}
              title="More Actions"
              class="flex items-center px-2.5 py-2 rounded-lg text-sm font-medium bg-fjord-border hover:bg-fjord-border/70 hover:text-fjord-fg transition-colors disabled:opacity-40"
              ><Icon name="menu" size={16} /></button
            >
            {#if actionsMenuOpen}
              <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
              <div class="fixed inset-0 z-20" on:click={() => (actionsMenuOpen = false)}></div>
              <div
                class="absolute z-30 top-full right-0 mt-1 bg-fjord-card border border-fjord-border rounded-xl shadow-2xl p-1.5 min-w-52"
              >
                <button
                  on:click={() => {
                    actionsMenuOpen = false;
                    restart(selectedStack!.name);
                  }}
                  class="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-fjord-fg-body hover:bg-fjord-border transition-colors"
                  ><Icon name="restart" size={14} class="text-fjord-fg-muted" /> Restart</button
                >
                <button
                  on:click={() => {
                    actionsMenuOpen = false;
                    pendingAction = { kind: 'recreate', stack: selectedStack!.name };
                  }}
                  title="Pull every image and recreate every container, even ones already up to date"
                  class="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-fjord-fg-body hover:bg-fjord-border transition-colors"
                  ><Icon name="update" size={14} class="text-fjord-fg-muted" /> Pull &amp; recreate all…</button
                >
                {#if !multiImage}
                  <button
                    on:click={() => {
                      actionsMenuOpen = false;
                      changeVersion = {
                        name: selectedStack!.name,
                        image: stackImage(selectedStack!.compose),
                        suggest: updateInfo?.state === 'upgrade' ? updateInfo.newTag : undefined,
                      };
                    }}
                    disabled={!stackImage(selectedStack.compose)}
                    class="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-fjord-fg-body hover:bg-fjord-border transition-colors disabled:opacity-40"
                    ><Icon name="swap" size={14} class="text-fjord-fg-muted" /> Change Version…{#if /@sha256:/.test(selectedStack.compose)}<Icon
                      name="pin"
                      size={12}
                      class="text-fjord-accent ml-auto"
                    />{/if}</button
                  >
                {/if}
                <div class="h-px bg-fjord-border my-1.5 mx-1"></div>
                <button
                  on:click={() => {
                    actionsMenuOpen = false;
                    pendingAction = { kind: 'delete', stack: selectedStack!.name };
                  }}
                  class="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-fjord-danger hover:bg-fjord-danger/15 transition-colors"
                  ><Icon name="trash" size={14} /> Delete…</button
                >
              </div>
            {/if}
          </div>
        </div>
        {/if}

        <!-- Editor + tabs -->
        <div class="flex-1 flex flex-col bg-fjord-card border border-fjord-border rounded-xl shadow-xl overflow-hidden min-h-0">
          <div class="bg-fjord-border/50 flex border-b border-fjord-border text-xs font-semibold text-fjord-fg-secondary">
            <button
              on:click={() => (activeTab = 'net')}
              class="px-4 py-2.5 border-r border-fjord-border {activeTab === 'net'
                ? 'bg-fjord-card text-fjord-fg border-b-2 border-b-fjord-accent'
                : 'text-fjord-fg-muted hover:text-fjord-fg-body'}">Services</button
            >
            <button
              on:click={() => (activeTab = 'env')}
              class="px-4 py-2.5 border-r border-fjord-border flex items-center gap-2 {activeTab === 'env'
                ? 'bg-fjord-card text-fjord-fg border-b-2 border-b-fjord-accent'
                : 'text-fjord-fg-muted hover:text-fjord-fg-body'}"
            >
              .env{#if selectedStack.env !== originalEnv}<span class="text-fjord-warning font-bold">*</span>{/if}
            </button>
            <button
              on:click={() => (activeTab = 'compose')}
              class="px-4 py-2.5 border-r border-fjord-border flex items-center gap-2 {activeTab === 'compose'
                ? 'bg-fjord-card text-fjord-fg border-b-2 border-b-fjord-accent'
                : 'text-fjord-fg-muted hover:text-fjord-fg-body'}"
            >
              {#if isDirector}appjail-director.yml{#if (selectedStack.director ?? '') !== originalDirector}<span class="text-fjord-warning font-bold">*</span>{/if}{:else}compose.yaml{#if selectedStack.compose !== originalCompose}<span class="text-fjord-warning font-bold">*</span>{/if}{/if}
            </button>
            {#if isDirector}
              <button
                on:click={() => (activeTab = 'makejail')}
                class="px-4 py-2.5 border-r border-fjord-border flex items-center gap-2 {activeTab === 'makejail'
                  ? 'bg-fjord-card text-fjord-fg border-b-2 border-b-fjord-accent'
                  : 'text-fjord-fg-muted hover:text-fjord-fg-body'}"
              >
                Makejail{#if (selectedStack.makejail ?? '') !== originalMakejail}<span class="text-fjord-warning font-bold">*</span>{/if}
              </button>
            {/if}
          </div>

          <div class="flex-1 relative min-h-0">
            {#if activeTab === 'compose' && isDirector}
              <Editor
                bind:content={selectedStack.director}
                language="yaml"
                on:change={(e) => (selectedStack!.director = e.detail)}
                on:save={save}
              />
            {:else if activeTab === 'compose'}
              <Editor
                bind:content={selectedStack.compose}
                language="yaml"
                on:change={(e) => (selectedStack!.compose = e.detail)}
                on:save={save}
              />
            {:else if activeTab === 'makejail'}
              <Editor
                bind:content={selectedStack.makejail}
                language="env"
                on:change={(e) => (selectedStack!.makejail = e.detail)}
                on:save={save}
              />
            {:else if activeTab === 'env'}
              <Editor
                bind:content={selectedStack.env}
                language="env"
                on:change={(e) => (selectedStack!.env = e.detail)}
                on:save={save}
              />
            {:else}
              <div class="p-6 overflow-y-auto h-full">
                {#if !selectedStack.compose && !selectedStack.director}
                  <!-- The optimistic placeholder the install flow puts up: no
                       compose yet, so every answer here would be a guess. -->
                  <div class="flex items-center gap-2 text-sm text-fjord-fg-muted">
                    <Spinner size={14} />
                    Still installing — this fills in as soon as the stack is written.
                  </div>
                {:else}
                <h3 class="text-lg font-bold text-fjord-fg mb-1">Services</h3>
                <p class="text-xs text-fjord-fg-dim mb-3">
                  What each service holds, and where each one is on the network.
                  Changes apply to the stack on <b>Save</b>.
                </p>
                <ServiceResources
                  services={selectedStack.services ?? []}
                  updates={svcUpdates}
                  rollbacks={updateInfo?.rollback ?? {}}
                  pinned={svcPinned}
                  on:openUpdate={() => openUpdatePanel(selectedStack!.name)}
                  on:rollback={(e) => rollback(selectedStack!.name, e.detail)}
                  on:unpin={(e) => unpin(selectedStack!.name, e.detail)}
                  versionable={!selectedStack.director}
                  on:version={(e) =>
                    (changeVersion = {
                      name: selectedStack!.name,
                      service: e.detail.service,
                      image: e.detail.image,
                      suggest: svcUpdates[e.detail.service]?.newTag,
                    })}
                  {networks}
                  {unsupportedModes}
                  stackName={selectedStack.name}
                  bind:edits={svcNets}
                  {canUseVolumes}
                  {namedVolumes}
                  {folderSets}
                  bind:addSource
                  {addingTo}
                  on:change={() => (svcNets = svcNets)}
                  on:unmount={(e) => removeMount(e.detail)}
                  on:add={(e) => addMount(e.detail)}
                  on:remote={(e) => addRemoteMount(e.detail)}
                  on:folderset={(e) => addFolderSet(e.detail.service, e.detail.id, e.detail.dest, e.detail.readOnly)}
                  on:browse={() => (pickingMount = true)}
                  on:openAdd={(e) => { addingTo = e.detail; addSource = ''; }}
                  on:closeAdd={() => { addingTo = ''; addSource = ''; }}
                />


                {/if}
              </div>
            {/if}
          </div>
        </div>

        <!-- Terminal drawer -->
        {#if drawerOpen}
          <!-- editor↔drawer sash: drag up = taller, widens + highlights on hover -->
          <div
            class="group relative h-px shrink-0 my-2 bg-fjord-border cursor-row-resize"
            use:resizer={{ axis: 'y', onMove: (d) => setDrawer(drawerHeight - d) }}
            on:dblclick={() => setDrawer(256)}
            title="Drag to resize · double-click to reset"
          >
            <div class="absolute inset-x-0 -top-1.5 -bottom-1.5 group-hover:bg-fjord-accent/60 transition-colors"></div>
          </div>
          <div class="shrink-0 flex flex-col min-h-0" style="height:{drawerHeight}px">
            <!-- tab bar -->
            <div class="flex items-center gap-1 mb-1">
              <button
                on:click={() => showTab(selectedStack!.name, 'output')}
                class="flex items-center gap-1.5 px-3 py-1 rounded-t-md text-xs font-medium transition-colors {drawerTab ===
                'output'
                  ? 'bg-fjord-card text-fjord-fg border-b-2 border-fjord-accent'
                  : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="list" size={13} /> Output</button
              >
              <button
                on:click={() => showTab(selectedStack!.name, 'logs')}
                class="px-3 py-1 rounded-t-md text-xs font-medium transition-colors flex items-center gap-1.5 {drawerTab ===
                'logs'
                  ? 'bg-fjord-card text-fjord-fg border-b-2 border-fjord-accent'
                  : 'text-fjord-fg-muted hover:text-fjord-fg'}"
              >
                <Icon name="logs" size={13} /> Logs
                {#if logStacking === selectedStack.name}
                  <span class="w-1.5 h-1.5 rounded-full bg-fjord-warning animate-pulse"></span>
                {/if}
              </button>
              {#if drawerTab === 'logs' && shellContainers.length > 1}
                <div class="relative ml-1">
                  <button
                    on:click={() => (logsMenuOpen = !logsMenuOpen)}
                    title="Choose which services' logs to show"
                    class="bg-fjord-bg border border-fjord-border rounded px-2 py-0.5 text-[11px] font-mono focus:outline-none {logsAll
                      ? 'text-fjord-fg-secondary'
                      : 'text-fjord-accent border-fjord-accent/50'}"
                    ><span class="flex items-center gap-1"
                      >{logsAll ? 'Services' : `${logsSelected.length}/${shellContainers.length} services`}
                      <Icon name="chevron-down" size={11} /></span
                    ></button
                  >
                  {#if logsMenuOpen}
                    <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
                    <div class="fixed inset-0 z-20" on:click={() => (logsMenuOpen = false)}></div>
                    <div class="absolute z-30 top-full left-0 mt-1 bg-fjord-card border border-fjord-border rounded-lg shadow-2xl p-2 min-w-48">
                      {#each shellContainers as c}
                        <label class="flex items-center gap-2 px-1.5 py-1 rounded text-xs text-fjord-fg-secondary cursor-pointer hover:bg-fjord-border/50 hover:text-fjord-fg select-none">
                          <input
                            type="checkbox"
                            checked={logsSel[c.name] !== false}
                            on:change={(e) => {
                              logsSel[c.name] = e.currentTarget.checked;
                              logsSel = logsSel;
                              streamLogs(selectedStack!.name); // restart scoped
                            }}
                            class="accent-fjord-accent"
                          />
                          <span class="font-mono">{serviceLabel(selectedStack.name, c.name)}</span>
                        </label>
                      {/each}
                    </div>
                  {/if}
                </div>
              {/if}
              <button
                on:click={() => showTab(selectedStack!.name, 'shell')}
                class="flex items-center gap-1.5 px-3 py-1 rounded-t-md text-xs font-medium transition-colors {drawerTab ===
                'shell'
                  ? 'bg-fjord-card text-fjord-fg border-b-2 border-fjord-accent'
                  : 'text-fjord-fg-muted hover:text-fjord-fg'}"><Icon name="terminal" size={13} /> Shell</button
              >
              {#if drawerTab === 'shell' && shellContainers.length > 1}
                <select
                  value={shellContainer}
                  on:change={(e) => (shellPick = e.currentTarget.value)}
                  title="Container to open a shell in"
                  class="ml-1 bg-fjord-bg border border-fjord-border rounded px-2 py-0.5 text-[11px] text-fjord-fg-secondary font-mono focus:outline-none focus:border-fjord-accent"
                >
                  {#each shellContainers as c}
                    <option value={c.name}>{serviceLabel(selectedStack.name, c.name)}</option>
                  {/each}
                </select>
              {/if}
              <div class="flex-1"></div>
              <button
                on:click={() => {
                  stopLogs();
                  setDrawerOpen(false);
                }}
                class="flex items-center gap-1 text-xs font-medium text-fjord-fg-muted hover:text-fjord-fg"
                ><Icon name="chevron-down" size={12} /> Hide</button
              >
            </div>
            {#if drawerTab === 'logs'}
              <LogView
                text={containerLogs[selectedStack.name] || ''}
                streaming={logStacking === selectedStack.name}
              />
            {:else if drawerTab === 'shell'}
              {#if shellContainer && shellContainerUp}
                {#key selectedStack.name + '/' + shellContainer}
                  <Shell stack={selectedStack.name} container={shellContainer} />
                {/key}
              {:else}
                <div
                  class="flex-1 flex items-center justify-center text-sm text-fjord-fg-dim bg-fjord-card border border-fjord-border rounded-xl"
                >
                  Start the stack to open a shell.
                </div>
              {/if}
            {:else}
              <Terminal
                bind:logs={logs[selectedStack.name]}
                status={execStatus[selectedStack.name] || 'idle'}
                statusMessage={execMessage[selectedStack.name] || ''}
              />
            {/if}
          </div>
        {:else}
          <button
            on:click={() => setDrawerOpen(true)}
            class="shrink-0 mt-3 flex items-center gap-2 px-4 py-2 border border-fjord-border rounded-lg text-xs font-medium text-fjord-fg-muted hover:text-fjord-fg hover:border-fjord-accent/40 transition-colors"
          >
            <Icon name="chevron-up" size={12} /> Panel
            {#if execStatus[selectedStack.name] === 'running'}
              <span class="w-1.5 h-1.5 rounded-full bg-fjord-warning animate-ping"></span>
            {:else if logs[selectedStack.name]}
              <span class="text-fjord-fg-dim">· last output</span>
            {/if}
          </button>
        {/if}
      </div>
    {:else if currentView === 'store'}
      <div class="p-6 h-full overflow-hidden">
        <AppStore on:install={handleInstall} />
      </div>
    {:else if currentView === 'volumes'}
      <div class="p-6 h-full overflow-hidden">
        <Volumes />
      </div>
    {:else if currentView === 'networks'}
      <div class="p-6 h-full overflow-hidden">
        <Networks />
      </div>
    {:else if currentView === 'adopt'}
      <div class="p-6 h-full overflow-hidden">
        <Adopt
          on:back={() => (currentView = 'stacks')}
          on:adopted={async (e) => {
            await loadStacks();
            if (e.detail) {
              const st = stacks.find((x) => x.name === e.detail);
              if (st) {
                await selectStack(st);
                up(st.name);
              }
            }
          }}
        />
      </div>
    {:else if currentView === 'system'}
      <div class="p-6 h-full overflow-hidden">
        <System />
      </div>
    {:else if currentView === 'settings'}
      <div class="p-6 h-full overflow-hidden">
        <Settings tab={settingsTab} on:tab={(e) => (settingsTab = e.detail)} />
      </div>
    {:else}
      <!-- Default landing: dashboard overview of all stacks -->
      <div class="p-6 h-full overflow-hidden">
        <Dashboard
          {stacks}
          {fleet}
          {fleetRefreshing}
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
{#if changeVersion}
  <ChangeVersionModal
    name={changeVersion.service ? `${changeVersion.name} · ${changeVersion.service}` : changeVersion.name}
    image={changeVersion.image}
    suggestTag={changeVersion.suggest ?? ''}
    on:apply={applyVersion}
    on:close={() => (changeVersion = null)}
  />
{/if}

{#if pickingMount}
  <DirPicker
    start={addSource || '/containers'}
    on:select={(e) => { addSource = e.detail; pickingMount = false; }}
    on:close={() => (pickingMount = false)}
  />
{/if}

{#if deleting}
  <div class="fixed inset-0 bg-black/70 backdrop-blur-sm flex items-center-safe justify-center overflow-y-auto z-50 p-4">
    <div class="bg-fjord-card border border-fjord-border rounded-xl shadow-2xl w-full max-w-sm p-8 flex flex-col items-center text-center gap-4">
      <div class="w-14 h-14 rounded-full bg-fjord-danger/15 border border-fjord-danger/30 flex items-center justify-center text-fjord-danger">
        <Spinner size={26} class="text-fjord-danger" />
      </div>
      <div>
        <h3 class="text-lg font-bold text-fjord-fg">Deleting {deleting}</h3>
        <p class="text-sm text-fjord-fg-muted mt-1">Stopping and removing its containers. This can take a few seconds…</p>
      </div>
    </div>
  </div>
{/if}

<Toasts />
