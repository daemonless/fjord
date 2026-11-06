<script lang="ts">
  import { tick } from 'svelte';
  import { staleNote } from './staleNote';
  import { ensureRemoteVolume, remoteKind } from './remote';
  import Editor from './Editor.svelte';
  import BottomDrawer from './BottomDrawer.svelte';
  import ChangeVersionModal from './ChangeVersionModal.svelte';
  import RestartPolicy from './RestartPolicy.svelte';
  import DirPicker from './DirPicker.svelte';
  import Spinner from './Spinner.svelte';
  import LoginButton from './LoginButton.svelte';
  import OpenMenu from './OpenMenu.svelte';
  import { names, openBy, deviceTrust, openUrl as nameOrAddress, opensByName, linkUrl } from './names';
  import TrustMark from './TrustMark.svelte';
  import NamesPrompt from './NamesPrompt.svelte';
  import Icon from './Icon.svelte';
  import { fitMenu } from './fitMenu';
  import ServiceResources from './ServiceResources.svelte';
  import EngineMark from './EngineMark.svelte';
  import { imageIcons, serviceIcon } from './appIcons';
  import { toast } from './toast';
  import { openHow, noWebUI } from './appUrl';
  import { busyStep } from './busyStep';
  import { changeSentence } from './updateWords';
  import { parseEnv, missingVars, unusedVars, setEnvVar, isSecret, usedVars } from './composeVars';
  import { serviceLinks } from './serviceLinks';
  import VarsPanel from './VarsPanel.svelte';
  import {
    BUSY_LABEL,
    currentImage,
    existingGroupsOf,
    isDraftEdited,
    isMultiImage,
    newStackDraft as makeStackDraft,
    stackImage,
    stackLabel as label,
    statusLabel,
    statusStyle,
    type Attachment,
    type Network,
    type ServiceUpdate,
    type Stack,
    type UpdateInfo,
  } from './stacks';

  export let stack: Stack;
  export let stacks: Stack[] = [];
  export let activeTab: 'compose' | 'makejail' | 'env' | 'net' = 'compose';
  export let originalCompose = '';
  export let originalEnv = '';
  export let originalDirector = '';
  export let originalMakejail = '';
  export let hostOS = '';
  export let engines: { name: string; available: boolean; enabled: boolean }[] = [];
  export let defaultEngine = 'podman';
  export let saving = false;
  export let isDirty = false;
  export let isDraft = false;
  export let drawerOpen = true;
  export let drawerTab: 'output' | 'logs' | 'shell' = 'output';
  export let networks: Network[] = [];
  export let svcNets: Record<string, Attachment[]> = {};
  export let namedVolumes: { name: string; kind?: string }[] = [];
  export let updateInfo: UpdateInfo | null = null;
  export let updateCheckedAt = 0;
  export let checkingUpdate = false;
  export let updatePanel = '';
  export let saveConflict = '';
  export let logs: Record<string, string> = {};
  export let execStatus: Record<string, 'idle' | 'running' | 'watching' | 'error'> = {};
  export let following: Record<string, boolean> = {};
  export let execMessage: Record<string, string> = {};
  export let needsApply: Record<string, boolean> = {};
  export let applyDismissed: Record<string, boolean> = {};
  export let failureDismissed: Record<string, boolean> = {};

  export let onSave: () => void = () => {};
  export let onRevert: () => void = () => {};
  export let onReloadStack: (name: string) => void = () => {};
  export let onSelectStack: (stack: Stack | null) => Promise<void> | void = () => {};
  export let onLoadStacks: () => Promise<void> | void = () => {};
  export let onLoadVolumes: (stackId?: string) => Promise<void> | void = () => {};
  export let onCheckForUpdate: (name: string) => Promise<void> | void = () => {};
  export let onUp: (name: string) => Promise<void> | void = () => {};
  export let onDown: (name: string) => Promise<void> | void = () => {};
  export let onRestart: (name: string) => Promise<void> | void = () => {};
  export let onUpdate: (name: string, services?: string[]) => Promise<void> | void = () => {};
  export let onRollback: (name: string, service: string) => Promise<void> | void = () => {};
  export let onUnpin: (name: string, service: string) => Promise<void> | void = () => {};
  export let onUpgradeTo: (name: string, tag: string) => Promise<void> | void = () => {};
  export let onUpdatePicked: (name: string, svcs: ServiceUpdate[]) => Promise<void> | void = () => {};
  export let onApplyVersion: (
    change: { name: string; image: string; suggest?: string; service?: string },
    tag: string,
    pin: boolean,
  ) => Promise<void> | void = () => {};
  export let onDeleteStack: (name: string, withData?: boolean) => Promise<void> | void = () => {};
  export let onRenameStack: (id: string, newName: string) => Promise<void> | void = () => {};
  export let onSetStackGroup: (name: string, group: string) => Promise<void> | void = () => {};
  export let onDismissFailure: (name: string) => Promise<void> | void = () => {};

  let drawer: BottomDrawer | null = null;
  export function stopLogs() {
    drawer?.stopLogs();
  }
  export function resetLogs() {
    drawer?.resetLogs();
  }
  export function followLogsIfActive(name: string) {
    drawer?.followLogsIfActive(name);
  }

  // Switch a draft's runtime: swap in that engine's starter files. The two
  // engines' files are different formats (compose vs director + Makejail), so
  // nothing carries over -- which was silent, and took every edit with it. An
  // edited draft asks first; an untouched one just switches.
  let engineSwitch = ''; // the engine a confirm is pending for
  function draftEdited(): boolean {
    return isDraftEdited(stack, defaultEngine, hostOS);
  }
  function setDraftEngine(engine: string, confirmed = false) {
    if (!stack || !isDraft || engine === (stack.engine ?? defaultEngine)) return;
    if (!confirmed && draftEdited()) {
      engineSwitch = engine;
      return;
    }
    engineSwitch = '';
    stack = makeStackDraft(engine, hostOS, stack.name);
    activeTab = 'compose';
  }
  $: if (!isDraft) engineSwitch = '';
  const draftFiles = (engine: string) => (engine === 'appjail' ? 'a director file and a Makejail' : 'a compose.yaml');

  let actionsMenuOpen = false; // stack header overflow menu
  let editingName = false; // inline click-to-rename on the stack title
  let nameEdit = '';
  $: nameEditValid = /^[\p{L}\p{N}][\p{L}\p{N} ._-]{0,63}$/u.test(nameEdit);

  // Commit the inline rename: updates the stack's display label. Identity (id,
  // URL, containers) is unchanged, so it's instant -- no navigation, no restart.
  function commitRename() {
    editingName = false;
    const id = stack?.name;
    const next = nameEdit.trim();
    if (!id || !next || next === label(stack) || !nameEditValid) return;
    if (isDraft) {
      // Nothing on the server yet: keep the label on the draft; Create sends it.
      stack = { ...stack, displayName: next };
      return;
    }
    onRenameStack(id, next);
  }

  // Change-version modal: {name, image} of the stack being retagged, or null.
  // service set: that service alone (the Services tab); unset: the whole
  // single-image stack (the ⋮ menu).
  let changeVersion: { name: string; image: string; suggest?: string; service?: string } | null = null;

  // Multi-image stacks (e.g. app+db+redis) have no single version to change --
  // the Change Version modal would retag every service. Hidden for those.
  $: multiImage = isMultiImage(stack?.compose);
  // A director-backed appjail stack: its runtime spec is appjail-director.yml,
  // not compose.yaml (kept only for status/log service discovery). The primary
  // tab edits the director spec; director reconciles changes on the next up.
  $: isDirector = !!stack?.director;
  $: existingGroups = existingGroupsOf(stacks);

  // Derive a clickable URL for a webapp stack: its own IP when it has one,
  // else the host you're browsing fjord on, plus its primary published web
  // port. Empty when the stack publishes nothing web-ish.
  $: openUrl = nameOrAddress(stack, $names, $openBy, $deviceTrust);
  // Beside Open, for an app on its own address: why the port is the app's and
  // not the compose's web_port. Not when Open goes by name.
  $: openHowNote = openByName ? '' : openHow(stack);
  $: openByName = opensByName(stack, $names, $openBy);
  // A stack of several apps opens each at its own address: Open is the main
  // one, the menu beside it the rest.
  // Each by its name when it has one and this browser opens by name.
  $: links = stack
    ? serviceLinks(stack.compose ?? '', parseEnv(stack.env ?? ''), stack.services ?? [], location.hostname).map(
        (l) => ({ ...l, url: linkUrl($names, `${stack.name}/${l.service}`, l.url, $openBy, $deviceTrust) }),
      )
    : [];
  let linksOpen = false;
  $: linkIcon = (service: string) =>
    serviceIcon($imageIcons, (stack?.services ?? []).find((s: any) => s.name === service)?.image);

  // The .env as the editors resolve ${VAR} against it, and what the compose
  // (or director file) uses that it never sets -- shown on the .env tab.
  // A director file's ${PWD} is the stack's folder: fjord sets it on every run.
  let stackVars: Record<string, string> = {};
  $: stackVars = {
    ...parseEnv(stack?.env ?? ''),
    ...(stack?.director ? { PWD: stack.dir } : {}),
  };
  $: unsetVars = missingVars((stack?.director || stack?.compose) ?? '', stackVars);
  // .env lines nothing the engine runs uses: WEB_PORT once a stack has its
  // own address, where no port is published. Edited, they changed nothing.
  $: unusedEnv = unusedVars((stack?.director || stack?.compose) ?? '', parseEnv(stack?.env ?? ''));
  // Variables on the compose tab. Each ${VAR} carries its value in the
  // editor (Values), and clicking one sets it in a small box right there.
  // The Variables panel lists them all, missing first; with it open, a
  // click jumps to the field instead. Both toggles are remembered.
  const remembered = (key: string, dflt: boolean) => {
    try {
      const v = localStorage.getItem(key);
      return v === null ? dflt : v === 'on';
    } catch {
      return dflt;
    }
  };
  const remember = (key: string, on: boolean) => {
    try {
      localStorage.setItem(key, on ? 'on' : 'off');
    } catch {}
  };
  let varLabels = remembered('fjord.varLabels', true);
  let varsPanelOpen = remembered('fjord.varsPanel', false);
  $: remember('fjord.varLabels', varLabels);
  $: remember('fjord.varsPanel', varsPanelOpen);
  let varsFocus = '';
  let varPop: { name: string; x: number; y: number; value: string } | null = null;
  $: varsFile = (isDirector ? stack?.director : stack?.compose) ?? '';
  $: fileVars = usedVars(varsFile, stackVars);
  $: fileMissing = fileVars.filter((v) => v.state === 'unset').length;
  function setStackVar(name: string, value: string) {
    if (stack) stack.env = setEnvVar(stack.env ?? '', name, value);
  }
  function varClicked(name: string, rect: DOMRect) {
    if (varsPanelOpen) varsFocus = name;
    else varPop = { name, x: rect.left, y: rect.bottom + 4, value: stackVars[name] ?? '' };
  }
  // .env values the compose never names reach no container: hand them to
  // the app's (first) service, an edit held until Save.
  async function passUnusedEnv() {
    if (!stack) return;
    try {
      const res = await fetch('/api/compose/pass-env', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ compose: stack.compose ?? '', env: stack.env ?? '', names: unusedEnv }),
      });
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
      adopt(await res.json());
    } catch (e: any) {
      toast(e.message || 'Could not change the compose', { kind: 'error' });
    }
  }
  function addUnsetVars() {
    if (!stack) return;
    const env = (stack.env ?? '').replace(/\s*$/, '');
    stack.env = (env ? env + '\n' : '') + unsetVars.map((n) => `${n}=`).join('\n') + '\n';
  }
  // Attached to a network but holding no address: the reason the Open button
  // is missing, taken from whichever container reported it.
  $: noAddress =
    stack && (stack as any).ownAddress && !openUrl && !noWebUI(stack)
      ? (stack.status?.containers || []).find((c: any) => c.detail?.includes('no address'))?.detail ||
        `no address on ${(stack as any).network} yet`
      : '';

  // Inline confirmation for destructive stack actions (Stop / Delete): a
  // banner under the stack header, not a modal, so the rest of the app stays
  // usable and the question stays visible. Cleared when the selection changes.
  let pendingAction: { kind: 'stop' | 'delete' | 'recreate'; stack: string } | null = null;
  let pendingConfirmBtn: HTMLButtonElement | null = null;
  // What a delete removes and leaves, from the daemon (delete-preview): its
  // containers and stack files always; its own app-data folders only when
  // ticked; anything else it uses is listed as untouched.
  type DeletePlan = {
    containers?: string[];
    stackDir: string;
    appData: { path: string; bytes: number; more?: boolean }[];
    keeps: { path: string; why: string }[];
  };
  let deletePlan: DeletePlan | 'loading' | 'error' | null = null;
  let deleteData = false;
  $: if (pendingAction?.kind === 'delete' && deletePlan === null) loadDeletePlan(pendingAction.stack);
  $: if (pendingAction?.kind !== 'delete') {
    deletePlan = null;
    deleteData = false;
  }
  async function loadDeletePlan(name: string) {
    deletePlan = 'loading';
    try {
      const r = await fetch(`/api/stacks/${name}/delete-preview`);
      deletePlan = r.ok ? await r.json() : 'error';
    } catch {
      deletePlan = 'error';
    }
  }
  const fmtBytes = (n: number) =>
    n < 1024 ? `${n} B` : n < 1024 ** 2 ? `${(n / 1024).toFixed(0)} KB` : n < 1024 ** 3 ? `${(n / 1024 ** 2).toFixed(1)} MB` : `${(n / 1024 ** 3).toFixed(1)} GB`;
  $: if (pendingAction && stack?.name !== pendingAction.stack) pendingAction = null;
  // Focus the confirm button so Enter confirms and Escape cancels from the keyboard.
  $: if (pendingAction) tick().then(() => pendingConfirmBtn?.focus());
  function confirmPending() {
    const a = pendingAction;
    pendingAction = null;
    if (!a) return;
    if (a.kind === 'stop') onDown(a.stack);
    else if (a.kind === 'recreate') onUpdate(a.stack);
    else onDeleteStack(a.stack, deleteData);
  }

  // Which built-ins this engine cannot give a service, as the daemon says.
  // The rows are where a mode is chosen now, so this goes to them.
  $: unsupportedModes = ((stack as any)?.unsupportedModes ?? []) as string[];

  // The Update panel. Update used to act on click -- pull everything and
  // recreate every container whether or not anything had changed, with no
  // word about which part of the stack was behind. Now the button opens this
  // list first, and acting on it is a second, deliberate click.
  $: if (updatePanel && stack?.name !== updatePanel) updatePanel = '';
  // Older than this and the panel asks again before offering to act on it.
  const UPDATE_FRESH_MS = 2 * 60 * 1000;
  function openUpdatePanel(name: string) {
    updatePanel = name;
    unpicked = {};
    if (!updateInfo || Date.now() - updateCheckedAt > UPDATE_FRESH_MS) onCheckForUpdate(name);
  }
  $: updatable = (updateInfo?.services ?? []).filter((s) => s.state === 'available');
  // What the panel can take one service at a time: a new build, or -- where
  // a service can be retagged on its own (pinServices) -- a new version. All
  // ticked by default; the ones unticked are remembered, so a re-check keeps
  // the choice.
  $: offered = updateInfo?.perService
    ? (updateInfo.services ?? []).filter(
        (s) => s.state === 'available' || (s.state === 'upgrade' && !!s.newTag && !!updateInfo?.pinServices),
      )
    : [];
  let unpicked: Record<string, boolean> = {};
  $: picked = offered.filter((s) => !unpicked[s.service]);
  $: svcPinned = Object.fromEntries(
    (updateInfo?.services ?? []).filter((s) => s.state === 'pinned').map((s) => [s.service, true]),
  ) as Record<string, boolean>;
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
        : updateInfo?.state === 'upgrade' && multiImage && !updateInfo.pinServices
          ? `A newer version is published (v${updateInfo.toVersion}) — set it in the compose editor`
          : '';
  // A single-image stack behind by a VERSION: Update takes it there. It used
  // to be refused ("use Change Version"), so a downgraded stack showed an
  // update next to a greyed-out Update button. Multi-image stacks cannot be
  // retagged in one go (set-tag refuses them), so there it stays a note.
  $: versionStep =
    updateInfo?.state === 'upgrade' && !multiImage && !updateInfo.pinServices && updateInfo.newTag
      ? { tag: updateInfo.newTag, from: updateInfo.fromVersion, to: updateInfo.toVersion }
      : null;
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
  const changesKey = (stackName: string, s: ServiceUpdate) => `${stackName}:${s.service}:${s.latest ?? s.newTag ?? ''}`;
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

  // ---- Resources → Storage: mounts table over the compose (source of truth) ----
  // The backend (/api/compose/mounts) is stateless: it parses / transforms the
  // editor's in-memory compose text and hands it back, so adds/removes just edit
  // stack.compose (marking it dirty) and Save persists + recreates.
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
    const slug = stack?.name || 'app'; // its folder is named by its id
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
  async function addFolderSet(service: string, setId: string, at: string, readOnly: boolean, options: string[] = []) {
    const set = folderSets.find((s) => s.id === setId);
    if (!stack || !set) return;
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
          source = await ensureRemoteVolume(f, { stack: stack.name });
          kind = 'volume';
        }
        adopt(await mountsAPI({ op: 'add', kind, service, source, dest: at, readOnly, options }));
      }
      addSource = '';
      addingTo = '';
    } catch (e: any) {
      toast(e.message || 'Add failed', { kind: 'error' });
    }
  }
  // appjail binds host paths via fstab and has no podman named volumes.
  $: canUseVolumes = !stack?.state?.engine || stack.state.engine === 'podman';

  async function mountsAPI(body: Record<string, unknown>) {
    const res = await fetch('/api/compose/mounts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      // A director stack's mounts live in its director file, not the compose
      // appjail never reads: that file is what gets edited and handed back.
      body: JSON.stringify({
        compose: stack?.compose ?? '',
        env: stack?.env ?? '',
        ...(isDirector ? { director: stack?.director ?? '', name: stack?.name ?? '' } : {}),
        ...body,
      }),
    });
    if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`);
    return res.json();
  }
  async function addMount(d: { service: string; kind: string; source: string; dest: string; readOnly: boolean; options?: string[] }) {
    if (!stack || !d.source.trim() || !d.dest.trim()) return;
    try {
      adopt(await mountsAPI({ op: 'add', ...d }));
      addSource = '';
      addingTo = '';
    } catch (e: any) {
      toast(e.message || 'Add failed', { kind: 'error' });
    }
  }
  // A mount's options changed (or "Add :z" on an SELinux warning): an edit
  // like adding one, held until Save.
  async function setMountOptions(d: { service: string; dest: string; options: string[] }) {
    if (!stack) return;
    try {
      adopt(await mountsAPI({ op: 'options', ...d }));
    } catch (e: any) {
      toast(e.message || 'Could not set the options', { kind: 'error' });
    }
  }
  // NFS/SMB from the Resources tab: same row format + credential store as the
  // folder-set editor (remote.ts), then mounted as its named volume.
  async function addRemoteMount(d: { service: string; row: string; dest: string; readOnly: boolean; options?: string[] }) {
    if (!stack) return;
    if (!d.dest) {
      toast('Enter the container path to mount it at', { kind: 'error' });
      return;
    }
    try {
      const source = await ensureRemoteVolume(d.row, { stack: stack.name });
      adopt(await mountsAPI({ op: 'add', kind: 'volume', service: d.service, source, dest: d.dest, readOnly: d.readOnly, options: d.options }));
      addingTo = '';
      onLoadVolumes(stack.name);
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
  function adopt(res: { compose?: string; director?: string; services?: unknown[] }) {
    if (!stack) return;
    stack = {
      ...stack,
      ...(res.director !== undefined ? { director: res.director } : { compose: res.compose ?? stack.compose }),
      services: res.services ?? stack.services,
    };
  }

  // Removing a mount rewrites the compose straight away -- no Save, no revert --
  // so it asks first (in the row itself). The data is untouched, but putting
  // the source path back is the user's problem.
  async function removeMount(d: { service: string; dest: string }) {
    if (!stack) return;
    try {
      adopt(await mountsAPI({ op: 'remove', service: d.service, dest: d.dest }));
    } catch (e: any) {
      toast(e.message || 'Remove failed', { kind: 'error' });
    }
  }

  function revert() {
    addSource = '';
    addingTo = '';
    onRevert();
  }

  $: if (saveConflict && stack?.name !== saveConflict) saveConflict = '';

  $: busyNow = !!stack && (execStatus[stack.name] === 'running' || !!stack.busy);
  // What the Output pane's badge shows: running while this page streams an action or follows another page's.
  $: termStatus = (
    !stack
      ? 'idle'
      : ['running', 'watching'].includes(execStatus[stack.name]) || following[stack.name]
        ? 'running'
        : execStatus[stack.name] === 'error'
          ? 'error'
          : 'idle'
  ) as 'idle' | 'running' | 'error';
  // Down to the stay-up check: the actions are back, and say what using one does.
  $: watchNote = stack && execStatus[stack.name] === 'watching' ? 'Checking it stays up — any action ends the check' : '';
  // One sentence about the stack, under the actions: what fjord is doing,
  // else what a container reports (named when the stack has several), else
  // the missing address, else the engine's lag. In the header's chip row
  // these stretched it, moved Open about and got cut to "the app keeps crash…".
  $: containerNote = (() => {
    const cs = stack?.status?.containers || [];
    const c = cs.find((x) => x.detail);
    if (!c) return '';
    return cs.length > 1 && (c.service || c.name) ? `${c.service || c.name}: ${c.detail}` : c.detail || '';
  })();
  $: staleText = stack ? staleNote(stack.status, stack.state?.engine || stack.engine) : '';
  $: stackNote = busyReason || watchNote || containerNote || noAddress || staleText;
  // Why the actions are greyed, in words: what fjordd is doing to the stack,
  // or what this page started. Grey alone read as broken.
  // The step itself, when the output names one: the chip up top already
  // says "Installing…", and a pull took minutes with nothing else to go on.
  $: busyStepNow = stack && busyNow ? busyStep(logs[stack.name] || '') : '';
  $: busyReason = !stack
    ? ''
    : busyStepNow
      ? `${busyStepNow} — actions come back when it finishes`
      : stack.busy
        ? `${BUSY_LABEL[stack.busy] ?? 'Busy…'} — actions come back when it finishes`
        : execStatus[stack.name] === 'running'
          ? `${execMessage[stack.name] || 'Working…'} — actions come back when it finishes`
          : '';

  const FAILED: Record<string, string> = { install: 'Install failed', up: 'Start failed', update: 'Update failed', restart: 'Restart failed', down: 'Stop failed' };
  const failedWhen = (at: string) => {
    const d = new Date(at);
    const sameDay = d.toDateString() === new Date().toDateString();
    return sameDay ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  };
  $: pendingApply = !!stack && (stack.state?.pending_services?.length ?? 0) > 0 &&
    ['running', 'partial'].includes(statusLabel(stack.status));
</script>

<!-- The app menu beside Open closes on a click anywhere else. -->
<svelte:window
  on:click={(e) => {
    const t = e.target as HTMLElement;
    if (linksOpen && !t?.closest?.('.svc-links')) linksOpen = false;
  }}
/>

<div class="flex flex-col h-full overflow-hidden p-6">
  <!-- Header -->
  <header class="flex justify-between items-center mb-4 shrink-0 flex-wrap gap-y-2">
    <div class="flex items-center gap-3 min-w-0 flex-wrap gap-y-1">
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
        <!-- The heading stays a heading; the rename is a button inside it. -->
        <h2 class="shrink-0 max-w-[16rem] text-2xl font-semibold text-fjord-fg truncate">
          <button
            on:click={() => { editingName = true; nameEdit = label(stack); }}
            title="Click to rename (id: {stack.name})"
            class="cursor-text hover:bg-fjord-border/40 focus:outline-none focus:bg-fjord-border/40 rounded px-1 -mx-1 transition-colors"
          >{label(stack)}</button>
        </h2>
      {/if}
      <span
        title={(stack.status?.containers || []).filter((c) => c.detail).map((c) => `${c.name}: ${c.detail}`).join('\n') || ''}
        class="shrink-0 px-2.5 py-0.5 border rounded-full text-[11px] font-semibold capitalize {stack.busy
          ? 'bg-fjord-accent/10 text-fjord-accent border-fjord-accent/30'
          : statusStyle(stack.status)}">{stack.busy
          ? BUSY_LABEL[stack.busy] ?? 'Busy…'
          : statusLabel(stack.status)}</span
      >
      <!-- Chips only up here. What the engine or a container has to say
           is the status line under the actions (stackNote). -->
      {#if stack.state?.engine}
        <span class="shrink-0 flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium bg-fjord-bg border border-fjord-border text-fjord-fg-muted" title="Runtime engine"
          ><EngineMark engine={stack.state.engine} size={12} strokeWidth={2.25} />{stack.state.engine}</span
        >
      {/if}
      {#if noAddress}
        <!-- Open stays where it is, greyed: the reason is the status line. -->
        <span
          class="shrink-0 flex items-center gap-1 text-sm font-medium text-fjord-accent opacity-40"
          title="A stack on its own IP publishes nothing on the host, so there is no link to open until the network gives it an address. {noAddress}"
          ><Icon name="external" size={13} /> Open</span
        >
      {/if}
      {#if openUrl}
        <a
          href={openUrl}
          target="_blank"
          rel="noreferrer"
          title="Open {openUrl}"
          class="min-w-0 flex items-center gap-1 text-sm font-medium text-fjord-accent hover:text-fjord-accent-hover hover:underline {statusLabel(
            stack.status,
          ) === 'running'
            ? ''
            : 'opacity-40 pointer-events-none'}"
          ><Icon name="external" size={13} /> Open
          <!-- where it goes, as the dashboard shows it: the name once the
               app has one, so the name is in sight, not only in the ▾ -->
          <span class="min-w-0 max-w-64 truncate font-mono font-normal text-fjord-fg-secondary">{openUrl.replace(/^https?:\/\//, '').replace(/[#?].*$/, '').replace(/\/$/, '')}</span></a
        >
        {#if openByName && statusLabel(stack.status) === 'running'}<TrustMark {stacks} size={13} />{/if}
        <OpenMenu {stack} disabled={statusLabel(stack.status) !== 'running'} />
        {#if openHowNote}<span class="shrink-0 text-xs text-fjord-fg-dim" title="An app with its own address answers on its container port; x-fjord web_port is where it would be published at this host's address.">{openHowNote}</span>{/if}
        <LoginButton stack={stack.name} />
      {/if}
      {#if openUrl && links.length > 1}
        <details
          class="svc-links relative shrink-0 {statusLabel(stack.status) === 'running' ? '' : 'opacity-40 pointer-events-none'}"
          bind:open={linksOpen}
        >
          <summary
            class="list-none cursor-pointer text-sm font-medium text-fjord-accent hover:text-fjord-accent-hover"
            title="Open another of this stack's {links.length} apps">▾ {links.length}</summary
          >
          <div use:fitMenu class="absolute z-30 left-0 mt-1 min-w-72 bg-fjord-card border border-fjord-border rounded-lg shadow-xl py-1">
            {#each links as l}
              <a
                href={l.url}
                target="_blank"
                rel="noreferrer"
                on:click={() => (linksOpen = false)}
                class="flex items-center justify-between gap-4 px-3 py-1.5 text-sm hover:bg-fjord-border/50"
                ><span class="flex items-center gap-2 font-medium text-fjord-fg-body"
                  >{#if linkIcon(l.service)}<img src={linkIcon(l.service)} alt="" class="w-4 h-4 rounded object-contain" />{:else}<span
                      class="w-4 h-4"
                    ></span>{/if}{l.service}</span
                ><span class="font-mono text-xs text-fjord-fg-muted"
                  >{l.url.replace(/^https?:\/\//, '')}</span
                ></a
              >
            {/each}
          </div>
        </details>
      {/if}
      <!-- The restart policy rewrites the compose on the server: with
           unsaved edits open it would replace them, so it waits. -->
      <RestartPolicy
        name={stack.name}
        restart={stack.restart}
        disabled={busyNow || isDirty}
        reason={busyReason || (isDirty ? 'Save or discard your edits first' : '')}
        on:changed={async () => {
          const name = stack.name;
          applyDismissed[name] = false;
          await onSelectStack({ name } as Stack);
          await onLoadStacks();
        }}
      />
      <input
        list="fjord-groups"
        value={stack.state?.group ?? ''}
        on:change={(e) => onSetStackGroup(stack.name, e.currentTarget.value)}
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
            value={stack.engine ?? defaultEngine}
            on:change={(e) => {
              setDraftEngine(e.currentTarget.value);
              // Until confirmed, the picker keeps showing the engine in use.
              e.currentTarget.value = stack?.engine ?? defaultEngine;
            }}
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
        on:click={onSave}
        disabled={saving}
        class="bg-fjord-accent hover:bg-fjord-accent-hover text-white text-sm font-medium py-1.5 px-4 rounded-lg disabled:opacity-50"
        >{saving ? (isDraft ? 'Creating…' : 'Saving…') : isDraft ? 'Create' : 'Save'}</button
      >
    </div>
  </header>

  <!-- Inline confirmation for Stop / Delete: visible, but nothing is blocked -->
  {#if engineSwitch && isDraft}
    <div
      role="alertdialog"
      aria-live="polite"
      class="flex items-center gap-3 mb-4 shrink-0 px-4 py-2.5 rounded-lg bg-fjord-danger/10 border border-fjord-danger/30 text-sm text-fjord-fg-body"
    >
      <span class="flex-1">
        <b>Switch to {engineSwitch}?</b> It replaces this {stack.director ? 'director file and Makejail' : 'compose.yaml'}
        with {draftFiles(engineSwitch)} — your edits are lost.
      </span>
      <button
        on:click={() => (engineSwitch = '')}
        class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium text-fjord-fg-muted hover:text-fjord-fg transition-colors"
        >Keep {stack.engine ?? defaultEngine}</button
      >
      <button
        on:click={() => setDraftEngine(engineSwitch, true)}
        class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-danger text-white hover:bg-fjord-danger-hover transition-colors"
        >Switch</button
      >
    </div>
  {/if}
  {#if pendingAction && pendingAction.stack === stack.name}
    <div
      role="alertdialog"
      aria-live="polite"
      class="flex {pendingAction.kind === 'delete'
        ? 'items-start'
        : 'items-center'} gap-3 mb-4 shrink-0 px-4 py-2.5 rounded-lg bg-fjord-danger/10 border border-fjord-danger/30 text-sm text-fjord-fg-body"
    >
      <span class="flex-1">
        {#if pendingAction.kind === 'delete'}
          <b>Delete {label(stack)}?</b> Stops it and removes its containers and its settings in fjord.
          {#if deletePlan === 'loading'}
            <span class="block mt-1 text-xs text-fjord-fg-dim">Looking at what it uses…</span>
          {:else if deletePlan && deletePlan !== 'error'}
            {#if deletePlan.appData.length}
              <span class="block mt-2 text-xs">
                <span class="font-medium text-fjord-fg-secondary">Its app data</span>
                {deleteData ? '— deleted with it:' : '— kept on disk unless you tick below:'}
                {#each deletePlan.appData as d}
                  <span class="block font-mono {deleteData ? 'text-fjord-danger' : 'text-fjord-success'}"
                    >{d.path} <span class="font-sans text-fjord-fg-dim">({d.more ? 'more than ' : ''}{fmtBytes(d.bytes)})</span></span
                  >
                {/each}
              </span>
              <label class="flex items-center gap-2 mt-1.5 text-xs cursor-pointer">
                <input type="checkbox" bind:checked={deleteData} class="accent-fjord-danger" />
                Also delete its app data — this cannot be undone
              </label>
            {/if}
            {#if deletePlan.keeps.length}
              <span class="block mt-2 text-xs text-fjord-fg-dim">
                Not touched:
                {#each deletePlan.keeps as k}
                  <span class="block"><span class="font-mono text-fjord-fg-secondary">{k.path}</span> — {k.why}</span>
                {/each}
              </span>
            {/if}
          {:else}
            Its data stays on disk.
          {/if}
        {:else if pendingAction.kind === 'recreate'}
          <b>Pull &amp; recreate {label(stack)}?</b> Pulls every image and recreates every container, including
          ones already up to date.
        {:else}
          <b>Stop {label(stack)}?</b> Stops and removes the containers. Your data is preserved.
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
        disabled={busyNow}
        title={busyReason || undefined}
        class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-danger text-white hover:bg-fjord-danger-hover transition-colors disabled:opacity-40"
        >{pendingAction.kind === 'delete'
          ? deleteData
            ? 'Delete stack and data'
            : 'Delete'
          : pendingAction.kind === 'recreate'
            ? 'Recreate'
            : 'Stop'}</button
      >
    </div>
  {/if}

  <!-- Update panel: what an update would change, per service, before it runs -->
  {#if updatePanel === stack.name}
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
                      <!-- Digests identify the builds but say nothing to read; they
                           stay reachable on hover, the sentence below says what changed. -->
                      <span class="font-sans" title="{shortDigest(s.running) || 'local'} → {shortDigest(s.latest)}">new build</span>
                    {:else if s.state === 'upgrade'}
                      v{s.fromVersion} → v{s.toVersion}
                      {#if multiImage && !updateInfo.pinServices}<span class="font-sans text-fjord-fg-dim">(set in the compose)</span>{/if}
                    {:else if s.state === 'unknown'}
                      <span class="font-sans text-fjord-fg-dim">{s.detail ?? ''}</span>
                    {/if}
                  </td>
                </tr>
                {#if s.state === 'available' || s.state === 'upgrade'}
                  {@const k = changesKey(stack.name, s)}
                  {@const c = changes[k]}
                  {#if c === 'loading'}
                    <tr>{#if offered.length > 1}<td></td>{/if}<td></td><td colspan="3" class="pb-1.5 text-fjord-fg-dim">Reading what changed…</td></tr>
                  {:else if c && c !== 'none' && changeSentence(c)}
                    {@const n = (c.changed?.length ?? 0) + (c.added?.length ?? 0) + (c.removed?.length ?? 0)}
                    <tr>
                      {#if offered.length > 1}<td></td>{/if}
                      <td></td>
                      <td colspan="3" class="pb-1.5 text-fjord-fg-secondary">
                        {changeSentence(c)}
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
                ? onUpgradeTo(stack.name, versionStep.tag)
                : updateInfo?.perService
                  ? onUpdatePicked(stack.name, picked)
                  : onUpdate(stack.name)}
            disabled={(versionStep ? false : updateInfo.perService ? !picked.length : !updatable.length) || busyNow}
            title={busyReason || undefined}
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
  {#if saveConflict === stack.name}
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
        on:click={() => onReloadStack(stack.name)}
        class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-danger text-white hover:bg-fjord-danger-hover transition-colors"
        >Reload</button
      >
    </div>
  {/if}

  {#if stack.state?.last_failure && !failureDismissed[stack.name] && !busyNow}
    {@const f = stack.state.last_failure}
    <div class="flex items-center gap-3 mb-4 shrink-0 px-4 py-2.5 rounded-lg bg-fjord-danger/10 border border-fjord-danger/25 text-sm text-fjord-fg-body">
      <span class="flex-1 min-w-0"
        ><b>{FAILED[f.action] ?? 'Failed'}</b> at {failedWhen(f.at)} — {f.message}</span
      >
      <button
        on:click={() => onDismissFailure(stack.name)}
        title="Hide this until it happens again"
        class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium text-fjord-fg-muted hover:text-fjord-fg transition-colors"
        >Dismiss</button
      >
    </div>
  {/if}

  <!-- HIG banner: persistent saved-but-unapplied state, with its action.
       Hidden while an action runs: that action applies the config, and a
       greyed-out Apply next to "Updating…" only read as broken. -->
  {#if (needsApply[stack.name] || pendingApply) && !applyDismissed[stack.name] && execStatus[stack.name] !== 'running'}
    <div
      class="flex items-center gap-3 mb-4 shrink-0 px-4 py-2.5 rounded-lg bg-fjord-warning/10 border border-fjord-warning/25 text-sm text-fjord-fg-body"
    >
      <span class="flex-1">
        {#if execStatus[stack.name] === 'error' && execMessage[stack.name]}
          <span class="text-fjord-danger font-medium">Couldn't apply:</span> {execMessage[stack.name]}
          <span class="block text-xs text-fjord-fg-dim mt-0.5">The running services still use the old configuration. The Output below has the whole message.</span>
        {:else}
          The running services still use the old configuration — apply the saved changes to recreate them.
        {/if}
      </span>
      <button
        on:click={() => { needsApply[stack.name] = false; applyDismissed[stack.name] = true; }}
        title="Keep the running services as they are; the saved changes apply the next time the stack starts"
        class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium text-fjord-fg-muted hover:text-fjord-fg transition-colors"
        >Not now</button
      >
      <button
        on:click={() => onUp(stack.name)}
        disabled={busyNow}
        title={busyReason || undefined}
        class="shrink-0 px-3 py-1.5 rounded-lg text-sm font-medium bg-fjord-warning/15 text-fjord-warning border border-fjord-warning/40 hover:bg-fjord-warning/25 transition-colors disabled:opacity-40"
        >{execStatus[stack.name] === 'error' ? 'Try again' : 'Apply Changes'}</button
      >
    </div>
  {/if}

  <!-- Action bar: primary actions visible, the rest in the overflow menu.
       Hidden until the stack exists on the server -- a draft can only be
       created (the Save button reads "Create"), not started/updated. -->
  {#if !isDraft}
  <div class="flex flex-wrap items-center gap-2 mb-4 shrink-0">
    <button
      on:click={() => onUp(stack.name)}
      disabled={busyNow}
      title={busyReason || undefined}
      class="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium bg-fjord-border hover:bg-fjord-success/80 hover:text-white transition-colors disabled:opacity-40"
      ><Icon name="play" size={14} /> Start</button
    >
    <button
      on:click={() => (pendingAction = { kind: 'stop', stack: stack.name })}
      disabled={busyNow}
      title={busyReason || undefined}
      class="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium bg-fjord-border hover:bg-fjord-danger hover:text-white transition-colors disabled:opacity-40"
      ><Icon name="stop" size={14} /> Stop…</button
    >
    <button
      on:click={() => (updatePanel === stack.name ? (updatePanel = '') : openUpdatePanel(stack.name))}
      disabled={busyNow || !!updateButtonReason}
      title={busyReason ||
        updateButtonReason ||
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
            name: stack.name,
            image: currentImage(stack),
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
        on:click={() => onCheckForUpdate(stack.name)}
        title="Check again"
        class="flex items-center gap-1 text-xs text-fjord-success self-center hover:underline decoration-dotted"
        ><Icon name="check" size={12} /> Up to date</button
      >
    {:else if updateInfo?.state === 'pinned'}
      <span class="flex items-center gap-1 text-xs text-fjord-accent self-center"
        ><Icon name="pin" size={12} /> Pinned</span
      >
    {/if}
    {#if stackNote}
      <span
        class="min-w-0 truncate text-xs {stackNote === containerNote ? 'text-fjord-warning' : 'text-fjord-fg-dim'}"
        title={stackNote === staleText
          ? 'The engine did not answer in time -- usually a pull writing an image to disk. The state updates as soon as it answers.'
          : stackNote}>{stackNote}</span
      >
    {/if}
    <div class="flex-1"></div>
    <div class="relative">
      <button
        on:click={() => (actionsMenuOpen = !actionsMenuOpen)}
        disabled={busyNow}
        title={busyReason || 'More Actions'}
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
              onRestart(stack.name);
            }}
            class="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-fjord-fg-body hover:bg-fjord-border transition-colors"
            ><Icon name="restart" size={14} class="text-fjord-fg-muted" /> Restart</button
          >
          <button
            on:click={() => {
              actionsMenuOpen = false;
              pendingAction = { kind: 'recreate', stack: stack.name };
            }}
            title="Pull every image and recreate every service, even ones already up to date"
            class="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-fjord-fg-body hover:bg-fjord-border transition-colors"
            ><Icon name="update" size={14} class="text-fjord-fg-muted" /> Pull &amp; recreate all…</button
          >
          {#if !multiImage}
            <button
              on:click={() => {
                actionsMenuOpen = false;
                changeVersion = {
                  name: stack.name,
                  image: currentImage(stack),
                  suggest: updateInfo?.state === 'upgrade' ? updateInfo.newTag : undefined,
                };
              }}
              disabled={!stackImage(stack.compose)}
              class="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-fjord-fg-body hover:bg-fjord-border transition-colors disabled:opacity-40"
              ><Icon name="swap" size={14} class="text-fjord-fg-muted" /> Change Version…{#if /@sha256:/.test(stack.compose)}<Icon
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
              pendingAction = { kind: 'delete', stack: stack.name };
            }}
            class="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-fjord-danger hover:bg-fjord-danger/15 transition-colors"
            ><Icon name="trash" size={14} /> Delete…</button
          >
        </div>
      {/if}
    </div>
  </div>
  {/if}

  <NamesPrompt {stack} {stacks} />

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
        on:click={() => (activeTab = 'compose')}
        class="px-4 py-2.5 border-r border-fjord-border flex items-center gap-2 {activeTab === 'compose'
          ? 'bg-fjord-card text-fjord-fg border-b-2 border-b-fjord-accent'
          : 'text-fjord-fg-muted hover:text-fjord-fg-body'}"
      >
        {#if isDirector}appjail-director.yml{#if (stack.director ?? '') !== originalDirector}<span class="text-fjord-warning font-bold">*</span>{/if}{:else}compose.yaml{#if stack.compose !== originalCompose}<span class="text-fjord-warning font-bold">*</span>{/if}{/if}
      </button>
      <!-- .env after the file it feeds: the compose tab shows each ${VAR}'s
           value and the Variables panel sets them; this is the whole file. -->
      <button
        on:click={() => (activeTab = 'env')}
        class="px-4 py-2.5 border-r border-fjord-border flex items-center gap-2 {activeTab === 'env'
          ? 'bg-fjord-card text-fjord-fg border-b-2 border-b-fjord-accent'
          : 'text-fjord-fg-muted hover:text-fjord-fg-body'}"
      >
        .env{#if stack.env !== originalEnv}<span class="text-fjord-warning font-bold">*</span>{/if}
      </button>
      {#if isDirector}
        <button
          on:click={() => (activeTab = 'makejail')}
          class="px-4 py-2.5 border-r border-fjord-border flex items-center gap-2 {activeTab === 'makejail'
            ? 'bg-fjord-card text-fjord-fg border-b-2 border-b-fjord-accent'
            : 'text-fjord-fg-muted hover:text-fjord-fg-body'}"
        >
          Makejail{#if (stack.makejail ?? '') !== originalMakejail}<span class="text-fjord-warning font-bold">*</span>{/if}
        </button>
      {/if}
    </div>

    <div class="flex-1 relative min-h-0">
      {#if activeTab === 'compose'}
        <div class="h-full flex flex-col">
          {#if fileVars.length}
            <div class="shrink-0 flex items-center justify-end gap-1.5 px-3 py-1 border-b border-fjord-border text-[11px]">
              <button
                on:click={() => (varLabels = !varLabels)}
                aria-pressed={varLabels}
                title={varLabels ? 'Hide the values shown after each ${VAR}' : 'Show each ${VAR}’s value in the file'}
                class="px-2 py-0.5 rounded {varLabels ? 'bg-fjord-border text-fjord-fg' : 'text-fjord-fg-muted hover:text-fjord-fg'}"
                >Values</button
              >
              <button
                on:click={() => (varsPanelOpen = !varsPanelOpen)}
                aria-pressed={varsPanelOpen}
                title="Every variable this file uses, and a field to set each"
                class="px-2 py-0.5 rounded {varsPanelOpen
                  ? 'bg-fjord-border text-fjord-fg'
                  : fileMissing
                    ? 'text-fjord-warning hover:bg-fjord-warning/10'
                    : 'text-fjord-fg-muted hover:text-fjord-fg'}"
                >Variables{#if fileMissing} · {fileMissing} missing{/if}</button
              >
            </div>
          {/if}
          <div class="flex-1 min-h-0 flex">
            <div class="flex-1 min-w-0">
              {#key varLabels}
                {#if isDirector}
                  <Editor
                    bind:content={stack.director}
                    language="yaml"
                    vars={varLabels ? stackVars : null}
                    varsClickable
                    on:varclick={(e) => varClicked(e.detail.name, e.detail.rect)}
                    on:change={(e) => (stack.director = e.detail)}
                    on:save={onSave}
                  />
                {:else}
                  <Editor
                    bind:content={stack.compose}
                    language="yaml"
                    vars={varLabels ? stackVars : null}
                    varsClickable
                    on:varclick={(e) => varClicked(e.detail.name, e.detail.rect)}
                    on:change={(e) => (stack.compose = e.detail)}
                    on:save={onSave}
                  />
                {/if}
              {/key}
            </div>
            {#if varsPanelOpen && fileVars.length}
              <VarsPanel
                text={varsFile}
                env={stack.env ?? ''}
                builtins={stack.director ? { PWD: stack.dir } : {}}
                bind:focus={varsFocus}
                on:set={(e) => setStackVar(e.detail.name, e.detail.value)}
                on:close={() => (varsPanelOpen = false)}
              />
            {/if}
          </div>
        </div>
      {:else if activeTab === 'makejail'}
        <Editor
          bind:content={stack.makejail}
          language="env"
          on:change={(e) => (stack.makejail = e.detail)}
          on:save={onSave}
        />
      {:else if activeTab === 'env'}
        <div class="h-full flex flex-col">
          {#if unusedEnv.length}
            {@const portsOnly = (stack as any).ownAddress && unusedEnv.every((n) => /PORT$/.test(n))}
            <div class="shrink-0 flex items-center gap-3 px-4 py-2 text-xs bg-fjord-card border-b border-fjord-border text-fjord-fg-secondary">
              <span class="flex-1">
                <span class="font-mono text-fjord-fg-body">{unusedEnv.join(', ')}</span>
                {unusedEnv.length === 1 ? 'is' : 'are'} not used by this stack{#if portsOnly}:
                  it has its own address on {(stack as any).network}, so nothing is published on this host's
                  ports, and the app answers on its own port there{:else}, so changing {unusedEnv.length === 1 ? 'it' : 'them'} has no effect{/if}.
              </span>
              {#if !portsOnly && !stack.director}
                <button
                  on:click={passUnusedEnv}
                  title="Add {unusedEnv.map((n) => `${n}=\${${n}}`).join(', ')} to the app's environment in the compose"
                  class="shrink-0 px-2.5 py-1 rounded-lg font-medium bg-fjord-border hover:bg-fjord-accent hover:text-white transition-colors"
                  >Pass {unusedEnv.length === 1 ? 'it' : 'them'} to the app</button
                >
              {/if}
            </div>
          {/if}
          {#if unsetVars.length}
            <div class="shrink-0 flex items-center gap-3 px-4 py-2 text-xs bg-fjord-warning/10 border-b border-fjord-warning/30 text-fjord-fg-body">
              <span class="flex-1">
                The {stack.director ? 'director file' : 'compose'} uses
                <span class="font-mono text-fjord-warning">{unsetVars.join(', ')}</span>, which
                {unsetVars.length === 1 ? 'is' : 'are'} not set here and {unsetVars.length === 1 ? 'has' : 'have'} no default.
              </span>
              <button
                on:click={addUnsetVars}
                class="shrink-0 px-2.5 py-1 rounded-lg font-medium bg-fjord-border hover:bg-fjord-accent hover:text-white transition-colors"
                >Add {unsetVars.length === 1 ? 'it' : 'them'}</button
              >
            </div>
          {/if}
          <div class="flex-1 min-h-0">
            <Editor
              bind:content={stack.env}
              language="env"
              on:change={(e) => (stack.env = e.detail)}
              on:save={onSave}
            />
          </div>
        </div>
      {:else}
        <div class="p-6 overflow-y-auto h-full">
          {#if !stack.compose && !stack.director}
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
          <!-- Answers given at install, not status: here with the
               services they decided, not as chips in the header. -->
          {#if Object.keys(stack.state?.choices ?? {}).length}
            <p class="text-xs text-fjord-fg-dim -mt-2 mb-3">
              Chosen at install: {Object.entries(stack.state?.choices ?? {})
                .map(([c, o]) => `${c.replace(/_/g, ' ')} ${o}`)
                .join(' · ')}
            </p>
          {/if}
          <ServiceResources
            services={stack.services ?? []}
            iconOf={(image) => serviceIcon($imageIcons, image)}
            oneBridgePerService={(stack.state?.engine || stack.engine || defaultEngine) === 'appjail'}
            bridgeAlone={(stack.state?.engine || stack.engine || defaultEngine) === 'appjail'}
            updates={svcUpdates}
            rollbacks={updateInfo?.rollback ?? {}}
            pinned={svcPinned}
            on:openUpdate={() => openUpdatePanel(stack.name)}
            on:rollback={(e) => onRollback(stack.name, e.detail)}
            on:unpin={(e) => onUnpin(stack.name, e.detail)}
            versionable={!stack.director || (!!updateInfo?.pinServices && !!stack.compose?.trim())}
            on:version={(e) =>
              (changeVersion = {
                name: stack.name,
                service: e.detail.service,
                image: e.detail.image,
                suggest: svcUpdates[e.detail.service]?.newTag,
              })}
            {networks}
            {unsupportedModes}
            stackName={stack.name}
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
            on:folderset={(e) => addFolderSet(e.detail.service, e.detail.id, e.detail.dest, e.detail.readOnly, e.detail.options)}
            on:mountoptions={(e) => setMountOptions(e.detail)}
            on:browse={() => (pickingMount = true)}
            on:openAdd={(e) => { addingTo = e.detail; addSource = ''; }}
            on:closeAdd={() => { addingTo = ''; addSource = ''; }}
          />
          {/if}
        </div>
      {/if}
    </div>
  </div>

  <BottomDrawer
    bind:this={drawer}
    {stack}
    bind:open={drawerOpen}
    bind:tab={drawerTab}
    bind:outputLogs={logs[stack.name]}
    {termStatus}
    statusMessage={execMessage[stack.name] || ''}
  />
</div>

{#if varPop}
  <!-- Set one variable right where its label is -->
  <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
  <div class="fixed inset-0 z-40" on:click={() => (varPop = null)}></div>
  <div
    class="fixed z-50 w-72 p-3 rounded-lg bg-fjord-card border border-fjord-border shadow-xl text-sm"
    style="left: {Math.min(varPop.x, window.innerWidth - 300)}px; top: {varPop.y}px"
  >
    <label class="block text-xs text-fjord-fg-secondary mb-1" for="varpop-input"
      ><span class="font-mono">{varPop.name}</span> in .env</label
    >
    <!-- svelte-ignore a11y-autofocus -->
    <input
      id="varpop-input"
      autofocus
      type={isSecret(varPop.name) ? 'password' : 'text'}
      bind:value={varPop.value}
      on:keydown={(e) => {
        if (e.key === 'Enter' && varPop) {
          setStackVar(varPop.name, varPop.value);
          varPop = null;
        } else if (e.key === 'Escape') varPop = null;
      }}
      class="w-full bg-fjord-inset border border-fjord-border rounded-md px-2 py-1.5 font-mono text-xs text-fjord-fg-body"
    />
    <div class="flex justify-end gap-2 mt-2">
      <button on:click={() => (varPop = null)} class="px-2.5 py-1 rounded-lg text-xs text-fjord-fg-muted hover:text-fjord-fg">Cancel</button>
      <button
        on:click={() => {
          if (varPop) setStackVar(varPop.name, varPop.value);
          varPop = null;
        }}
        class="px-2.5 py-1 rounded-lg text-xs font-medium bg-fjord-accent text-white hover:bg-fjord-accent-hover">Set</button
      >
    </div>
  </div>
{/if}
{#if changeVersion}
  <ChangeVersionModal
    name={changeVersion.service ? `${changeVersion.name} · ${changeVersion.service}` : changeVersion.name}
    image={changeVersion.image}
    suggestTag={changeVersion.suggest ?? ''}
    on:apply={(e) => {
      if (changeVersion) onApplyVersion(changeVersion, e.detail.tag, e.detail.pin);
      changeVersion = null;
    }}
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
