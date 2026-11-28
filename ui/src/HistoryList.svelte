<script lang="ts">
  import { createEventDispatcher, onMount } from 'svelte';
  import { isoDate, isoDateTime } from './when';
  import { notCoveredParts, zfsHintMountpoint } from './restorePoints';
  import { changeLines, historyTitle, imageLabel, whenLabel, type HistoryEntry } from './history';

  export let stack = '';
  export let limit = 100;
  /** Bumped by the parent to read again, e.g. after an action ends. */
  export let reload = 0;

  const dispatch = createEventDispatcher<{ changed: void }>();

  interface RestoreSnapshot {
    name: string;
    kind: string;
    note?: string;
    hostPath?: string;
    createdAt: string;
    usedBytes: number;
    clones?: string[];
  }

  interface KeptCopy {
    dataset: string;
    mountpoint: string;
    kind: string;
    createdAt: string;
    until?: string;
    version?: string;
    image?: string;
    usedBytes: number;
  }

  interface RestorePointsData {
    dataset?: string;
    hasZfs: boolean;
    pinned: boolean;
    pinnedServices?: string[];
    currentVersion?: string;
    policy?: string;
    retentionDays?: number;
    dailyTime?: string;
    restoredFolders: string[];
    notRestoredFolders: string[];
    notCovered?: string[];
    snapshots: RestoreSnapshot[];
    keptCopies: KeptCopy[];
  }

  function restoreButtonLabel(e: HistoryEntry): string {
    if (e.action !== 'update') return 'Restore';
    const label = imageLabel(e.changes?.[0]?.from);
    if (label && !label.startsWith('sha256:') && label.length <= 20) return `Restore ${label}`;
    return 'Restore prior';
  }

  function snapTitle(s: RestoreSnapshot): string {
    if (s.kind === 'manual') return s.note || 'Manual restore point';
    if (s.kind === 'scheduled') return 'Daily restore point';
    return 'Before update';
  }

  function snapBadge(s: RestoreSnapshot): string {
    if (s.kind === 'manual') return 'Manual';
    if (s.kind === 'scheduled') return 'Daily';
    return 'Before update';
  }

  interface MigratePreview {
    eligible: boolean;
    reason?: string;
    hasZfs: boolean;
    sourceDir?: string;
    targetLocation?: string;
    targetDir?: string;
    targetDataset?: string;
    crossLocation?: boolean;
    sizeBytes: number;
    neededBytes: number;
    freeBytes: number;
    estSeconds: number;
    zfsCreateHint?: string;
  }

  let entries: HistoryEntry[] = [];
  let rp: RestorePointsData | null = null;
  let migratePlan: MigratePreview | null = null;
  let zfsLocations: string[] = [];
  let migrateModalOpen = false;
  let migrateKeepOld = true;
  let loading = true;
  let failedToLoad = false;

  let filter: 'all' | 'restore' = 'all';
  let expandedPaths: Record<string, boolean> = {};
  let openMenuFor = '';
  let busy = false;
  let actionError = '';
  let statusNote = '';

  // Modal state
  let saveModalOpen = false;
  let saveNote = '';
  let resumeModalOpen = false;
  let restoreSnap: RestoreSnapshot | null = null;
  let restoreMode: '' | 'app' | 'both' = '';
  let restoreKeepCopy = true;
  let restoreCopyTarget: KeptCopy | null = null;
  let restoreCopyKeepCurrent = true;
  let deleteCopyTarget: KeptCopy | null = null;
  let deleteSnapTarget: RestoreSnapshot | null = null;

  function fmtBytes(b: number): string {
    const mb = 1024 * 1024;
    const gb = 1024 * mb;
    if (b >= gb) return `${(b / gb).toFixed(1)} GB`;
    if (b >= mb) return `${Math.max(1, Math.round(b / mb))} MB`;
    if (b >= 1024) return `${Math.max(1, Math.round(b / 1024))} KB`;
    return `${b} B`;
  }

  function isoDate30DaysFromNow(): string {
    return isoDate(new Date(Date.now() + 30 * 24 * 60 * 60 * 1000));
  }

  function entryKey(e: HistoryEntry, idx: number): string {
    return `${idx}:${e.at}:${e.action}`;
  }

  // Pair each snapshot 1-to-1 with at most one History entry so two back-to-back
  // updates never claim the same snapshot.
  function matchSnapshotsToEntries(list: HistoryEntry[], snaps: RestoreSnapshot[]): Map<string, RestoreSnapshot> {
    const out = new Map<string, RestoreSnapshot>();
    if (list.length === 0 || snaps.length === 0) return out;

    type Candidate = { key: string; snap: RestoreSnapshot; diff: number };
    const candidates: Candidate[] = [];
    list.forEach((e, idx) => {
      if (e.result !== 'ok') return;
      const isUpdate = e.action === 'update';
      const isManualEntry = e.action === 'snapshot' || (e.action === 'apply' && (e.message ?? '').startsWith('Saved restore point'));
      if (!isUpdate && !isManualEntry) return;
      const t = Date.parse(e.at);
      if (Number.isNaN(t)) return;
      const maxWindow = isManualEntry ? 60 * 1000 : 20 * 60 * 1000;
      for (const s of snaps) {
        if (isManualEntry && s.kind !== 'manual') continue;
        if (isUpdate && s.kind !== 'update') continue;
        const st = Date.parse(s.createdAt);
        if (Number.isNaN(st)) continue;
        // Pre-update snapshots are taken before the update finishes (allow 5s clock/write jitter).
        if (isUpdate && st > t + 5000) continue;
        const diff = Math.abs(t - st);
        if (diff <= maxWindow) {
          candidates.push({ key: entryKey(e, idx), snap: s, diff });
        }
      }
    });

    candidates.sort((a, b) => a.diff - b.diff);
    const usedSnaps = new Set<string>();
    for (const c of candidates) {
      if (out.has(c.key) || usedSnaps.has(c.snap.name)) continue;
      out.set(c.key, c.snap);
      usedSnaps.add(c.snap.name);
    }
    return out;
  }

  function keptCopyForEntry(e: HistoryEntry, copies: KeptCopy[]): KeptCopy | null {
    if (e.action !== 'rollback' || copies.length === 0) return null;
    const msg = e.message ?? '';
    // Direct match by kept dataset basename in the history message if present.
    for (const c of copies) {
      const base = c.dataset.split('/').pop() ?? '';
      if (base && msg.includes(base)) return c;
    }
    const t = Date.parse(e.at);
    if (Number.isNaN(t)) return null;
    let best: KeptCopy | null = null;
    let bestDiff = 2 * 60 * 1000;
    for (const c of copies) {
      const ct = Date.parse(c.createdAt);
      if (Number.isNaN(ct)) continue;
      const diff = Math.abs(t - ct);
      if (diff < bestDiff) {
        best = c;
        bestDiff = diff;
      }
    }
    return best;
  }

  async function load(_stack: string, _reload: number) {
    const url = stack
      ? `/api/stacks/${encodeURIComponent(stack)}/history?limit=${limit}`
      : `/api/history?limit=${limit}`;
    try {
      const reqs: Promise<Response>[] = [fetch(url)];
      if (stack) {
        reqs.push(fetch(`/api/stacks/${encodeURIComponent(stack)}/restore-points`));
      }
      const [hr, rr] = await Promise.all(reqs);
      if (!hr.ok) throw new Error(String(hr.status));
      entries = await hr.json();
      if (rr && rr.ok) {
        rp = await rr.json();
        if (rp && rp.hasZfs && !rp.dataset) {
          const [mr, sr] = await Promise.all([
            fetch(`/api/stacks/${encodeURIComponent(stack)}/migrate-dataset`),
            fetch('/api/settings/storage/datasets'),
          ]);
          migratePlan = mr.ok ? await mr.json() : null;
          if (sr.ok) {
            const sd = await sr.json();
            zfsLocations = (sd?.locations ?? []).filter((l: any) => l?.isDataset && l?.path).map((l: any) => l.path);
          } else {
            zfsLocations = [];
          }
        } else {
          migratePlan = null;
          zfsLocations = [];
        }
      } else {
        rp = null;
        migratePlan = null;
        zfsLocations = [];
      }
      failedToLoad = false;
    } catch {
      failedToLoad = true;
    }
    loading = false;
  }

  $: load(stack, reload);
  onMount(() => {
    const t = setInterval(() => load(stack, reload), 30000);
    return () => clearInterval(t);
  });

  $: snapshots = rp?.snapshots ?? [];
  $: entrySnapMap = stack ? matchSnapshotsToEntries(entries, snapshots) : new Map<string, RestoreSnapshot>();
  $: keptCopies = rp?.keptCopies ?? [];
  $: latestKept = keptCopies.length > 0 ? keptCopies[0] : null;
  $: totalPointCount = snapshots.length + keptCopies.length;
  $: totalPointBytes =
    snapshots.reduce((sum, s) => sum + (s.usedBytes || 0), 0) +
    keptCopies.reduce((sum, c) => sum + (c.usedBytes || 0), 0);
  $: isPlainOnZfs = !!(stack && rp && rp.hasZfs && !rp.dataset);
  $: notCovered = notCoveredParts(rp?.notCovered);
  $: newerSnapsForModal = restoreSnap
    ? snapshots.filter((s) => Date.parse(s.createdAt) > Date.parse(restoreSnap!.createdAt))
    : [];

  // Unified restore points list for the "Restore points" filter, sorted newest first.
  $: unifiedPoints = [
    ...keptCopies.map((c) => ({
      id: 'kept:' + c.dataset,
      createdAt: c.createdAt,
      kind: 'kept' as const,
      copy: c,
      snap: null as RestoreSnapshot | null,
    })),
    ...snapshots.map((s) => ({
      id: 'snap:' + s.name,
      createdAt: s.createdAt,
      kind: 'snap' as const,
      copy: null as KeptCopy | null,
      snap: s,
    })),
  ].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt));

  function openRestoreSnapshot(s: RestoreSnapshot) {
    openMenuFor = '';
    actionError = '';
    restoreSnap = s;
    restoreMode = s.kind === 'manual' ? 'both' : '';
    restoreKeepCopy = true;
  }

  function openRestoreKept(c: KeptCopy) {
    openMenuFor = '';
    actionError = '';
    restoreCopyTarget = c;
    restoreCopyKeepCurrent = true;
  }

  function closeModals() {
    if (busy) return;
    saveModalOpen = false;
    resumeModalOpen = false;
    migrateModalOpen = false;
    restoreSnap = null;
    restoreCopyTarget = null;
    deleteCopyTarget = null;
    deleteSnapTarget = null;
    actionError = '';
  }

  async function confirmMigrateDataset() {
    if (!stack || busy) return;
    busy = true;
    actionError = '';
    try {
      const r = await fetch(`/api/stacks/${encodeURIComponent(stack)}/migrate-dataset`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ keepOld: migrateKeepOld }),
      });
      if (!r.ok) {
        actionError = (await r.text()) || `Migration failed (${r.status})`;
        return;
      }
      const data = await r.json().catch(() => null);
      migrateModalOpen = false;
      const ds = data?.dataset || migratePlan?.targetDataset || 'its own ZFS dataset';
      statusNote = data?.backupDir
        ? `Moved to ${ds}. Restore points are now on. Original folder kept at ${data.backupDir}.`
        : `Moved to ${ds}. Restore points are now on.`;
      await load(stack, reload + 1);
      dispatch('changed');
    } catch (err) {
      actionError = err instanceof Error ? err.message : 'Migration failed';
    } finally {
      busy = false;
    }
  }

  async function confirmSavePoint() {
    if (!stack || busy) return;
    busy = true;
    actionError = '';
    try {
      const r = await fetch(`/api/stacks/${encodeURIComponent(stack)}/snapshots`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ note: saveNote.trim() }),
      });
      if (!r.ok) {
        actionError = (await r.text()) || `Snapshot failed (${r.status})`;
        return;
      }
      saveModalOpen = false;
      saveNote = '';
      statusNote = 'Restore point saved.';
      await load(stack, reload + 1);
      dispatch('changed');
    } catch (err) {
      actionError = err instanceof Error ? err.message : 'Snapshot failed';
    } finally {
      busy = false;
    }
  }

  async function confirmDeleteSnap() {
    if (!stack || !deleteSnapTarget || busy) return;
    busy = true;
    actionError = '';
    try {
      const r = await fetch(
        `/api/stacks/${encodeURIComponent(stack)}/snapshots?snapshot=${encodeURIComponent(deleteSnapTarget.name)}`,
        { method: 'DELETE' }
      );
      if (!r.ok) {
        actionError = (await r.text()) || `Delete failed (${r.status})`;
        return;
      }
      deleteSnapTarget = null;
      statusNote = 'Restore point deleted.';
      await load(stack, reload + 1);
      dispatch('changed');
    } finally {
      busy = false;
    }
  }

  async function confirmRestoreSnapshot() {
    if (!stack || !restoreSnap || !restoreMode || busy) return;
    busy = true;
    actionError = '';
    try {
      const r = await fetch(`/api/stacks/${encodeURIComponent(stack)}/restore`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          mode: restoreMode,
          snapshot: restoreSnap.name,
          keepCopy: restoreKeepCopy,
        }),
      });
      if (!r.ok) {
        actionError = (await r.text()) || `Restore failed (${r.status})`;
        return;
      }
      restoreSnap = null;
      statusNote = 'Restore completed.';
      await load(stack, reload + 1);
      dispatch('changed');
    } catch (err) {
      actionError = err instanceof Error ? err.message : 'Restore failed';
    } finally {
      busy = false;
    }
  }

  async function confirmRestoreKeptCopy() {
    if (!stack || !restoreCopyTarget || busy) return;
    busy = true;
    actionError = '';
    try {
      const r = await fetch(`/api/stacks/${encodeURIComponent(stack)}/kept/restore`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          dataset: restoreCopyTarget.dataset,
          keepCurrent: restoreCopyKeepCurrent,
        }),
      });
      if (!r.ok) {
        actionError = (await r.text()) || `Restore failed (${r.status})`;
        return;
      }
      restoreCopyTarget = null;
      statusNote = 'Restore completed. Automatic updates stay paused.';
      await load(stack, reload + 1);
      dispatch('changed');
    } catch (err) {
      actionError = err instanceof Error ? err.message : 'Restore failed';
    } finally {
      busy = false;
    }
  }

  async function toggleForever(copy: KeptCopy) {
    if (!stack || busy) return;
    openMenuFor = '';
    busy = true;
    try {
      const forever = !!copy.until;
      const r = await fetch(`/api/stacks/${encodeURIComponent(stack)}/kept`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ dataset: copy.dataset, forever }),
      });
      if (r.ok) {
        await load(stack, reload + 1);
      }
    } finally {
      busy = false;
    }
  }

  async function confirmDeleteCopy() {
    if (!stack || !deleteCopyTarget || busy) return;
    busy = true;
    actionError = '';
    try {
      const r = await fetch(
        `/api/stacks/${encodeURIComponent(stack)}/kept?dataset=${encodeURIComponent(deleteCopyTarget.dataset)}`,
        { method: 'DELETE' }
      );
      if (!r.ok) {
        actionError = (await r.text()) || `Delete failed (${r.status})`;
        return;
      }
      deleteCopyTarget = null;
      statusNote = 'Restore point deleted.';
      await load(stack, reload + 1);
      dispatch('changed');
    } finally {
      busy = false;
    }
  }

  async function doResumeUpdates(skipRef: string) {
    if (!stack || !rp?.pinnedServices?.length || busy) return;
    busy = true;
    try {
      for (const svc of rp.pinnedServices) {
        await fetch(`/api/stacks/${encodeURIComponent(stack)}/unpin`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ service: svc, hold: skipRef }),
        });
      }
      resumeModalOpen = false;
      statusNote = skipRef
        ? 'Automatic updates resumed (skipping the version you rolled back from).'
        : 'Automatic updates resumed.';
      await load(stack, reload + 1);
      dispatch('changed');
    } finally {
      busy = false;
    }
  }

  function onResumeClick() {
    if (latestKept?.version || latestKept?.image) {
      resumeModalOpen = true;
    } else {
      doResumeUpdates('');
    }
  }

  function copyPath(p: string) {
    if (navigator.clipboard) navigator.clipboard.writeText(p).catch(() => {});
    statusNote = 'Path copied.';
  }
