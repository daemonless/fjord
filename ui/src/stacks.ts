import { statusKey } from './staleNote';
import { health } from './stackHealth';
import { networkLabel, HOST_NETWORK, DEFAULT_NETWORK } from './network';
import type { StackRestart } from './restartPolicy';

export type ContainerStatus = {
  name: string;
  service?: string; // the compose service it runs
  state: string;
  ports?: { hostPort: number; containerPort: number; protocol?: string }[];
  detail?: string; // one-line reason for a non-running state (appjail: crash-looping app)
  address?: string; // the container's own IP on an attachable network
};

export type StackStatus = {
  state: string;
  containers: ContainerStatus[];
  staleSince?: string;
};

export type StackState = {
  group?: string;
  desired_state?: string;
  engine?: string;
  order?: number;
  origin?: { type?: string; app_id?: string };
  choices?: Record<string, string>;
  pending_services?: string[];
  last_failure?: { action: string; at: string; message: string };
};

export type Stack = {
  name: string;
  displayName?: string;
  icon?: string;
  dir: string;
  compose: string;
  env: string;
  director?: string;
  makejail?: string;
  engine?: string;
  status?: StackStatus;
  state?: StackState;
  services?: any[];
  composeHash?: string;
  busy?: string;
  linkHost?: string;
  restart?: StackRestart;
  listOnly?: boolean;
};

