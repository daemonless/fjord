import { describe, expect, it } from 'vitest';
import { insidePort, ownAddress } from './ownPort';

describe('insidePort', () => {
  it('reads the container side of a published variable', () => {
    expect(insidePort('ports:\n  - "${WEB_PORT}:8000"\n', 'WEB_PORT')).toBe('8000');
    expect(insidePort('- ${WEB_PORT:-3456}:3456/tcp', 'WEB_PORT')).toBe('3456');
  });
  it('is empty for a variable not published', () => {
    expect(insidePort('- "${WEB_PORT}:8000"', 'WEB')).toBe('');
    expect(insidePort('environment:\n  - PORT=${PORT}', 'PORT')).toBe('');
  });
});

describe('ownAddress', () => {
  const nets = [{ name: 'lan', driver: 'epair' }, { name: 'ajnet', driver: 'virtualnet' }];
  it('is an epair network only', () => {
    expect(ownAddress('lan', nets)).toBe(true);
    expect(ownAddress('ajnet', nets)).toBe(false);
    expect(ownAddress('bridge', nets)).toBe(false);
    expect(ownAddress('', nets)).toBe(false);
  });
});