</script>

{#if loading}
  <p class="text-sm text-fjord-fg-muted">Loading...</p>
{:else if failedToLoad}
  <p class="text-sm text-fjord-fg-muted">History could not be read.</p>
{:else}
<div class={stack ? 'max-w-4xl' : ''}>
  {#if statusNote}
    <p class="mb-3 text-xs text-fjord-fg-muted" data-restore-status>{statusNote}</p>
  {/if}

  {#if stack && rp && (rp.pinned || isPlainOnZfs)}
    <div class="mb-4 flex flex-col gap-2.5" data-active-strip>
      {#if rp.pinned}
        <div class="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3.5 py-2.5 text-sm">
          <span>
            Pinned{rp.currentVersion ? ` to ${rp.currentVersion}` : ''}. Automatic updates are paused.{#if latestKept} Your {latestKept.version ? `${latestKept.version} ` : ''}data is {latestKept.until ? `kept until ${isoDate(latestKept.until)}` : 'kept forever'}.{/if}
          </span>
          <button
            class="rounded-md border border-fjord-border bg-fjord-card px-2.5 py-1 text-xs font-medium hover:bg-fjord-border"
            disabled={busy}
            on:click={onResumeClick}
          >Resume updates</button>
        </div>
      {/if}

      {#if isPlainOnZfs}
        <div class="flex flex-wrap items-start justify-between gap-3 rounded-lg border border-fjord-accent/40 bg-fjord-accent/10 px-3.5 py-3 text-sm">
          <div class="min-w-0 flex-1 space-y-1.5">
            <div class="font-medium text-fjord-fg">
              Restore points are off for <strong>{stack}</strong>
            </div>
            {#if migratePlan?.eligible}
              {#if migratePlan.crossLocation}
                <p class="text-xs text-fjord-fg-muted">
                  Its data is in <span class="font-mono">{migratePlan.sourceDir}</span> on the OS root filesystem. Move it to a dedicated ZFS dataset at <span class="font-mono">{migratePlan.targetDir}</span> (<span class="font-mono">{migratePlan.targetDataset}</span>) to enable restore points with zero data loss.
                </p>
              {:else}
                <p class="text-xs text-fjord-fg-muted">
                  Its data is in a plain folder{#if migratePlan.sourceDir} (<span class="font-mono">{migratePlan.sourceDir}</span>){/if} instead of its own ZFS dataset. Move it into <span class="font-mono">{migratePlan.targetDataset}</span> to enable restore points.
                </p>
              {/if}
            {:else if migratePlan?.zfsCreateHint}
              <p class="text-xs text-fjord-fg-muted">
                Its data is in <span class="font-mono">{migratePlan.sourceDir || 'a plain folder'}</span>, which sits on the OS root filesystem. fjord only snapshots dedicated app datasets so a restore never touches the OS.
              </p>
              {#if zfsLocations.length > 0}
                <p class="text-xs text-fjord-fg-muted">
                  This host already has a ZFS App data location (<span class="font-mono">{zfsLocations[0]}</span>). New apps installed there get restore points automatically.
                </p>
              {:else}
                <ol class="list-decimal list-inside space-y-1.5 text-xs text-fjord-fg-muted pt-0.5">
                  <li>
                    Create a dedicated ZFS dataset on the host:
                    <span class="mt-1 flex flex-wrap items-center gap-2">
                      <code class="rounded border border-fjord-border bg-fjord-bg px-2 py-0.5 font-mono text-fjord-fg">{migratePlan.zfsCreateHint}</code>
                      <button type="button" class="text-fjord-accent hover:underline" on:click={() => copyPath(migratePlan?.zfsCreateHint || '')}>Copy</button>
                    </span>
                  </li>
                  <li>
                    Add <span class="font-mono">{zfsHintMountpoint(migratePlan.zfsCreateHint)}</span> in <a href="#/settings/storage" class="text-fjord-accent hover:underline">Settings → Storage</a> and move it to the top so new apps get their own dataset automatically.
                  </li>
                </ol>
                <div class="flex flex-wrap items-center gap-3 pt-1 text-xs text-fjord-fg-muted">
                  <span>Once added, click Check again and fjord will walk you through moving <strong>{stack}</strong> with zero data loss.</span>
                  <button
                    type="button"
                    class="shrink-0 rounded-md border border-fjord-border bg-fjord-card px-2.5 py-1 text-xs font-medium text-fjord-fg hover:bg-fjord-border"
                    on:click={() => load(stack, reload + 1)}
                  >Check again</button>
                </div>
              {/if}
            {:else if migratePlan && migratePlan.reason}
              <p class="text-xs text-fjord-fg-muted">{migratePlan.reason}.</p>
            {:else}
              <p class="text-xs text-fjord-fg-muted">
                Uses a plain folder instead of its own ZFS dataset. Check <a href="#/settings/storage" class="text-fjord-accent hover:underline">Settings → Storage</a> to configure a ZFS App data location.
              </p>
            {/if}
          </div>
          {#if migratePlan?.eligible}
            <button
              class="shrink-0 rounded-md bg-fjord-accent px-3 py-1.5 text-xs font-semibold text-white hover:opacity-90 disabled:opacity-50"
              disabled={busy}
              on:click={() => {
                actionError = '';
                migrateKeepOld = true;
                migrateModalOpen = true;
              }}
            >Move to dataset…</button>
          {/if}
        </div>
      {/if}
    </div>
  {/if}

  {#if stack && rp?.hasZfs}
    <div class="mb-3 flex flex-wrap items-center justify-between gap-2 border-b border-fjord-border pb-3 text-xs">
      <div class="inline-flex rounded-lg border border-fjord-border bg-fjord-inset p-0.5">
        <button
          class="rounded-md px-2.5 py-1 {filter === 'all' ? 'bg-fjord-card font-semibold text-fjord-fg' : 'text-fjord-fg-muted hover:text-fjord-fg'}"
          on:click={() => (filter = 'all')}
        >All</button>
        <button
          class="rounded-md px-2.5 py-1 disabled:opacity-40 {filter === 'restore' ? 'bg-fjord-card font-semibold text-fjord-fg' : 'text-fjord-fg-muted hover:text-fjord-fg'}"
          disabled={!rp.dataset}
          on:click={() => (filter = 'restore')}
        >Restore points ({rp.dataset ? totalPointCount : 0}{rp.dataset && totalPointBytes ? ` · ${fmtBytes(totalPointBytes)}` : ''})</button>
      </div>
      <button
        class="rounded-md border border-fjord-border px-2.5 py-1 text-xs font-medium hover:bg-fjord-border disabled:opacity-40"
        disabled={busy || !rp.dataset}
        on:click={() => {
          actionError = '';
          saveNote = '';
          saveModalOpen = true;
        }}
      >+ Save restore point</button>
    </div>
  {/if}

  {#if filter === 'restore' && stack && rp?.dataset}
    {#if unifiedPoints.length === 0}
      <p class="text-sm text-fjord-fg-muted">No saved restore points yet.</p>
    {:else}
      <ul class="rounded-xl border border-fjord-border bg-fjord-card divide-y divide-fjord-border text-sm" data-restore-points>
        {#each unifiedPoints as item (item.id)}
          {#if item.copy}
            {@const c = item.copy}
            <li class="px-4 py-3 flex items-center justify-between gap-4">
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2 text-fjord-fg font-medium">
                  <span>{c.version ? `${c.version} saved before restore` : 'Saved before restore'}</span>
                  <span class="rounded-full border border-fjord-border px-2 py-0.5 text-[11px] font-medium text-fjord-fg-muted">Before restore</span>
                </div>
                <div class="text-xs text-fjord-fg-muted mt-0.5 tabular-nums">
                  <span title={isoDateTime(c.createdAt, true)}>{whenLabel(c.createdAt)}</span>
                  · {fmtBytes(c.usedBytes)}
                  · {c.until ? `Kept until ${isoDate(c.until)}` : 'Kept forever'}
                </div>
                {#if expandedPaths[c.dataset] && c.mountpoint}
                  <div class="mt-2 flex flex-wrap items-center justify-between gap-2 rounded-md border border-fjord-border bg-fjord-bg px-2.5 py-1.5 text-xs">
                    <code class="font-mono text-fjord-fg-muted break-all">{c.mountpoint}</code>
                    <button class="rounded border border-fjord-border px-2 py-0.5 text-xs hover:bg-fjord-border" on:click={() => copyPath(c.mountpoint)}>Copy path</button>
                  </div>
                {/if}
              </div>
              <div class="flex items-center gap-1.5 shrink-0 relative">
                {#if c.kind === 'rollback'}
                  <button
                    class="rounded-md border border-fjord-border px-2.5 py-1 text-xs font-medium hover:bg-fjord-border"
                    disabled={busy}
                    on:click={() => openRestoreKept(c)}
                  >Restore</button>
                {/if}
                <button
                  class="rounded-md px-2 py-1 text-xs text-fjord-fg-muted hover:bg-fjord-border hover:text-fjord-fg"
                  disabled={busy}
                  aria-label="More options"
                  on:click={() => (openMenuFor = openMenuFor === item.id ? '' : item.id)}
                >⋯</button>
                {#if openMenuFor === item.id}
                  <div class="absolute right-0 top-full mt-1 z-20 min-w-44 rounded-lg border border-fjord-border bg-fjord-card p-1 shadow-xl flex flex-col text-xs">
                    <button class="rounded px-2.5 py-1.5 text-left hover:bg-fjord-border" on:click={() => toggleForever(c)}>
                      {c.until ? 'Keep forever' : `Expire on ${isoDate30DaysFromNow()}`}
                    </button>
                    {#if c.mountpoint && c.mountpoint !== 'none'}
                      <button
                        class="rounded px-2.5 py-1.5 text-left hover:bg-fjord-border"
                        on:click={() => {
                          openMenuFor = '';
                          copyPath(c.mountpoint);
                        }}
                      >Copy host path</button>
                    {/if}
                    <button
                      class="rounded px-2.5 py-1.5 text-left text-fjord-danger hover:bg-fjord-border"
                      on:click={() => {
                        openMenuFor = '';
                        actionError = '';
                        deleteCopyTarget = c;
                      }}
                    >Delete ({fmtBytes(c.usedBytes)})</button>
                  </div>
                {/if}
              </div>
            </li>
          {:else if item.snap}
            {@const s = item.snap}
            <li class="px-4 py-3 flex items-center justify-between gap-4">
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2 text-fjord-fg font-medium">
                  <span>{snapTitle(s)}</span>
                  <span class="rounded-full px-2 py-0.5 text-[11px] font-medium border {s.kind === 'manual' ? 'border-fjord-accent/40 bg-fjord-accent/10 text-fjord-accent' : 'border-fjord-border text-fjord-fg-muted'}">{snapBadge(s)}</span>
                </div>
                <div class="text-xs text-fjord-fg-muted mt-0.5 tabular-nums">
                  <span title={isoDateTime(s.createdAt, true)}>{whenLabel(s.createdAt)}</span>
                  · {fmtBytes(s.usedBytes)}{s.kind === 'manual' ? ' · Kept forever' : ''}
                </div>
                {#if expandedPaths[s.name] && s.hostPath}
                  <div class="mt-2 flex flex-wrap items-center justify-between gap-2 rounded-md border border-fjord-border bg-fjord-bg px-2.5 py-1.5 text-xs">
                    <code class="font-mono text-fjord-fg-muted break-all">{s.hostPath}</code>
                    <button class="rounded border border-fjord-border px-2 py-0.5 text-xs hover:bg-fjord-border" on:click={() => copyPath(s.hostPath || '')}>Copy path</button>
                  </div>
                {/if}
              </div>
              <div class="flex items-center gap-1.5 shrink-0 relative">
                <button
                  class="rounded-md border border-fjord-border px-2.5 py-1 text-xs font-medium hover:bg-fjord-border"
                  disabled={busy}
                  on:click={() => openRestoreSnapshot(s)}
                >Restore</button>
                <button
                  class="rounded-md px-2 py-1 text-xs text-fjord-fg-muted hover:bg-fjord-border hover:text-fjord-fg"
                  disabled={busy}
                  aria-label="More options"
                  on:click={() => (openMenuFor = openMenuFor === item.id ? '' : item.id)}
                >⋯</button>
                {#if openMenuFor === item.id}
                  <div class="absolute right-0 top-full mt-1 z-20 min-w-44 rounded-lg border border-fjord-border bg-fjord-card p-1 shadow-xl flex flex-col text-xs">
                    {#if s.hostPath}
                      <button
                        class="rounded px-2.5 py-1.5 text-left hover:bg-fjord-border"
                        on:click={() => {
                          openMenuFor = '';
                          copyPath(s.hostPath || '');
                        }}
                      >Copy host path</button>
                    {/if}
                    <button
                      class="rounded px-2.5 py-1.5 text-left text-fjord-danger hover:bg-fjord-border"
                      on:click={() => {
                        openMenuFor = '';
                        actionError = '';
                        deleteSnapTarget = s;
                      }}
                    >Delete ({fmtBytes(s.usedBytes)})</button>
                  </div>
                {/if}
              </div>
            </li>
          {/if}
        {/each}
      </ul>
    {/if}
    <p class="mt-3 text-xs text-fjord-fg-dim" data-retention-note>
      Automatic restore points are kept for {rp.retentionDays ?? 30} days. Manual points stay until deleted.
      <a href="#/settings/maintenance" class="ml-1 text-fjord-accent hover:underline">Change in Settings</a>
    </p>
  {:else if entries.length === 0}
    <p class="text-sm text-fjord-fg-muted" data-history-empty>Nothing has happened here yet.</p>
  {:else}
    <ul class="divide-y divide-fjord-border text-sm" data-history>
      {#each entries as e, idx (e.at + e.stack + e.action + idx)}
        {@const matchedSnap = stack && rp?.dataset ? (entrySnapMap.get(entryKey(e, idx)) ?? null) : null}
        {@const matchedCopy = stack && rp?.dataset ? keptCopyForEntry(e, keptCopies) : null}
        {@const menuKey = 'row:' + e.at + e.action}
        {@const changes = changeLines(e)}
        <li class="py-2 flex items-start justify-between gap-4" data-history-entry data-action={e.action} data-result={e.result}>
          <div class="flex gap-4 min-w-0 flex-1">
            <span class="w-32 shrink-0 text-fjord-fg-muted tabular-nums text-xs pt-0.5" title={isoDateTime(e.at, true)}>{whenLabel(e.at)}</span>
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
                <span class={e.result === 'failed' ? 'text-fjord-danger font-medium' : 'text-fjord-fg font-medium'}>
                  {#if !stack}<span class="font-semibold">{e.stack}</span>{': '}{/if}{historyTitle(e)}
                </span>
                {#if changes.length === 1}
                  <span class="text-xs text-fjord-fg-muted font-mono">{changes[0]}</span>
                {/if}
                {#if e.message && !matchedCopy}
                  <span class="text-xs text-fjord-fg-muted break-words">{e.message}</span>
                {/if}
              </div>
              {#if changes.length > 1}
                {#each changes as line}
                  <div class="text-xs text-fjord-fg-muted font-mono">{line}</div>
                {/each}
              {/if}
              {#if matchedCopy}
                <div class="mt-0.5 text-xs text-fjord-fg-muted">
                  {matchedCopy.version ? `${matchedCopy.version} saved before restore` : 'Saved before restore'} · {fmtBytes(matchedCopy.usedBytes)} · {matchedCopy.until ? `until ${isoDate(matchedCopy.until)}` : 'kept forever'}
                </div>
              {/if}
            </div>
          </div>

          {#if matchedSnap}
            <div class="flex items-center gap-2 shrink-0">
              <span class="text-xs text-fjord-fg-muted tabular-nums">{fmtBytes(matchedSnap.usedBytes)}</span>
              <button
                class="rounded-md border border-fjord-border px-2.5 py-1 text-xs font-medium hover:bg-fjord-border"
                disabled={busy}
                on:click={() => openRestoreSnapshot(matchedSnap)}
              >{restoreButtonLabel(e)}</button>
            </div>
          {:else if matchedCopy}
            <div class="flex items-center gap-1.5 shrink-0 relative">
              {#if matchedCopy.kind === 'rollback'}
                <button
                  class="rounded-md border border-fjord-border px-2.5 py-1 text-xs font-medium hover:bg-fjord-border"
                  disabled={busy}
                  on:click={() => openRestoreKept(matchedCopy)}
                >Restore</button>
              {/if}
              <button
                class="rounded-md px-2 py-1 text-xs text-fjord-fg-muted hover:bg-fjord-border hover:text-fjord-fg"
                disabled={busy}
                aria-label="More options"
                on:click={() => (openMenuFor = openMenuFor === menuKey ? '' : menuKey)}
              >⋯</button>
              {#if openMenuFor === menuKey}
                <div class="absolute right-0 top-full mt-1 z-20 min-w-44 rounded-lg border border-fjord-border bg-fjord-card p-1 shadow-xl flex flex-col text-xs">
                  <button class="rounded px-2.5 py-1.5 text-left hover:bg-fjord-border" on:click={() => toggleForever(matchedCopy)}>
                    {matchedCopy.until ? 'Keep forever' : `Expire on ${isoDate30DaysFromNow()}`}
                  </button>
                  <button
                    class="rounded px-2.5 py-1.5 text-left text-fjord-danger hover:bg-fjord-border"
                    on:click={() => {
                      openMenuFor = '';
                      actionError = '';
                      deleteCopyTarget = matchedCopy;
                    }}
                  >Delete ({fmtBytes(matchedCopy.usedBytes)})</button>
                </div>
              {/if}
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</div>
{/if}

{#if migrateModalOpen && migratePlan}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" role="dialog" aria-modal="true">
    <div class="w-full max-w-lg rounded-xl border border-fjord-border bg-fjord-card p-5 shadow-xl flex flex-col gap-3.5 text-sm">
      <h2 class="text-base font-semibold text-fjord-fg">Move {stack} to its own dataset?</h2>
      <div class="rounded-lg border border-fjord-border bg-fjord-bg px-3 py-2.5 text-xs space-y-1.5">
        <div class="flex items-baseline justify-between gap-2">
          <span class="text-[10px] uppercase tracking-wide text-fjord-fg-muted shrink-0">From</span>
          <code class="font-mono text-fjord-fg truncate">{migratePlan.sourceDir}</code>
        </div>
        <div class="flex items-baseline justify-between gap-2">
          <span class="text-[10px] uppercase tracking-wide text-fjord-fg-muted shrink-0">To</span>
          <span class="min-w-0 text-right">
            <code class="font-mono text-fjord-fg">{migratePlan.targetDir || migratePlan.sourceDir}</code>
            <span class="block text-[11px] font-mono text-fjord-fg-dim">{migratePlan.targetDataset}</span>
          </span>
        </div>
      </div>
      <div class="grid grid-cols-2 gap-3 rounded-lg border border-fjord-border bg-fjord-bg px-3 py-2.5 text-xs">
        <div>
          <div class="text-[10px] uppercase tracking-wide text-fjord-fg-muted">Estimated downtime</div>
          <div class="mt-0.5 font-medium text-fjord-fg">~{migratePlan.estSeconds}s ({fmtBytes(migratePlan.sizeBytes)})</div>
        </div>
        <div>
          <div class="text-[10px] uppercase tracking-wide text-fjord-fg-muted">Space needed</div>
          <div class="mt-0.5 font-medium text-fjord-fg">{fmtBytes(migratePlan.neededBytes)} ({fmtBytes(migratePlan.freeBytes)} free)</div>
        </div>
      </div>
      <div class="rounded-lg border border-fjord-border bg-fjord-bg/60 px-3 py-2.5 text-xs text-fjord-fg-muted space-y-1">
        <div class="font-medium text-fjord-fg">How fjord keeps your data safe</div>
        <ol class="list-decimal list-inside space-y-1">
          <li>Stops <strong>{stack}</strong> cleanly so no files change during the copy.</li>
          <li>
            Creates <code class="font-mono text-fjord-fg">{migratePlan.targetDataset}</code>, copies {fmtBytes(migratePlan.sizeBytes)} with permissions preserved{#if migratePlan.crossLocation}, updates volume paths to <code class="font-mono text-fjord-fg">{migratePlan.targetDir}</code>,{/if} and saves an initial restore point.
          </li>
          <li>Starts <strong>{stack}</strong> automatically. If anything fails, fjord reverts to <code class="font-mono text-fjord-fg">{migratePlan.sourceDir}</code> untouched.</li>
        </ol>
      </div>
      <label class="flex items-start gap-2 text-xs text-fjord-fg cursor-pointer">
        <input type="checkbox" bind:checked={migrateKeepOld} class="mt-0.5" />
        <span>
          Keep the original folder as a backup copy
          <span class="block text-fjord-fg-muted">Renamed beside the original (<code class="font-mono">{migratePlan.sourceDir}.pre-dataset-…</code>); uses ~{fmtBytes(migratePlan.sizeBytes * 2)} total until you remove it.</span>
        </span>
      </label>
      {#if actionError}
        <p class="text-xs text-fjord-danger">{actionError}</p>
      {/if}
      <div class="flex justify-end gap-2 pt-1">
        <button class="rounded-lg px-3 py-1.5 text-xs text-fjord-fg-muted hover:bg-fjord-border" disabled={busy} on:click={closeModals}>Cancel</button>
        <button
          class="rounded-lg bg-fjord-accent px-3 py-1.5 text-xs font-semibold text-white disabled:opacity-50"
          disabled={busy}
          on:click={confirmMigrateDataset}
        >{busy ? 'Moving...' : 'Move to dataset'}</button>
      </div>
    </div>
  </div>
{/if}

{#if resumeModalOpen && latestKept}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" role="dialog" aria-modal="true">
    <div class="w-full max-w-md rounded-xl border border-fjord-border bg-fjord-card p-5 shadow-xl flex flex-col gap-3.5 text-sm">
      <h2 class="text-base font-semibold text-fjord-fg">Resume automatic updates?</h2>
      <p class="text-xs text-fjord-fg-muted">
        You rolled back from {latestKept.version || 'the newer version'}. Resuming all updates will pull it back in on the next update window.
      </p>
      <div class="flex flex-col gap-2">
        <button
          class="rounded-lg bg-fjord-accent px-3 py-2 text-xs font-semibold text-white text-left disabled:opacity-50"
          disabled={busy}
          on:click={() => doResumeUpdates(latestKept?.image || latestKept?.version || '')}
        >Skip {latestKept.version || 'that version'} and wait for the next release</button>
        <button
          class="rounded-lg border border-fjord-border px-3 py-2 text-xs font-medium text-fjord-fg text-left hover:bg-fjord-border disabled:opacity-50"
          disabled={busy}
          on:click={() => doResumeUpdates('')}
        >Resume all updates (includes {latestKept.version || 'the newer version'})</button>
      </div>
      <div class="flex justify-end">
        <button class="rounded-lg px-3 py-1.5 text-xs text-fjord-fg-muted hover:bg-fjord-border" disabled={busy} on:click={closeModals}>Cancel</button>
      </div>
    </div>
  </div>
{/if}

{#if saveModalOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" role="dialog" aria-modal="true">
    <div class="w-full max-w-md rounded-xl border border-fjord-border bg-fjord-card p-5 shadow-xl flex flex-col gap-3.5 text-sm">
      <h2 class="text-base font-semibold text-fjord-fg">Save restore point</h2>
      <p class="text-xs text-fjord-fg-muted">
        Saves a snapshot of {stack}'s data right now. The app keeps running with no interruption.
      </p>
      <div class="flex flex-col gap-1.5 text-xs">
        <div class="flex items-center justify-between gap-2">
          <label for="rp-save-note" class="text-fjord-fg-muted">Note (optional)</label>
          <button
            type="button"
            on:click={() => {
              const stamp = isoDate(new Date().toISOString());
              const cur = saveNote.trim();
              if (!cur) saveNote = stamp;
              else if (!cur.includes(stamp)) saveNote = `${cur} (${stamp})`;
            }}
            title="Insert today's date"
            class="inline-flex items-center gap-1 rounded-md border border-fjord-border bg-fjord-bg px-2 py-0.5 text-[11px] font-medium text-fjord-fg-muted hover:text-fjord-fg hover:border-fjord-accent/50 transition-colors"
          >+ Date</button>
        </div>
        <input
          id="rp-save-note"
          type="text"
          bind:value={saveNote}
          maxlength={120}
          placeholder="e.g. Before changing settings"
          on:keydown={(e) => {
            if (e.key === 'Enter' && !busy) confirmSavePoint();
          }}
          class="rounded-lg border border-fjord-border bg-fjord-bg px-3 py-2 text-sm text-fjord-fg focus:border-fjord-accent focus:outline-none"
        />
      </div>
      {#if actionError}
        <p class="text-xs text-fjord-danger">{actionError}</p>
      {/if}
      <div class="flex justify-end gap-2 pt-1">
        <button class="rounded-lg px-3 py-1.5 text-xs text-fjord-fg-muted hover:bg-fjord-border" disabled={busy} on:click={closeModals}>Cancel</button>
        <button
          class="rounded-lg bg-fjord-accent px-3 py-1.5 text-xs font-semibold text-white disabled:opacity-50"
          disabled={busy}
          on:click={confirmSavePoint}
        >{busy ? 'Saving...' : 'Save restore point'}</button>
      </div>
    </div>
  </div>
{/if}

{#if deleteSnapTarget}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" role="dialog" aria-modal="true">
    <div class="w-full max-w-md rounded-xl border border-fjord-border bg-fjord-card p-5 shadow-xl flex flex-col gap-3.5 text-sm">
      <h2 class="text-base font-semibold text-fjord-fg">Delete restore point?</h2>
      <p class="text-xs text-fjord-fg-muted">
        Permanently removes this restore point from {whenLabel(deleteSnapTarget.createdAt)} ({fmtBytes(deleteSnapTarget.usedBytes)}). Running data is not affected.
      </p>
      {#if actionError}
        <p class="text-xs text-fjord-danger">{actionError}</p>
      {/if}
      <div class="flex justify-end gap-2 pt-1">
        <button class="rounded-lg px-3 py-1.5 text-xs text-fjord-fg-muted hover:bg-fjord-border" disabled={busy} on:click={closeModals}>Cancel</button>
        <button
          class="rounded-lg bg-fjord-danger px-3 py-1.5 text-xs font-semibold text-white disabled:opacity-50"
          disabled={busy}
          on:click={confirmDeleteSnap}
        >{busy ? 'Deleting...' : `Delete ${fmtBytes(deleteSnapTarget.usedBytes)}`}</button>
      </div>
    </div>
  </div>
{/if}

{#if restoreSnap && rp}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" role="dialog" aria-modal="true">
    <div class="w-full max-w-lg rounded-xl border border-fjord-border bg-fjord-card p-5 shadow-xl flex flex-col gap-3.5 text-sm">
      <h2 class="text-base font-semibold text-fjord-fg">Restore {stack} to {whenLabel(restoreSnap.createdAt)}?</h2>

      <div class="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
        {stack} stops briefly while this runs, and automatic updates pause until you resume them.
      </div>

      {#if restoreSnap.kind !== 'manual'}
        <label class="flex items-start gap-2.5 rounded-lg border p-3 cursor-pointer {restoreMode === 'app' ? 'border-fjord-accent bg-fjord-bg' : 'border-fjord-border bg-fjord-bg/50'}">
          <input type="radio" name="restore-mode" value="app" bind:group={restoreMode} class="mt-0.5" />
          <div class="flex flex-col gap-1">
            <span class="font-medium text-fjord-fg">App only</span>
            <span class="text-xs text-fjord-fg-muted">Restore the previous app version. Keep all current data untouched.</span>
          </div>
        </label>
      {/if}

      <label class="flex items-start gap-2.5 rounded-lg border p-3 cursor-pointer {restoreMode === 'both' ? (restoreKeepCopy ? 'border-fjord-accent bg-fjord-bg' : 'border-fjord-danger bg-fjord-bg') : 'border-fjord-border bg-fjord-bg/50'}">
        <input type="radio" name="restore-mode" value="both" bind:group={restoreMode} class="mt-0.5" />
        <div class="flex flex-col gap-1.5">
          <span class="font-medium text-fjord-fg">App and data</span>
          {#if rp.restoredFolders.length > 0}
            <span class="text-xs text-fjord-fg-muted"><strong>Restored:</strong> {rp.restoredFolders.join(', ')}</span>
          {/if}
          {#if notCovered}
            <span class="text-xs text-fjord-fg-muted" data-not-covered>
              {#if notCovered.extra > 0}
                Not included: {notCovered.shown} and {notCovered.extra} more. These folders stay as they are.
              {:else}
                Not included: {notCovered.shown}. These folders stay as they are.
              {/if}
            </span>
          {/if}
          {#if newerSnapsForModal.length > 0}
            <span class="text-xs text-fjord-fg-muted">
              {restoreKeepCopy
                ? `${newerSnapsForModal.length} newer restore point(s) stay with the saved copy and expire with it.`
                : `${newerSnapsForModal.length} newer restore point(s) will be permanently deleted.`}
            </span>
          {/if}
          <label class="mt-1 flex items-center gap-2 text-xs text-fjord-fg cursor-pointer">
            <input type="checkbox" bind:checked={restoreKeepCopy} />
            <span>Save current data as a restore point until {isoDate30DaysFromNow()}</span>
          </label>
          {#if !restoreKeepCopy}
            <div class="rounded border border-fjord-danger/50 bg-fjord-danger/10 px-2.5 py-1.5 text-xs text-fjord-danger">
              Changes made since {whenLabel(restoreSnap.createdAt)} will be permanently deleted.
            </div>
          {/if}
        </div>
      </label>

      {#if actionError}
        <p class="text-xs text-fjord-danger">{actionError}</p>
      {/if}

      <div class="flex justify-end gap-2 pt-1">
        <button class="rounded-lg px-3 py-1.5 text-xs text-fjord-fg-muted hover:bg-fjord-border" disabled={busy} on:click={closeModals}>Cancel</button>
        <button
          class="rounded-lg px-3 py-1.5 text-xs font-semibold text-white disabled:opacity-50 {restoreMode === 'both' && !restoreKeepCopy ? 'bg-fjord-danger' : 'bg-fjord-accent'}"
          disabled={busy || !restoreMode}
          on:click={confirmRestoreSnapshot}
        >
          {#if busy}
            Restoring...
          {:else if !restoreMode}
            Choose an option
          {:else if restoreMode === 'app'}
            Restore app
          {:else if restoreKeepCopy}
            Restore and save current data
          {:else}
            Restore and delete current data
          {/if}
        </button>
      </div>
    </div>
  </div>
{/if}

{#if restoreCopyTarget}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" role="dialog" aria-modal="true">
    <div class="w-full max-w-md rounded-xl border border-fjord-border bg-fjord-card p-5 shadow-xl flex flex-col gap-3.5 text-sm">
      <h2 class="text-base font-semibold text-fjord-fg">Restore {stack} to {whenLabel(restoreCopyTarget.createdAt)}?</h2>
      <p class="text-xs text-fjord-fg-muted">
        Restores {stack}{restoreCopyTarget.version ? ` to ${restoreCopyTarget.version}` : ''} along with the data saved before your rollback.
      </p>
      <div class="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
        {stack} stops briefly while this runs. Automatic updates stay paused until you resume them.
      </div>
      <label class="flex items-center gap-2 text-xs text-fjord-fg cursor-pointer">
        <input type="checkbox" bind:checked={restoreCopyKeepCurrent} />
        <span>Save current data as a restore point until {isoDate30DaysFromNow()}</span>
      </label>
      {#if !restoreCopyKeepCurrent}
        <div class="rounded border border-fjord-danger/50 bg-fjord-danger/10 px-2.5 py-1.5 text-xs text-fjord-danger">
          Current running data will be permanently deleted.
        </div>
      {/if}
      {#if actionError}
        <p class="text-xs text-fjord-danger">{actionError}</p>
      {/if}
      <div class="flex justify-end gap-2 pt-1">
        <button class="rounded-lg px-3 py-1.5 text-xs text-fjord-fg-muted hover:bg-fjord-border" disabled={busy} on:click={closeModals}>Cancel</button>
        <button
          class="rounded-lg px-3 py-1.5 text-xs font-semibold text-white disabled:opacity-50 {restoreCopyKeepCurrent ? 'bg-fjord-accent' : 'bg-fjord-danger'}"
          disabled={busy}
          on:click={confirmRestoreKeptCopy}
        >{busy ? 'Restoring...' : restoreCopyKeepCurrent ? 'Restore and save current data' : 'Restore and delete current data'}</button>
      </div>
    </div>
  </div>
{/if}

{#if deleteCopyTarget}
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" role="dialog" aria-modal="true">
    <div class="w-full max-w-md rounded-xl border border-fjord-border bg-fjord-card p-5 shadow-xl flex flex-col gap-3.5 text-sm">
      <h2 class="text-base font-semibold text-fjord-fg">Delete restore point?</h2>
      <p class="text-xs text-fjord-fg-muted">
        Permanently removes the data saved before restore on {whenLabel(deleteCopyTarget.createdAt)} ({fmtBytes(deleteCopyTarget.usedBytes)}). Running data is not affected.
      </p>
      {#if actionError}
        <p class="text-xs text-fjord-danger">{actionError}</p>
      {/if}
      <div class="flex justify-end gap-2 pt-1">
        <button class="rounded-lg px-3 py-1.5 text-xs text-fjord-fg-muted hover:bg-fjord-border" disabled={busy} on:click={closeModals}>Cancel</button>
        <button
          class="rounded-lg bg-fjord-danger px-3 py-1.5 text-xs font-semibold text-white disabled:opacity-50"
          disabled={busy}
          on:click={confirmDeleteCopy}
        >{busy ? 'Deleting...' : `Delete ${fmtBytes(deleteCopyTarget.usedBytes)}`}</button>
      </div>
    </div>
  </div>
{/if}
