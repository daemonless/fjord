import { describe, expect, it } from 'vitest';
import { startError } from './adopt';

describe('start output', () => {
  it('a clean start has no error', () => {
    expect(startError('podman-compose up -d\nnavidrome\n')).toBe('');
  });
  it('an [error] line is the failure, even after a 200', () => {
    expect(startError('podman-compose up -d\n[error] image podman: not found\nmore\n')).toBe('image podman: not found');
    expect(startError('[ERROR]: exit status 1\n')).toBe('exit status 1');
  });
});
