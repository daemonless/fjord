import { describe, expect, it } from 'vitest';
import { firstLine } from './errorLine';
import { startError } from './adopt';

describe('firstLine', () => {
  it('is the first line with text, trimmed', () => {
    expect(firstLine('\n  pre-flight failed (stack saved, not started):\n  port 3456/tcp is taken')).toBe(
      'pre-flight failed (stack saved, not started):',
    );
    expect(firstLine('')).toBe('');
  });
  it('fits a status line', () => {
    expect(firstLine('x'.repeat(400)).length).toBe(160);
  });
});

describe('startError in a streamed output', () => {
  it('is the first [error] line', () => {
    expect(startError('$ podman-compose up\n[error] port 3456 is in use\nmore')).toBe('port 3456 is in use');
  });
});
