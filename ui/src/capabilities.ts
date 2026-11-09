import * as yaml from 'js-yaml';
import type { StackChoice } from './choices';

export type CapabilityCheckInput = {
  compose: string;
  choices?: StackChoice[];
  picks?: Record<string, string>;
  variables?: { name: string; type?: string; default?: string }[];
  formData?: Record<string, string>;
  values?: Record<string, string>;
  paths?: Record<string, string[]>;
  appDataLocations?: string[];
  storageBase?: string;
  allowedRoots?: string[];
  folderSets?: { folders?: string[] }[];
};

export function normalizePosixPath(p: string): string {
  const trimmed = p.trim();
  if (!trimmed) return '';
  const isAbs = trimmed.startsWith('/');
  const parts = trimmed.split('/');
  const stack: string[] = [];
  for (const part of parts) {
    if (!part || part === '.') continue;
    if (part === '..') {
      if (stack.length > 0 && stack[stack.length - 1] !== '..') {
        stack.pop();
      } else if (!isAbs) {
        stack.push('..');
      }
    } else {
      stack.push(part);
    }
  }
  if (isAbs) {
    return '/' + stack.join('/');
  }
  return stack.join('/') || '.';
}

function hasDotDotSegment(p: string): boolean {
  return p
    .trim()
    .split('/')
    .some((part) => part === '..');
}

