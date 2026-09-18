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
  import EngineMark from './EngineMark.svelte';
  import { appIcons, loadAppIcons, iconFor, tile, initial } from './appIcons';
  import Toasts from './Toasts.svelte';
  import { toast, dismissToast } from './toast';
  import { expandVars } from './expand';
  import { appUrl } from './appUrl';
  import { currentTheme, setTheme, watchSystem, type Theme } from './theme';
  import { networkLabel, HOST_NETWORK, DEFAULT_NETWORK, randomMAC } from './network';

  type ContainerStatus = {
    name: string;
    state: string;
    ports?: { hostPort: number; containerPort: number; protocol?: string }[];
    detail?: string; // one-line reason for a non-running state (appjail: crash-looping app)
    address?: string; // the container's own IP on an attachable network
  };
  type StackStatus = { state: string; containers: ContainerStatus[] };
  type StackState = { group?: string; desired_state?: string; engine?: string; order?: number; origin?: { app_id?: string } };
  type Stack = { name: string; displayName?: string; icon?: string; dir: string; compose: string; env: string; director?: string; makejail?: string; engine?: string; status?: StackStatus; state?: StackState };
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
  let changeVersion: { name: string; image: string; suggest?: string } | null = null;

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
    selectedStack && (selectedStack as any).network && !openUrl
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
  // Number of containers in the selected stack -- the Services list only shows
  // when there's more than one (single-service stacks are just the header).
  $: serviceCount = selectedStack?.status?.containers?.length ?? 0;

  // Inline confirmation for destructive stack actions (Stop / Delete): a
  // banner under the stack header, not a modal, so the rest of the app stays
  // usable and the question stays visible. Cleared when the selection changes.
  let pendingAction: { kind: 'stop' | 'delete'; stack: string } | null = null;
  let pendingConfirmBtn: HTMLButtonElement | null = null;
  $: if (pendingAction && selectedStack?.name !== pendingAction.stack) pendingAction = null;
  // Focus the confirm button so Enter confirms and Escape cancels from the keyboard.
  $: if (pendingAction) tick().then(() => pendingConfirmBtn?.focus());
  function confirmPending() {
    const a = pendingAction;
    pendingAction = null;
    if (!a) return;
    if (a.kind === 'stop') down(a.stack);
    else deleteStack(a.stack);
  }

  // Attachable macvlan networks (empty on hosts without them, e.g. saturn).
  type Network = { name: string; driver: string; subnet: string; gateway: string };
  let networks: Network[] = [];
  // What the SAVED compose says, so revert() and post-save reset go back to it
  // rather than blanking the picker.
  // The stack's networks, in interface order: row 0 is eth0. The table is the
  // whole truth -- what is listed here replaces what the compose declares.
  type Attachment = { network: string; ip?: string; mac?: string };
  let netRows: Attachment[] = [];
  let savedRows = '';
  $: netRowsDirty = JSON.stringify(netRows) !== savedRows;

  function addNetRow() {
    const free = networks.find((n) => !netRows.some((r) => r.network === n.name));
    netRows = [...netRows, { network: free?.name ?? '', ip: '', mac: '' }];
  }
  function removeNetRow(i: number) {
    netRows = netRows.filter((_, j) => j !== i);
  }
  function moveNetRow(i: number, to: number) {
    if (to < 0 || to >= netRows.length) return;
    const copy = [...netRows];
    [copy[i], copy[to]] = [copy[to], copy[i]];
    netRows = copy;
  }

  // netRowsDirty belongs here: the network table edits staged state rather than
  // the compose, so without it removing a row showed no Unsaved badge at all --
  // the change looked like it had already happened, or like nothing had.
  $: isDirty = selectedStack
    ? selectedStack.compose !== originalCompose || selectedStack.env !== originalEnv || (selectedStack.director ?? '') !== originalDirector || (selectedStack.makejail ?? '') !== originalMakejail || netRowsDirty
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
  let updateInfo: { state: string; tag?: string; latest?: string; newTag?: string; toVersion?: string } | null = null;
  let checkingUpdate = false;

  async function checkForUpdate(name: string) {
    checkingUpdate = true;
    updateInfo = null;
    try {
      const res = await fetch(`/api/stacks/${name}/update-check`);
      if (res.ok) updateInfo = await res.json();
    } catch {
      // registry/socket unreachable -> leave it unknown, no badge
    } finally {
      checkingUpdate = false;
    }
  }

  async function selectStack(stack: Stack | null) {
    activeTab = 'compose';
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
    // write-only and reads "Host ports (default)" for every attached stack.
    netRows = ((stack as any)!.networks ?? []).map((a: Attachment) => ({ ...a }));
    savedRows = JSON.stringify(netRows);
    // Resources tab is engine-scoped to this stack (see loadNetworks/loadVolumes).
    loadNetworks(stack!.name);
    loadVolumes(stack!.name);
    loadMounts();
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
          // compose/env with the list's empty values.
          selectedStack = { ...selectedStack, status: found.status, state: found.state };
        }
      }
    }
  }

  // Networks/volumes are engine-specific: when a stack is selected the Resources
  // tab must show ITS engine's resources (appjail virtualnets, not podman's), so
  // scope the query to that stack. Unscoped = the default engine's view.
  async function loadNetworks(stackId?: string) {
    try {
      const q = stackId ? '?stack=' + encodeURIComponent(stackId) : '';
      const res = await fetch('/api/networks' + q);
      if (res.ok) networks = (await res.json()) || []; // null when none -> [] (guards networks.length)
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
  type UpdateInfo = { state: string; tag?: string; latest?: string; newTag?: string; toVersion?: string };
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
  type Mount = { source: string; dest: string; readOnly: boolean; kind: string; service: string };
  let mounts: Mount[] = [];
  let addKind: 'bind' | 'volume' | 'remote' = 'bind';
  let addRemoteKind: RemoteKind = 'nfs';
  let addSource = '';
  let addDest = '';
  let addRO = false;
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
  async function addFolderSet(setId: string) {
    const set = folderSets.find((s) => s.id === setId);
    if (!selectedStack || !set) return;
    // No container path typed: mount the set at /<set-name> ("Movies" ->
    // /movies), which is what the set is for; type a path first to override.
    let dest = addDest.trim().replace(/\/+$/, '');
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
        const { compose } = await mountsAPI({ op: 'add', kind, source, dest: at, readOnly: addRO });
        selectedStack = { ...selectedStack, compose };
      }
      addSource = '';
      addDest = '';
      addRO = false;
      await loadMounts();
    } catch (e: any) {
      toast(e.message || 'Add failed', { kind: 'error' });
    }
  }
  // appjail binds host paths via fstab and has no podman named volumes.
  $: canUseVolumes = !selectedStack?.state?.engine || selectedStack.state.engine === 'podman';
  $: if (!canUseVolumes && addKind !== 'bind') addKind = 'bind';

  async function mountsAPI(body: Record<string, unknown>) {
    const res = await fetch('/api/compose/mounts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ compose: selectedStack?.compose ?? '', env: selectedStack?.env ?? '', ...body }),
    });
    if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
    return res.json();
  }
  async function loadMounts() {
    if (!selectedStack) return;
    try {
      mounts = (await mountsAPI({ op: 'list' })).mounts || [];
    } catch {
      mounts = [];
    }
    if (!folderSets.length) loadFolderSets();
  }
  async function addMount() {
    if (!selectedStack || !addSource.trim() || !addDest.trim()) return;
    try {
      const { compose } = await mountsAPI({
        op: 'add',
        kind: addKind,
        source: addSource.trim(),
        dest: addDest.trim(),
        readOnly: addRO,
      });
      selectedStack = { ...selectedStack, compose };
      addSource = '';
      addDest = '';
      addRO = false;
      await loadMounts();
    } catch (e: any) {
      toast(e.message || 'Add failed', { kind: 'error' });
    }
  }
  // NFS/SMB from the Resources tab: same row format + credential store as the
  // folder-set editor (remote.ts), then mounted as its named volume.
  async function addRemoteMount(row: string) {
    if (!selectedStack) return;
    const dest = addDest.trim();
    if (!dest) {
      toast('Enter the container path to mount it at', { kind: 'error' });
      return;
    }
    try {
      const source = await ensureRemoteVolume(row, { stack: selectedStack.name });
      const { compose } = await mountsAPI({ op: 'add', kind: 'volume', source, dest, readOnly: addRO });
      selectedStack = { ...selectedStack, compose };
      addDest = '';
      addRO = false;
      addKind = 'bind';
      await loadMounts();
      loadVolumes(selectedStack.name);
    } catch (e: any) {
      toast(e.message || 'Add failed', { kind: 'error' });
    }
  }
  // Removing a mount rewrites the compose straight away -- no Save, no revert --
  // so it asks first, the same two-click confirm the Networks page uses. The
  // data is untouched, but putting the source path back is the user's problem.
  let confirmUnmount = '';
  async function removeMount(dest: string) {
    if (!selectedStack) return;
    confirmUnmount = '';
    try {
      const { compose } = await mountsAPI({ op: 'remove', dest });
      selectedStack = { ...selectedStack, compose };
      await loadMounts();
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
  // mounts table so it reflects the restored compose.
  function revert() {
    if (!selectedStack) return;
    selectedStack = { ...selectedStack, compose: originalCompose, env: originalEnv, ...(isDirector ? { director: originalDirector, makejail: originalMakejail } : {}) };
    netRows = JSON.parse(savedRows || '[]');
    addSource = '';
    addDest = '';
    addRO = false;
    loadMounts();
    toast('Reverted to the last saved version', { kind: 'success' });
  }

  async function save() {
    if (!selectedStack) return;
    saving = true;
    try {
      const body: Record<string, unknown> = {
        compose: selectedStack.compose,
        env: selectedStack.env,
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
      if (netRowsDirty) {
        // An empty list detaches: the table is the whole truth, so clearing
        // it has to mean something rather than quietly doing nothing.
        body.networks = netRows
          .filter((r) => r.network)
          .map((r) => ({ network: r.network, ip: (r.ip || '').trim(), mac: (r.mac || '').trim() }));
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
        // If we injected a network/volume the on-disk compose changed;
        // re-fetch it so the editor shows the real (injected) compose.
        if (body.network || body.volume) {
          const detail = await fetch(`/api/stacks/${selectedStack.name}`);
          if (detail.ok) selectedStack = await detail.json();
          // Re-read the picker from what was just written, so it keeps showing
          // the stack's network instead of snapping back to "Host ports".
          netRows = ((selectedStack as any)!.networks ?? []).map((a: Attachment) => ({ ...a }));
          savedRows = JSON.stringify(netRows);
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
      } else {
        toast((await res.text()).trim() || `HTTP ${res.status}`, { kind: 'error' });
      }
    } finally {
      saving = false;
    }
  }

  // Run a streamed lifecycle action (up/down/restart/update) and pipe its
  // output into the terminal drawer.
  async function streamAction(name: string, action: string, msg: string) {
    stopLogs(); // action output goes to the Output tab, not the Logs stream
    drawerTab = 'output';
    // The backend announces its own "$ command" header lines in the stream --
    // it is the source of truth for what actually runs, whatever the engine.
    logs[name] = '';
    execStatus[name] = 'running';
    execMessage[name] = msg;
    drawerOpen = true;
    try {
      const res = await fetch(`/api/stacks/${name}/${action}`, { method: 'POST' });
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
      execStatus[name] = 'idle';
      execMessage[name] = '';
      await loadStacks();
      // The stack may still be "partial" (yellow) the instant an action
      // returns. The SSE stream (subscribeEvents) pushes the settle to
      // running/stopped as the containers finish -- no polling needed.
    } catch (err: any) {
      execStatus[name] = 'error';
      execMessage[name] = 'Failed';
      logs[name] += `[ERROR]: ${err?.message || err}\n`;
    }
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
  const update = async (name: string) => {
    await streamAction(name, 'update', 'Updating…');
    if (execStatus[name] !== 'error') needsApply[name] = false; // update recreates too
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
    changeVersion = null;
    if (!name) return;
    const tagChanged = e.detail.tag !== refTag(curImage);
    const res = await fetch(`/api/stacks/${name}/set-tag`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ tag: e.detail.tag, pin: e.detail.pin }),
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
    if (tagChanged) await update(name);
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
  }>) {
    const { name, appId, engine, manifest, values, paths, appData, tag, network, ip } = e.detail;

    // The stack id is allocated server-side; until the response header arrives
    // key the optimistic terminal by a temporary handle, then re-key to the id.
    let key = 'installing:' + name;
    currentView = 'stacks';
    selectedStack = { name: key, displayName: name, dir: '', compose: '', env: '' };
    originalCompose = '';
    originalEnv = '';
    originalDirector = '';
    originalMakejail = '';
    logs[key] = `Installing ${name}...\n`;
    execStatus[key] = 'running';
    execMessage[key] = 'Installing...';

    try {
      const res = await fetch('/api/apps/install', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, manifest, values, ...(paths ? { paths } : {}), ...(appData ? { appData } : {}), ...(appId ? { app_id: appId } : {}), ...(engine ? { engine } : {}), ...(tag ? { tag } : {}), ...(network ? { network, ip } : {}) }),
      });
      // Re-key optimistic state from the temp handle to the real allocated id.
      // The server sends the id as soon as the stack exists on disk -- also on
      // a refused bring-up -- so the saved stack is what the user lands on.
      const rekey = () => {
        const id = res.headers.get('X-Fjord-Stack-Id') || key;
        if (id === key) return;
        logs[id] = logs[key];
        execStatus[id] = execStatus[key];
        execMessage[id] = execMessage[key];
        delete logs[key];
        delete execStatus[key];
        delete execMessage[key];
        if (selectedStack?.name === key) selectedStack = { ...selectedStack, name: id };
        key = id;
      };
      if (!res.ok) {
        const msg = (await res.text()).trim();
        rekey();
        if (key.startsWith('installing:')) {
          // Nothing was saved (bad input, unknown app): no stack to show, so
          // don't leave an empty "Create" draft behind -- report and go back.
          delete logs[key];
          delete execStatus[key];
          delete execMessage[key];
          selectedStack = null;
          currentView = 'store';
          toast(`Install failed: ${msg || `HTTP ${res.status}`}`, { kind: 'error', timeout: 10000 });
          return;
        }
        execStatus[key] = 'error';
        execMessage[key] = `HTTP ${res.status}`;
        logs[key] += `[ERROR]: ${msg}\n`;
        await loadStacks();
        const saved = stacks.find((s) => s.name === key) || null;
        if (saved) selectStack(saved);
        return;
      }
      rekey();
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
      if (found) selectStack(found);
    } catch (err: any) {
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
                      ? `v${fleet[stack.name].toVersion} available`
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
              >{pendingAction.kind === 'delete' ? 'Delete' : 'Stop'}</button
            >
          </div>
        {/if}

        <!-- HIG banner: persistent saved-but-unapplied state, with its action -->
        {#if needsApply[selectedStack.name]}
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
            on:click={() => update(selectedStack!.name)}
            disabled={execStatus[selectedStack.name] === 'running'}
            title={updateInfo?.state === 'available'
              ? `Update available — ${updateInfo.tag} moved to ${(updateInfo.latest ?? '').replace('sha256:', '').slice(0, 12)}…`
              : 'Pull the latest images and recreate'}
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
              >v{updateInfo!.toVersion} available</button
            >
          {:else if updateInfo?.state === 'current'}
            <span class="flex items-center gap-1 text-xs text-fjord-success self-center"
              ><Icon name="check" size={12} /> Up to date</span
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

        <!-- Services (only shown when the stack has more than one) -->
        {#if serviceCount > 1}
          <div class="mb-4 shrink-0 border border-fjord-border rounded-xl overflow-hidden">
            <div class="bg-fjord-border/40 px-4 py-2 text-xs font-semibold text-fjord-fg-secondary">Services</div>
            <div class="divide-y divide-fjord-border">
              {#each selectedStack.status?.containers ?? [] as c}
                <div class="flex items-center gap-3 px-4 py-2 text-sm">
                  <span class="w-2 h-2 rounded-full shrink-0 {DOT[c.state === 'running' ? 'running' : 'stopped']}"></span>
                  <span class="text-fjord-fg-body">{c.name}</span>
                  <span class="text-fjord-fg-dim text-xs">{c.state}</span>
                </div>
              {/each}
            </div>
          </div>
        {/if}

        <!-- Editor + tabs -->
        <div class="flex-1 flex flex-col bg-fjord-card border border-fjord-border rounded-xl shadow-xl overflow-hidden min-h-0">
          <div class="bg-fjord-border/50 flex border-b border-fjord-border text-xs font-semibold text-fjord-fg-secondary">
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
            <button
              on:click={() => (activeTab = 'env')}
              class="px-4 py-2.5 border-r border-fjord-border flex items-center gap-2 {activeTab === 'env'
                ? 'bg-fjord-card text-fjord-fg border-b-2 border-b-fjord-accent'
                : 'text-fjord-fg-muted hover:text-fjord-fg-body'}"
            >
              .env{#if selectedStack.env !== originalEnv}<span class="text-fjord-warning font-bold">*</span>{/if}
            </button>
            <button
              on:click={() => { activeTab = 'net'; loadMounts(); }}
              class="px-4 py-2.5 border-r border-fjord-border {activeTab === 'net'
                ? 'bg-fjord-card text-fjord-fg border-b-2 border-b-fjord-accent'
                : 'text-fjord-fg-muted hover:text-fjord-fg-body'}">Resources</button
            >
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
                <h3 class="text-lg font-bold text-fjord-fg mb-1">Networking</h3>
                {#if networks.length}
                  <p class="text-xs text-fjord-fg-muted mb-4 max-w-lg">
                    Each row is one interface, in order — the first is <span class="font-mono">eth0</span> and the
                    one links are built from. Leave an address blank to let the network assign one. Applied to the
                    compose on <b>Save</b>.
                  </p>
                  {#if netRows.length}
                    <table class="w-full max-w-3xl text-sm">
                      <thead>
                        <tr class="text-left text-xs text-fjord-fg-dim">
                          <th class="font-medium pb-1 w-8"></th>
                          <th class="font-medium pb-1">Network</th>
                          <th class="font-medium pb-1">IP</th>
                          <th class="font-medium pb-1">MAC</th>
                          <th class="pb-1 w-20"></th>
                        </tr>
                      </thead>
                      <tbody>
                        {#each netRows as row, i}
                          <tr class="border-t border-fjord-border">
                            <td class="py-2 pr-2 font-mono text-xs text-fjord-fg-dim">eth{i}</td>
                            <td class="py-2 pr-2">
                              <select
                                bind:value={row.network}
                                on:change={() => (netRows = netRows)}
                                class="w-full bg-fjord-inset border border-fjord-border rounded px-2 py-1 text-sm text-fjord-fg-body focus:border-fjord-accent outline-none"
                              >
                                {#each networks as n}
                                  <option value={n.name} disabled={netRows.some((r, j) => j !== i && r.network === n.name)}
                                    >{n.name} ({n.subnet || 'DHCP'})</option
                                  >
                                {/each}
                              </select>
                            </td>
                            <td class="py-2 pr-2">
                              <input
                                bind:value={row.ip}
                                on:input={() => (netRows = netRows)}
                                placeholder="auto"
                                class="w-full bg-fjord-inset border border-fjord-border rounded px-2 py-1 text-sm font-mono text-fjord-fg-body focus:border-fjord-accent outline-none"
                              />
                            </td>
                            <td class="py-2 pr-2">
                              <div class="flex gap-1">
                                <input
                                  bind:value={row.mac}
                                  on:input={() => (netRows = netRows)}
                                  placeholder="auto"
                                  class="w-full bg-fjord-inset border border-fjord-border rounded px-2 py-1 text-sm font-mono text-fjord-fg-body focus:border-fjord-accent outline-none"
                                />
                                <!-- Fills a blank field only. Changing a MAC breaks
                                     the DHCP reservation keyed on it, so overwriting
                                     one has to be deliberate: clear it first. -->
                                <button
                                  type="button"
                                  on:click={() => { row.mac = randomMAC(); netRows = netRows; }}
                                  disabled={!!(row.mac || '').trim()}
                                  title={(row.mac || '').trim()
                                    ? 'Clear the field first — changing a MAC breaks a DHCP reservation keyed on it'
                                    : 'Generate a locally-administered address'}
                                  class="shrink-0 px-2 rounded border border-fjord-border text-xs text-fjord-fg-secondary hover:bg-fjord-border disabled:opacity-30 disabled:hover:bg-transparent">Gen</button
                                >
                              </div>
                            </td>
                            <td class="py-2 text-right whitespace-nowrap">
                              <button
                                type="button"
                                on:click={() => moveNetRow(i, i - 1)}
                                disabled={i === 0}
                                title="Move up (earlier interface)"
                                class="px-1.5 text-fjord-fg-muted hover:text-fjord-fg disabled:opacity-30">↑</button
                              >
                              <button
                                type="button"
                                on:click={() => removeNetRow(i)}
                                title="Detach from this network"
                                class="px-1.5 text-fjord-fg-muted hover:text-fjord-danger">✕</button
                              >
                            </td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  {:else}
                    <p class="text-xs text-fjord-fg-dim">
                      Host ports — this stack publishes on the host address.
                    </p>
                  {/if}
                  {#if netRows.length}
                    <!-- The eth numbers are what the NEXT container will get:
                         interfaces are assigned at create time, so a running
                         one keeps its layout until it is recreated. Saying so
                         beats letting the table look like live state. -->
                    <p class="text-xs mt-2 {netRowsDirty ? 'text-fjord-warning' : 'text-fjord-fg-dim'}">
                      {#if netRowsDirty}
                        Not saved yet — <b>Save</b>, then restart the stack for these to take effect.
                      {:else}
                        Interface names are assigned when a container is created, so a running stack keeps
                        its current layout until it is restarted.
                      {/if}
                    </p>
                  {/if}
                  {#if netRows.length < networks.length}
                    <button
                      type="button"
                      on:click={addNetRow}
                      class="mt-3 text-sm px-3 py-1.5 rounded-lg border border-fjord-border text-fjord-fg-secondary hover:bg-fjord-border"
                      >+ Add network</button
                    >
                  {/if}
                {:else}
                  <p class="text-xs text-fjord-fg-dim mb-4 max-w-lg">
                    No attachable networks on this host — the stack publishes ports on the host address.
                  </p>
                {/if}

                <h3 class="text-lg font-bold text-fjord-fg mb-1 mt-8">Storage</h3>
                <p class="text-xs text-fjord-fg-muted mb-4 max-w-lg">
                  Folders and volumes mounted into this stack. Changes edit the compose — <b>Save</b> to apply
                  (running containers pick it up on <b>Apply</b>/recreate).
                </p>

                <!-- current mounts, parsed from the compose -->
                {#if mounts.length}
                  <div class="border border-fjord-border rounded-lg divide-y divide-fjord-border max-w-2xl mb-4">
                    {#each mounts as m}
                      <div class="flex items-center gap-3 px-3 py-2 text-sm">
                        <Icon name={m.kind === 'volume' ? 'drive' : 'folder'} size={15} class="text-fjord-fg-dim shrink-0" />
                        <span class="font-mono text-fjord-fg-secondary truncate" title={m.source}>{m.source}</span>
                        <Icon name="chevron-right" size={13} class="text-fjord-fg-faint shrink-0" />
                        <span class="font-mono text-fjord-fg truncate shrink-0" title={m.dest}>{m.dest}</span>
                        {#if m.readOnly}
                          <span class="shrink-0 text-[10px] font-semibold px-1.5 py-0.5 rounded bg-fjord-bg border border-fjord-border text-fjord-fg-muted">RO</span>
                        {/if}
                        <span class="shrink-0 ml-auto text-[10px] px-1.5 py-0.5 rounded bg-fjord-bg border border-fjord-border text-fjord-fg-dim">{m.kind}</span>
                        {#if confirmUnmount === m.dest}
                          <button
                            on:click={() => removeMount(m.dest)}
                            class="shrink-0 text-[10px] px-1.5 py-0.5 rounded bg-fjord-danger hover:bg-fjord-danger-hover text-white"
                            >Confirm</button
                          >
                          <button
                            on:click={() => (confirmUnmount = '')}
                            class="shrink-0 text-[10px] px-1.5 py-0.5 rounded text-fjord-fg-muted hover:text-fjord-fg">Cancel</button
                          >
                        {:else}
                          <button
                            on:click={() => (confirmUnmount = m.dest)}
                            title="Unmount this — the files stay, the stack stops seeing them"
                            class="shrink-0 text-fjord-fg-dim hover:text-fjord-danger transition-colors"><Icon name="trash" size={14} /></button
                          >
                        {/if}
                      </div>
                    {/each}
                  </div>
                {:else}
                  <p class="text-xs text-fjord-fg-faint mb-4">No storage mounted yet.</p>
                {/if}

                <!-- add a mount: host path (Browse) or, on podman, a named volume -->
                <div class="flex flex-wrap items-end gap-3 max-w-2xl">
                  {#if canUseVolumes}
                    <div class="flex rounded-lg overflow-hidden border border-fjord-border shrink-0">
                      <button
                        on:click={() => { addKind = 'bind'; addSource = ''; }}
                        class="px-3 py-2 text-xs font-medium {addKind === 'bind' ? 'bg-fjord-accent text-white' : 'bg-fjord-inset text-fjord-fg-muted hover:text-fjord-fg'}">Host path</button
                      >
                      <button
                        on:click={() => { addKind = 'volume'; addSource = ''; }}
                        class="px-3 py-2 text-xs font-medium border-l border-fjord-border {addKind === 'volume' ? 'bg-fjord-accent text-white' : 'bg-fjord-inset text-fjord-fg-muted hover:text-fjord-fg'}">Volume</button
                      >
                      <button
                        on:click={() => { addKind = 'remote'; addSource = ''; }}
                        class="px-3 py-2 text-xs font-medium border-l border-fjord-border {addKind === 'remote' ? 'bg-fjord-accent text-white' : 'bg-fjord-inset text-fjord-fg-muted hover:text-fjord-fg'}">NFS / SMB</button
                      >
                    </div>
                  {/if}
                  {#if addKind === 'remote'}
                    <!-- Remote folder: same form as the folder-set editor; mounted at the container path on the right. -->
                    <div class="flex items-center gap-2">
                      <select bind:value={addRemoteKind} class="bg-fjord-inset border border-fjord-border rounded-lg px-2 py-2 text-xs text-fjord-fg-secondary focus:border-fjord-accent outline-none">
                        <option value="nfs">NFS</option>
                        <option value="smb">SMB</option>
                      </select>
                      {#key addRemoteKind}
                        <RemoteFolderForm kind={addRemoteKind} on:add={(e) => addRemoteMount(e.detail)} on:cancel={() => (addKind = 'bind')} />
                      {/key}
                    </div>
                  {:else if addKind === 'volume'}
                    <select
                      bind:value={addSource}
                      class="bg-fjord-inset border border-fjord-border rounded-lg px-3 py-2 text-sm text-fjord-fg-body focus:border-fjord-accent outline-none"
                    >
                      <option value="">Volume…</option>
                      {#each namedVolumes as v}
                        <option value={v.name}>{v.name}{v.kind && v.kind !== 'local' ? ` (${v.kind.toUpperCase()})` : ''}</option>
                      {/each}
                    </select>
                  {:else}
                    <div class="flex items-center gap-1.5">
                      <input
                        bind:value={addSource}
                        placeholder="/host/path"
                        class="w-52 bg-fjord-inset border border-fjord-border rounded-lg px-3 py-2 text-sm text-fjord-fg-body font-mono focus:border-fjord-accent outline-none"
                      />
                      <button
                        on:click={() => (pickingMount = true)}
                        title="Browse the host filesystem"
                        class="shrink-0 px-2.5 py-2 rounded-lg text-xs font-medium bg-fjord-inset border border-fjord-border text-fjord-fg-secondary hover:text-fjord-fg hover:border-fjord-accent/40 transition-colors">Browse…</button
                      >
                      {#if folderSets.length}
                        <select
                          aria-label="Add a folder set"
                          title="Mounts the set's folders under the container path on the right"
                          class="shrink-0 bg-fjord-inset border border-fjord-border rounded-lg px-2 py-2 text-xs text-fjord-fg-secondary focus:border-fjord-accent outline-none"
                          on:change={(e) => { addFolderSet(e.currentTarget.value); e.currentTarget.value = ''; }}
                        >
                          <option value="">Add folder set…</option>
                          {#each folderSets as fs}<option value={fs.id}>{fs.name}</option>{/each}
                        </select>
                      {/if}
                    </div>
                  {/if}
                  <Icon name="chevron-right" size={14} class="text-fjord-fg-faint shrink-0 mb-2.5" />
                  <input
                    bind:value={addDest}
                    placeholder="/container/path"
                    class="w-48 bg-fjord-inset border border-fjord-border rounded-lg px-3 py-2 text-sm text-fjord-fg-body font-mono focus:border-fjord-accent outline-none"
                  />
                  <label class="flex items-center gap-1.5 text-sm text-fjord-fg-secondary cursor-pointer select-none mb-2">
                    <input type="checkbox" bind:checked={addRO} class="accent-fjord-accent" /> RO
                  </label>
                  {#if addKind !== 'remote'}
                    <button
                      on:click={addMount}
                      disabled={!addSource.trim() || !addDest.trim()}
                      class="px-4 py-2 rounded-lg text-sm font-medium bg-fjord-border hover:bg-fjord-accent hover:text-white transition-colors disabled:opacity-40">Add</button
                    >
                  {/if}
                </div>
                {#if addKind === 'volume' && namedVolumes.length === 0}
                  <p class="text-xs text-fjord-fg-faint mt-2">No named volumes yet — create one on the Volumes page.</p>
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
    name={changeVersion.name}
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
  <div class="fixed inset-0 bg-black/70 backdrop-blur-sm flex items-center justify-center z-50 p-4">
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
