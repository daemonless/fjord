import type { Changes } from './updateWords';

export type FleetServiceUpdate = {
  service: string;
  tag?: string;
  state?: string;
  running?: string;
  latest?: string;
  fromVersion?: string;
  toVersion?: string;
  newTag?: string;
  detail?: string;
};

export type FleetStackUpdate = {
  state?: string;
  tag?: string;
  newTag?: string;
  fromVersion?: string;
  toVersion?: string;
  services?: FleetServiceUpdate[];
};

export type MassUpdateStackInput = {
  name: string;
  displayName?: string;
  busy?: string;
};

export type MassUpdateRow = {
  stack: string;
  label: string;
  services: FleetServiceUpdate[];
  showServiceName: boolean;
};

export type DetailedChanges = Omit<Changes, 'changed' | 'added' | 'removed'> & {
  changed?: { name: string; from: string; to: string }[];
  added?: { name: string; version: string }[];
  removed?: { name: string; version: string }[];
};

export const shortDigest = (d?: string): string => (d ?? '').replace('sha256:', '').slice(0, 12);

export const changesKeyFor = (stack: string, s: FleetServiceUpdate): string =>
  `${stack}:${s.service}:${s.latest ?? s.newTag ?? ''}`;

export const isUpdatableFleetStack = (u?: FleetStackUpdate): boolean =>
  u?.state === 'available' || (u?.state === 'upgrade' && !!(u.newTag || u.services?.some((s) => s.state === 'upgrade' && s.newTag)));

/** Build the ordered rows for the Update all review panel from the stack list and fleet cache. */
export function massUpdateRows(
  stacks: MassUpdateStackInput[],
  fleet: Record<string, FleetStackUpdate | undefined>,
  fallbackTags: Record<string, string | undefined> = {},
): MassUpdateRow[] {
  const rows: MassUpdateRow[] = [];
  for (const s of stacks) {
    if (s.busy) continue;
    const u = fleet[s.name];
    if (!isUpdatableFleetStack(u)) continue;
    const allSvcs = u?.services ?? [];
    const behind = allSvcs
      .filter((sv) => sv.state === 'available' || (sv.state === 'upgrade' && !!sv.newTag))
      .map((sv) => (sv.tag ? sv : { ...sv, tag: fallbackTags[s.name] }));
    const services: FleetServiceUpdate[] = behind.length
      ? behind
      : [
          {
            service: s.name,
            tag: u?.tag ?? fallbackTags[s.name],
            state: u?.state ?? 'available',
            newTag: u?.newTag,
            fromVersion: u?.fromVersion,
            toVersion: u?.toVersion,
          },
        ];
    const showServiceName =
      allSvcs.length > 1 || services.length > 1 || services.some((sv) => sv.service !== s.name);
    rows.push({
      stack: s.name,
      label: s.displayName || s.name,
      services,
      showServiceName,
    });
  }
  return rows;
}

/** Return the subset of rows still ticked for update. */
export function pickedMassRows(rows: MassUpdateRow[], unpicked: Record<string, boolean>): MassUpdateRow[] {
  return rows.filter((r) => !unpicked[r.stack]);
}
