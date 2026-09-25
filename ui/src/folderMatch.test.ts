import { describe, it, expect } from 'vitest';
import { setFor } from './folderMatch';

// The presets, in the order the daemon offers them.
const sets = [
  { name: 'Movies', match: 'MOVIE|FILM', folders: ['/data/movies'] },
  { name: 'TV', match: 'TV|SHOW|SERIES', folders: ['/data/tv'] },
  { name: 'Music', match: 'MUSIC', folders: ['/data/music'] },
  { name: 'Audiobooks', match: 'AUDIOBOOK', folders: ['/data/audiobooks'] },
  { name: 'Ebooks', match: 'EBOOK|BOOK', folders: ['/data/ebooks'] },
  { name: 'Downloads', match: 'DOWNLOAD', folders: ['/data/downloads'] },
  { name: 'Photos', match: 'PHOTO|PICTURE', folders: [] }, // no folders: never picked
];
const pick = (name: string, label?: string) => setFor(name, label, sets)?.name;

describe('setFor', () => {
  // The catalog's book apps, as they name their folders.
  it('gives each book app the right library', () => {
    expect(pick('AUDIOBOOKS_PATH', 'Audiobook library path')).toBe('Audiobooks'); // audiobookshelf
    expect(pick('BOOKS_LOCATION', 'Path to your book library')).toBe('Ebooks'); // grimmory
    expect(pick('MEDIA_PATH', 'Audiobook library')).toBe('Audiobooks'); // readmeabook, by label
  });

  // An app's own storage whose label mentions photos is not a photo library.
  it('reads a label only when it calls the folder a library', () => {
    const withPhotos = [...sets.slice(0, 6), { name: 'Photos', match: 'PHOTO|PICTURE', folders: ['/data/photos'] }];
    expect(setFor('DATA_PATH', 'Media storage (photos, videos, thumbnails)', withPhotos)).toBeUndefined(); // immich-server
    expect(setFor('DATA_PATH', 'Media library (comics, manga, books)', withPhotos)?.name).toBe('Ebooks'); // stump
  });

  it('never hands a drop folder the whole library', () => {
    expect(pick('BOOKDROP_LOCATION', 'Drop folder watched for automatic imports')).toBeUndefined();
  });

  it('keeps the plain cases working', () => {
    expect(pick('MOVIES_PATH')).toBe('Movies');
    expect(pick('TV_PATH')).toBe('TV');
    expect(pick('DOWNLOADS_PATH')).toBe('Downloads');
    expect(pick('SERIES_DIR')).toBe('TV');
  });

  it('matches whole words only', () => {
    expect(pick('SHOWCASE_PATH')).toBeUndefined(); // SHOW is not SHOWCASE
    expect(pick('CONFIG_DATA', 'Configuration directory')).toBeUndefined();
  });

  it('skips a set with no folders yet', () => {
    expect(pick('PHOTOS_PATH')).toBeUndefined();
  });
});