// Fleet-wide update state (server-side cached; refreshed in the background).
export type ServiceUpdate = {
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

export type UpdateInfo = {
  perService?: boolean;
  pinServices?: boolean;
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

export type StackFilter = 'all' | 'running' | 'stopped' | 'problem' | 'updates';
export type GroupMode = 'groups' | 'engine' | 'app' | 'network' | 'status' | 'flat';

// Networks a stack can be given an address on (empty on a host with none).
// problem: the daemon's reason a network cannot be used here (a CNI plugin
// that is not installed, typically). The picker grays those out rather than
// offering an attachment Save would refuse.
export type Network = { name: string; driver: string; subnet: string; gateway: string; problem?: string; bridge?: string; addressSource?: string };
export type Attachment = { network: string; service?: string; ip?: string; mac?: string; iface?: string };

// What fjordd says it is doing to a stack. An install or up shows no
// container for the whole pull; without this the page said "stopped".
export const BUSY_LABEL: Record<string, string> = {
  installing: 'Installing…',
  up: 'Starting…',
  update: 'Updating…',
  restart: 'Restarting…',
  down: 'Stopping…',
  deleting: 'Deleting…',
};

// What the UI shows for a stack: its label, falling back to the id.
export function stackLabel(s: { name: string; displayName?: string } | null | undefined): string {
  return s?.displayName || s?.name || '';
}

const STATUS_STYLE: Record<string, string> = {
  running: 'bg-fjord-success/10 border-fjord-success/20 text-fjord-success',
  partial: 'bg-fjord-warning/10 border-fjord-warning/20 text-fjord-warning',
  stopped: 'bg-fjord-neutral/10 border-fjord-neutral/20 text-fjord-fg-muted',
  // Not known yet is not broken: grey, not red.
  checking: 'bg-fjord-neutral/10 border-fjord-neutral/20 text-fjord-fg-dim',
  // The engine answered and could not say (socket down): a host problem.
  unknown: 'bg-fjord-warning/10 border-fjord-warning/20 text-fjord-warning',
};

const DOT: Record<string, string> = {
  running: 'bg-fjord-success',
  partial: 'bg-fjord-warning',
  stopped: 'bg-fjord-neutral',
  checking: 'bg-fjord-neutral animate-pulse',
  unknown: 'bg-fjord-warning',
};

export function statusLabel(status: StackStatus | undefined): string {
  return statusKey(status);
}

export function statusStyle(status: StackStatus | undefined): string {
  return STATUS_STYLE[statusLabel(status)] ?? STATUS_STYLE.unknown;
}

export function statusDot(status: StackStatus | undefined): string {
  return DOT[statusLabel(status)] ?? DOT.unknown;
}

// Draft for the "+" button: an image that comes Up as-is, so a blank stack
// is immediately a box you can open a shell in -- FreeBSD's base image, or
// on Linux alpine, which needs a command to stay up. A podman draft is a
// compose; an appjail draft is a native director spec + Makejail (no
// compose) that runs via appjail-director from the start.
export const NEW_STACK_IMAGE = 'ghcr.io/daemonless/base:15.1-latest';
export const NEW_STACK_MAKEJAIL = `OPTION container=boot\nOPTION overwrite=force\nOPTION from=${NEW_STACK_IMAGE}\n`;

export function newStackCompose(os: string): string {
  return os === 'linux'
    ? `services:\n  app:\n    image: docker.io/library/alpine:3.24\n    command: sleep infinity\n`
    : `services:\n  app:\n    image: ${NEW_STACK_IMAGE}\n`;
}

// Jail name is namespaced by the stack id, like catalog installs.
export function newStackDirector(name: string): string {
  return `options:\n  - virtualnet: ':<random> default'\n  - nat:\nservices:\n  app:\n    name: ${name.replace(/[^A-Za-z0-9_]/g, '_')}_app\n    oci:\n      environment:\n        - TZ: !ENV '\${TZ}'\n`;
}

export function newStackDraft(engine = 'podman', hostOS = '', name = `stack-${Date.now().toString().slice(-4)}`): Stack {
  return engine === 'appjail'
    ? {
        name,
        dir: '',
        compose: '',
        env: `DIRECTOR_PROJECT=${name}\nTZ=UTC\n`,
        director: newStackDirector(name),
        makejail: NEW_STACK_MAKEJAIL,
        engine,
      }
    : {
        name,
        dir: '',
        compose: newStackCompose(hostOS),
        env: 'TZ=UTC\n',
        engine,
      };
}

// Switch a draft's runtime: swap in that engine's starter files. The two
// engines' files are different formats (compose vs director + Makejail), so
// nothing carries over -- which was silent, and took every edit with it. An
// edited draft asks first; an untouched one just switches.
export function isDraftEdited(stack: Stack | null, defaultEngine = 'podman', hostOS = ''): boolean {
  if (!stack) return false;
  const starter = newStackDraft(stack.engine ?? defaultEngine, hostOS, stack.name);
  return (['compose', 'env', 'director', 'makejail'] as const).some((k) => (stack[k] ?? '') !== (starter[k] ?? ''));
}

// Pull the first service image out of a stack's compose (for the retag modal).
export function stackImage(compose: string): string {
  const m = compose.match(/^\s*image:\s*["']?([^\s"'#]+)/m);
  return m ? m[1] : '';
}

// The image as it runs, ${VAR}s filled in from .env: fjordd's services
// list has it. The raw compose line read ":${IMMICH_TAG:-latest}" as a
// tag called "-latest}".
export function currentImage(s: Stack): string {
  return s.services?.[0]?.image || stackImage(s.compose);
}

// Multi-image stacks (e.g. app+db+redis) have no single version to change --
// the Change Version modal would retag every service. Hidden for those.
export function isMultiImage(compose: string | undefined): boolean {
  return ((compose ?? '').match(/^\s*image:/gm) || []).length > 1;
}

// Parse the tag out of an image ref, ignoring any @sha256 digest.
export function refTag(image: string): string {
  const b = image.includes('@') ? image.slice(0, image.indexOf('@')) : image;
  const s = b.lastIndexOf('/');
  const c = b.lastIndexOf(':');
  return c > s ? b.slice(c + 1) : 'latest';
}

export function behind(u?: UpdateInfo): boolean {
  return u?.state === 'available' || u?.state === 'upgrade';
}

// fleet is passed in, not read inside: a $: line re-runs only for what it
// names, and Updates must follow each update check as it lands.
export function inFilter(s: Stack, f: StackFilter, fl: Record<string, UpdateInfo>): boolean {
  return f === 'all' ? true : f === 'updates' ? behind(fl[s.name]) : health(s) === f;
}

export function computeFilterCounts(stacks: Stack[], fl: Record<string, UpdateInfo>): Record<string, number> {
  return {
    running: stacks.filter((s) => inFilter(s, 'running', fl)).length,
    stopped: stacks.filter((s) => inFilter(s, 'stopped', fl)).length,
    problem: stacks.filter((s) => inFilter(s, 'problem', fl)).length,
    updates: stacks.filter((s) => inFilter(s, 'updates', fl)).length,
  };
}

export function filterStacks(stacks: Stack[], search: string, f: StackFilter, fl: Record<string, UpdateInfo>): Stack[] {
  const q = search.toLowerCase();
  return stacks.filter((s) => s.name.toLowerCase().includes(q) && inFilter(s, f, fl));
}

// Manual sidebar order: state.order (1-based) wins; unset (0) sorts last by
// numeric id so fresh installs land at the bottom in a stable order.
export function orderKey(s: Stack): number {
  return s.state?.order && s.state.order > 0 ? s.state.order : Number.MAX_SAFE_INTEGER;
}

export function orderStacks(list: Stack[]): Stack[] {
  return [...list].sort((a, b) => orderKey(a) - orderKey(b) || a.name.localeCompare(b.name, undefined, { numeric: true }));
}

// Which bucket a stack falls in for the active mode.
export function bucketOf(s: Stack, groupMode: GroupMode): string {
  if (groupMode === 'engine') return s.state?.engine || 'podman';
  if (groupMode === 'status') return s.status?.state || 'unknown';
  if (groupMode === 'network') return networkLabel(s.compose);
  if (groupMode === 'flat') return '';
  // By app: the catalog app id (two installs of the same app group together),
  // falling back to the display name for stacks with no recorded origin (BYO).
  if (groupMode === 'app') return s.state?.origin?.app_id || s.displayName || s.name;
  return (s.state?.group || '').trim();
}

const STATUS_RANK: Record<string, number> = { running: 0, partial: 1, stopped: 2, unknown: 3 };

export function groupStacks(ordered: Stack[], groupMode: GroupMode): [string, Stack[]][] {
  const m = new Map<string, Stack[]>();
  for (const s of ordered) {
    const k = bucketOf(s, groupMode);
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
    const rank = (n: string) => (n === 'unknown' ? 3 : n === HOST_NETWORK ? 2 : n === DEFAULT_NETWORK ? 1 : 0);
    return [...m.entries()].sort((a, b) => rank(a[0]) - rank(b[0]) || a[0].localeCompare(b[0]));
  }
  return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0])); // engine / app / flat
}

export function existingGroupsOf(stacks: Stack[]): string[] {
  return [...new Set(stacks.map((s) => (s.state?.group || '').trim()).filter(Boolean))].sort();
}

// Commit a drop: rebuild the flat top-to-bottom order (dragged item removed,
// then reinserted at the target) and the dragged item's new group, persist the
// whole layout, and update locally so the sidebar doesn't flicker.
export function computeReorder(
  ordered: Stack[],
  allStacks: Stack[],
  dragName: string,
  targetName: string | null,
  after: boolean,
  group: string,
): { nextStacks: Stack[]; items: { name: string; group: string }[] } | null {
  if (!dragName) return null;
  const groupOf = (name: string) => (allStacks.find((s) => s.name === name)?.state?.group || '').trim();
  if (targetName === dragName && group === groupOf(dragName)) return null; // dropped on itself

  const flat = ordered.map((s) => s.name).filter((n) => n !== dragName);
  let idx: number;
  if (targetName && targetName !== dragName) {
    idx = flat.indexOf(targetName) + (after ? 1 : 0);
  } else {
    // Dropped on a group header/zone: append after that group's last member.
    const last = ordered.filter((s) => (s.state?.group || '').trim() === group && s.name !== dragName).pop();
    idx = last ? flat.indexOf(last.name) + 1 : flat.length;
  }
  flat.splice(idx < 0 ? flat.length : idx, 0, dragName);

  // Optimistic local update: stamp order by new position + the dragged group.
  const pos = new Map(flat.map((n, i) => [n, i + 1]));
  const nextStacks = allStacks.map((s) => {
    if (!pos.has(s.name)) return s;
    const state = { ...(s.state || {}), order: pos.get(s.name)! };
    if (s.name === dragName) state.group = group;
    return { ...s, state };
  });
  const items = flat.map((n) => ({ name: n, group: n === dragName ? group : groupOf(n) }));
  return { nextStacks, items };
}

// Refresh live status/state only -- never overwrite the editor's
// compose/env with the list's empty values. services goes with them:
// it is what each service is running right now, not something the
// editor holds an unsaved version of.
export function mergeStackList(
  selectedStack: Stack | null,
  freshStacks: Stack[],
): { stacks: Stack[]; selectedStack: Stack | null } {
  if (!selectedStack) return { stacks: freshStacks, selectedStack: null };
  const found = freshStacks.find((s) => s.name === selectedStack.name);
  if (!found) return { stacks: freshStacks, selectedStack };
  return {
    stacks: freshStacks,
    selectedStack: {
      ...selectedStack,
      status: found.status,
      state: found.state,
      services: found.services ?? selectedStack.services,
    },
  };
}

export type StackEventEffects = {
  stacks: Stack[];
  selectedStack: Stack | null;
  reloadStacks?: boolean;
  refreshFleet?: boolean;
  followOutput?: string;
  refreshState?: string;
  checkUpdate?: string;
  clearSelection?: boolean;
};

export function reduceStackEvent(
  stacks: Stack[],
  selectedStack: Stack | null,
  ev: any,
): StackEventEffects {
  if (ev?.type === 'fleet-updates') {
    return { stacks, selectedStack, refreshFleet: true };
  }
  if (ev?.type === 'stack-busy') {
    const busy = ev.busy || undefined;
    const wasBusy = stacks.find((s) => s.name === ev.name)?.busy;
    const nextStacks = stacks.map((s) => (s.name === ev.name ? { ...s, busy } : s));
    let nextSelected = selectedStack;
    let followOutput: string | undefined;
    if (selectedStack && selectedStack.name === ev.name) {
      nextSelected = { ...selectedStack, busy };
      if (busy) followOutput = ev.name; // started elsewhere: follow it
    }
    let refreshState: string | undefined;
    let refreshFleet = false;
    let checkUpdate: string | undefined;
    if (!busy && wasBusy) {
      refreshState = ev.name;
      if (wasBusy === 'update' || wasBusy === 'up') {
        refreshFleet = true;
        if (selectedStack?.name === ev.name) checkUpdate = ev.name;
      }
    }
    return {
      stacks: nextStacks,
      selectedStack: nextSelected,
      followOutput,
      refreshState,
      refreshFleet: refreshFleet || undefined,
      checkUpdate,
    };
  }
  if (ev?.type === 'stack-removed') {
    const nextStacks = stacks.filter((s) => s.name !== ev.name);
    const clearSelection = selectedStack?.name === ev.name;
    return {
      stacks: nextStacks,
      selectedStack: clearSelection ? null : selectedStack,
      clearSelection: clearSelection || undefined,
    };
  }
  if (ev?.type === 'stack') {
    if (!stacks.some((s) => s.name === ev.name)) {
      return { stacks, selectedStack, reloadStacks: true }; // a stack this tab doesn't know yet (installed elsewhere)
    }
    // Status and where Open goes -- never compose/env, so an open editor is safe.
    const linkHost = ev.linkHost ?? undefined;
    const nextStacks = stacks.map((s) => (s.name === ev.name ? { ...s, status: ev.status, linkHost } : s));
    const nextSelected =
      selectedStack && selectedStack.name === ev.name
        ? { ...selectedStack, status: ev.status, linkHost }
        : selectedStack;
    return { stacks: nextStacks, selectedStack: nextSelected };
  }
  return { stacks, selectedStack };
}

// Short service label for pickers: <project>_<service>_1 -> <service>.
// Mirrors the backend's log-prefix derivation (pkg/engine/podman Logs).
export function serviceLabel(stackName: string, containerName: string): string {
  let label = containerName;
  const prefix = stackName.toLowerCase() + '_';
  if (label.toLowerCase().startsWith(prefix)) label = label.slice(prefix.length);
  const i = label.lastIndexOf('_');
  if (i > 0 && /^\d+$/.test(label.slice(i + 1))) label = label.slice(0, i);
  return label || containerName;
}

// Default shell target: the compose's FIRST service (authors list the
// primary app first; deps like db/redis/ml follow). The runtime's container
// list order is arbitrary, so containers[0] would land you in redis.
export function primaryContainer(compose: string, containers: { name: string }[]): string {
  const first = (compose.match(/^services:\s*\n\s{2}([\w.-]+):/m) || [])[1];
  if (first) {
    const hit = containers.find((c) => c.name.includes(`_${first}_`) || c.name === first);
    if (hit) return hit.name;
  }
  return containers[0]?.name ?? '';
}