export function isTruthyYamlBool(val: unknown): boolean {
  if (val === true || val === 1) return true;
  if (typeof val === 'string') {
    const norm = val.trim().replace(/^['"]|['"]$/g, '').trim().toLowerCase();
    return norm === 'true' || norm === 'yes' || norm === 'on' || norm === '1';
  }
  return false;
}

function isWithinAllowedPath(hostPath: string, allowedRoots: string[]): boolean {
  if (hasDotDotSegment(hostPath)) return false;
  const norm = normalizePosixPath(hostPath);
  if (!norm) return true;
  if (norm === '/etc/localtime') return true;
  for (const rawRoot of allowedRoots) {
    if (hasDotDotSegment(rawRoot)) continue;
    const root = normalizePosixPath(rawRoot);
    if (!root || root === '/') continue;
    if (norm === root || norm.startsWith(`${root}/`)) return true;
  }
  return false;
}

function collectCompose(
  composeText: string,
  choices: StackChoice[] = [],
  picks: Record<string, string> = {},
): { services: Record<string, any>; volumes: Record<string, any> } {
  let doc: any;
  try {
    doc = yaml.load(composeText);
  } catch {
    return { services: {}, volumes: {} };
  }
  const baseServices =
    doc && typeof doc === 'object' && doc.services && typeof doc.services === 'object'
      ? { ...doc.services }
      : {};
  const baseVolumes =
    doc && typeof doc === 'object' && doc.volumes && typeof doc.volumes === 'object'
      ? { ...doc.volumes }
      : {};

  for (const c of choices) {
    const pickId = picks[c.id] || c.default;
    const opt = c.options?.find((o) => o.id === pickId);
    if (!opt) continue;
    for (const drop of opt.drop ?? []) {
      delete baseServices[drop];
    }
    if (!opt.services) continue;
    let parsedSvcs: unknown = opt.services;
    if (typeof opt.services === 'string') {
      try {
        parsedSvcs = yaml.load(opt.services);
      } catch {
        parsedSvcs = null;
      }
    }
    if (parsedSvcs && typeof parsedSvcs === 'object') {
      for (const [k, v] of Object.entries(parsedSvcs as Record<string, unknown>)) {
        baseServices[k] = v;
      }
    }
  }
  return { services: baseServices, volumes: baseVolumes };
}

// A value the user typed, browsed or picked differs from the catalog's; that
// location is their choice, so only catalog-supplied values ask.
function userChose(
  varName: string,
  inlineDef: string | undefined,
  varDefaults: Record<string, string>,
  mergedValues: Record<string, string>,
  paths: Record<string, string[]>,
): boolean {
  const catalog = (varDefaults[varName] ?? inlineDef ?? '').trim();
  const pathList = (paths[varName] || []).map((p) => p.trim()).filter(Boolean);
  if (pathList.length > 0) return pathList.some((p) => p !== catalog);
  if (!(varName in mergedValues)) return false;
  return (mergedValues[varName] ?? '').trim() !== catalog;
}

function expandVolumeSources(
  rawSource: string,
  varDefaults: Record<string, string>,
  mergedValues: Record<string, string>,
  paths: Record<string, string[]>,
  zfsVars: Set<string>,
): string[] {
  const trimmed = rawSource.trim();
  if (!trimmed) return [];

  // A '..' in the compose's own text can climb out of any folder, chosen or not.
  const literal = trimmed.replace(/\$\{[A-Za-z_][A-Za-z0-9_]*(?::-[^}]*)?\}/g, 'x');
  const vars = [...trimmed.matchAll(/\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}/g)];
  if (
    vars.length > 0 &&
    !hasDotDotSegment(literal) &&
    vars.every(([, n, d]) => userChose(n, d, varDefaults, mergedValues, paths))
  ) {
    return [];
  }

  // Check if the entire source or its prefix is a single ${VAR} or ${VAR:-default}
  const exactVar = trimmed.match(/^\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}(.*)$/);
  if (exactVar) {
    const [, varName, inlineDef, suffix] = exactVar;
    const pathList = (paths[varName] || []).map((p) => p.trim()).filter(Boolean);
    if (pathList.length > 0) {
      return pathList.map((p) => `${p}${suffix}`);
    }
    const val = (mergedValues[varName] ?? varDefaults[varName] ?? inlineDef ?? '').trim();
    if (zfsVars.has(varName)) {
      if (
        !val.startsWith('/') &&
        !val.startsWith('.') &&
        !val.startsWith('~') &&
        !hasDotDotSegment(val) &&
        !hasDotDotSegment(suffix)
      ) {
        return [];
      }
    }
    if (val) return [`${val}${suffix}`];
    if (suffix && (suffix.startsWith('/') || hasDotDotSegment(suffix))) return [suffix];
    return [];
  }

  // General variable interpolation
  let touchesZfs = false;
  const resolved = trimmed.replace(/\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}/g, (_, varName, inlineDef) => {
    if (zfsVars.has(varName)) touchesZfs = true;
    const pathList = (paths[varName] || []).map((p) => p.trim()).filter(Boolean);
    if (pathList.length > 0) return pathList[0];
    return (mergedValues[varName] ?? varDefaults[varName] ?? inlineDef ?? '').trim();
  });
  if (
    touchesZfs &&
    !resolved.startsWith('/') &&
    !resolved.startsWith('.') &&
    !resolved.startsWith('~') &&
    !hasDotDotSegment(resolved)
  ) {
    return [];
  }
  return resolved ? [resolved] : [];
}

