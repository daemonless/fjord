export type StoreView = 'cards' | 'list';

const KEY = 'fjord.store.view';

export function normalizeStoreView(raw: unknown): StoreView {
  return raw === 'list' ? 'list' : 'cards';
}

export function readStoreView(storage?: Pick<Storage, 'getItem'> | null): StoreView {
  try {
    const s = storage ?? (typeof localStorage !== 'undefined' ? localStorage : null);
    return normalizeStoreView(s?.getItem(KEY));
  } catch {
    return 'cards';
  }
}

export function writeStoreView(view: StoreView, storage?: Pick<Storage, 'setItem'> | null): void {
  try {
    const s = storage ?? (typeof localStorage !== 'undefined' ? localStorage : null);
    s?.setItem(KEY, normalizeStoreView(view));
  } catch {}
}

export function isAppInstalled(appId: string, stackIds: string[]): boolean {
  return stackIds.includes(appId);
}
