import { describe, expect, it } from 'vitest';
import { startError, startsAfterReplace } from './adopt';

describe('start output', () => {
  it('a clean start has no error', () => {
    expect(startError('podman-compose up -d\nnavidrome\n')).toBe('');
  });
  it('an [error] line is the failure, even after a 200', () => {
    expect(startError('podman-compose up -d\n[error] image podman: not found\nmore\n')).toBe('image podman: not found');
    expect(startError('[ERROR]: exit status 1\n')).toBe('exit status 1');
  });
});

describe('after a replace', () => {
  const c = (state: string) => ({ id: 'x', name: 'x', image: 'i', state, engine: 'podman' });
  it('a running container is started again', () => {
    expect(startsAfterReplace(c('running'))).toBe(true);
  });
  it('a stopped or never-started one stays stopped', () => {
    for (const s of ['exited', 'created', 'stopped', 'paused', '']) expect(startsAfterReplace(c(s))).toBe(false);
  });
});