function extractVolumeRawSources(volumes: unknown): string[] {
  if (!Array.isArray(volumes)) return [];
  const out: string[] = [];
  for (const entry of volumes) {
    if (typeof entry === 'string') {
      const s = entry.trim();
      if (!s || /^nfs:\/\//i.test(s) || /^smb:\/\//i.test(s)) continue;
      // Find the separator between source and target while respecting ${VAR:-default}
      let depth = 0;
      let colonIdx = -1;
      for (let i = 0; i < s.length; i++) {
        if (s[i] === '{') depth++;
        else if (s[i] === '}' && depth > 0) depth--;
        else if (s[i] === ':' && depth === 0) {
          colonIdx = i;
          break;
        }
      }
      if (colonIdx <= 0) continue;
      out.push(s.slice(0, colonIdx).trim());
    } else if (entry && typeof entry === 'object') {
      const obj = entry as { type?: string; source?: string };
      if (obj.type && obj.type !== 'bind') continue;
      if (typeof obj.source === 'string' && obj.source.trim()) {
        out.push(obj.source.trim());
      }
    }
  }
  return out;
}

function cleanMode(val: unknown): string {
  return String(val ?? '')
    .trim()
    .replace(/^['"]|['"]$/g, '')
    .trim()
    .toLowerCase();
}

function extractJailAnnotations(annotations: unknown): string[] {
  const out: string[] = [];
  const checkEntry = (rawKey: string, rawVal: unknown) => {
    const k = rawKey.trim().replace(/^['"]|['"]$/g, '').trim();
    const v = String(rawVal ?? '')
      .trim()
      .replace(/^['"]|['"]$/g, '')
      .trim()
      .toLowerCase();
    if (
      k.startsWith('org.freebsd.jail.') &&
      k !== 'org.freebsd.jail.vnet' &&
      k !== 'org.freebsd.jail.allow.mlock' && // .NET runtimes need it; no host access
      v !== 'false' &&
      v !== '0' &&
      v !== 'no' &&
      v !== 'off'
    ) {
      out.push(k);
    }
  };
  if (Array.isArray(annotations)) {
    for (const item of annotations) {
      const s = String(item ?? '').trim();
      if (!s) continue;
      const eq = s.indexOf('=');
      const colon = s.indexOf(':');
      const sep = eq >= 0 && (colon < 0 || eq < colon) ? eq : colon;
      if (sep >= 0) {
        checkEntry(s.slice(0, sep), s.slice(sep + 1));
      } else {
        checkEntry(s, 'true');
      }
    }
  } else if (annotations && typeof annotations === 'object') {
    for (const [k, v] of Object.entries(annotations as Record<string, unknown>)) {
      checkEntry(k, v);
    }
  }
  return out;
}

/**
 * Inspect a catalog app's compose (plus active choice services and top-level volumes)
 * for elevated host capabilities: privileged, cap_add, devices, host namespaces
 * (pid, ipc, userns_mode, network_mode), FreeBSD jail annotations, or bind mounts
 * outside configured App data locations, folder sets, and /etc/localtime.
 */
export function detectHostCapabilities(input: CapabilityCheckInput): string[] {
  const { services, volumes } = collectCompose(input.compose || '', input.choices, input.picks);
  const serviceEntries = Object.entries(services);
  const volumeEntries = Object.entries(volumes);
  if (!serviceEntries.length && !volumeEntries.length) return [];

  const varDefaults: Record<string, string> = {};
  const zfsVars = new Set<string>();
  for (const v of input.variables ?? []) {
    if (!v?.name) continue;
    if (v.default !== undefined) varDefaults[v.name] = String(v.default);
    if (v.type === 'zfs_dataset') zfsVars.add(v.name);
  }
  const mergedValues: Record<string, string> = {
    ...(input.formData ?? {}),
    ...(input.values ?? {}),
  };

  const allowedRoots: string[] = [
    ...(input.allowedRoots ?? []),
    ...(input.storageBase ? [input.storageBase] : []),
    ...(input.appDataLocations ?? []),
    ...(input.folderSets ?? []).flatMap((fs) => fs.folders ?? []),
  ];
  const effectiveBase =
    normalizePosixPath(input.storageBase || input.appDataLocations?.[0] || '/containers') || '/containers';

  const found: string[] = [];
  const seen = new Set<string>();
  const add = (item: string) => {
    if (!seen.has(item)) {
      seen.add(item);
      found.push(item);
    }
  };

  const checkResolvedMount = (rawSource: string, rawExpanded: string, forceBind = false) => {
    const trimmed = rawExpanded.trim();
    if (!trimmed || /^nfs:\/\//i.test(trimmed) || /^smb:\/\//i.test(trimmed)) {
      return;
    }
    const hasTraversal = hasDotDotSegment(rawSource) || hasDotDotSegment(trimmed);
    if (!hasTraversal && /^\{\{\s*(?:appdata|base)\s*\}\}(?:\/|$)/.test(trimmed)) {
      return;
    }
    const src = trimmed
      .replace(/\{\{\s*(?:appdata|base)\s*\}\}/g, effectiveBase)
      .replace(/\{\{\s*stack\s*\}\}/g, 'stack');
    const isHostBind =
      forceBind ||
      src.startsWith('/') ||
      src.startsWith('./') ||
      src.startsWith('../') ||
      src.startsWith('~/') ||
      src === '.' ||
      src === '..' ||
      src === '~' ||
      hasDotDotSegment(src);
    if (!isHostBind) return;
    if (hasTraversal || hasDotDotSegment(src) || !isWithinAllowedPath(src, allowedRoots)) {
      add(`bind mount ${normalizePosixPath(src)}`);
    }
  };

  for (const [, svc] of serviceEntries) {
    if (!svc || typeof svc !== 'object') continue;

    if (isTruthyYamlBool(svc.privileged)) {
      add('privileged: true');
    }

    if (Array.isArray(svc.cap_add)) {
      const caps = svc.cap_add.map((c: unknown) => String(c ?? '').trim()).filter(Boolean);
      if (caps.length > 0) {
        add(`cap_add (${caps.join(', ')})`);
      }
    }

    if (Array.isArray(svc.devices)) {
      const devs = svc.devices
        .map((d: unknown) => {
          const s = String(d ?? '').trim();
          return s.split(':')[0]?.trim() || s;
        })
        .filter(Boolean);
      if (devs.length > 0) {
        add(`devices (${devs.join(', ')})`);
      }
    }

    if (cleanMode(svc.pid ?? svc.pid_mode) === 'host') {
      add('pid: host');
    }
    if (cleanMode(svc.ipc ?? svc.ipc_mode) === 'host') {
      add('ipc: host');
    }
    if (cleanMode(svc.userns_mode) === 'host') {
      add('userns_mode: host');
    }
    if (cleanMode(svc.network_mode) === 'host') {
      add('network_mode: host');
    }

    const jailAnns = extractJailAnnotations(svc.annotations);
    if (jailAnns.length > 0) {
      add(`annotations (${jailAnns.join(', ')})`);
    }

    const rawSources = extractVolumeRawSources(svc.volumes);
    for (const rawSource of rawSources) {
      const expanded = expandVolumeSources(
        rawSource,
        varDefaults,
        mergedValues,
        input.paths ?? {},
        zfsVars,
      );
      for (const rawExpanded of expanded) {
        checkResolvedMount(rawSource, rawExpanded);
      }
    }
  }

  for (const [volName, volDef] of volumeEntries) {
    if (!volDef || typeof volDef !== 'object') continue;
    const opts = (volDef as any).driver_opts;
    if (!opts || typeof opts !== 'object') continue;
    const rawDevice = String(opts.device ?? opts.source ?? '').trim();
    const optO = String(opts.o ?? '')
      .trim()
      .toLowerCase();
    const optType = String(opts.type ?? '')
      .trim()
      .toLowerCase();
    const hasBindOpt =
      optO
        .split(',')
        .map((s) => s.trim())
        .includes('bind') ||
      optType === 'none' ||
      optType === 'bind';
    if (rawDevice) {
      if (
        !hasBindOpt &&
        (optType === 'nfs' ||
          optType === 'cifs' ||
          /^nfs:\/\//i.test(rawDevice) ||
          /^smb:\/\//i.test(rawDevice) ||
          rawDevice.startsWith(':/') ||
          rawDevice.startsWith('//'))
      ) {
        continue;
      }
      const expanded = expandVolumeSources(
        rawDevice,
        varDefaults,
        mergedValues,
        input.paths ?? {},
        zfsVars,
      );
      if (expanded.length === 0 && hasBindOpt) {
        add(`bind mount ${volName}`);
      }
      for (const rawExpanded of expanded) {
        checkResolvedMount(rawDevice, rawExpanded, hasBindOpt);
      }
    } else if (hasBindOpt) {
      add(`bind mount ${volName}`);
    }
  }

  return found;
}
