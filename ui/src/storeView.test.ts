import { describe, it, expect } from 'vitest';
import {
  isAppInstalled,
  normalizeStacksView,
  normalizeStoreView,
  readStacksView,
  readStoreView,
  writeStacksView,
  writeStoreView,
} from './storeView';

describe('storeView', () => {
  it('defaults to cards when nothing is stored or value is unknown', () => {
    expect(normalizeStoreView(null)).toBe('cards');
    expect(normalizeStoreView('grid')).toBe('cards');
    expect(normalizeStoreView('list')).toBe('list');
  });

  it('persists and reads back the selected view mode', () => {
    const mem = new Map<string, string>();
    const storage = {
      getItem: (k: string) => mem.get(k) ?? null,
      setItem: (k: string, v: string) => {
        mem.set(k, v);
      },
    };
    expect(readStoreView(storage)).toBe('cards');
    writeStoreView('list', storage);
    expect(readStoreView(storage)).toBe('list');
    writeStoreView('cards', storage);
    expect(readStoreView(storage)).toBe('cards');
  });

  it('defaults stacks view to list and persists cards/list separately from store view', () => {
    const mem = new Map<string, string>();
    const storage = {
      getItem: (k: string) => mem.get(k) ?? null,
      setItem: (k: string, v: string) => {
        mem.set(k, v);
      },
    };
    expect(normalizeStacksView(null)).toBe('list');
    expect(normalizeStacksView('cards')).toBe('cards');
    expect(readStacksView(storage)).toBe('list');
    writeStacksView('cards', storage);
    expect(readStacksView(storage)).toBe('cards');
    expect(readStoreView(storage)).toBe('cards');
    writeStacksView('list', storage);
    expect(readStacksView(storage)).toBe('list');
  });

  it('detects installed apps by id', () => {
    expect(isAppInstalled('radarr', ['radarr', 'sonarr'])).toBe(true);
    expect(isAppInstalled('lidarr', ['radarr', 'sonarr'])).toBe(false);
  });
});
