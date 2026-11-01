import { describe, it, expect } from 'vitest';
import { strength } from './password';

describe('strength', () => {
  it('calls the obvious ones weak, with why', () => {
    for (const p of ['password', 'Password1!', 'fjord2024', 'aaaaaaaa', 'qwertyui', 'hunter2hunter2', 'kit7']) {
      const s = strength(p);
      expect(s.level, p).toBe(1);
      expect(s.why, p).not.toBe('');
    }
  });
  it('rates length above all', () => {
    expect(strength('kitten77').level).toBe(2);
    expect(strength('correct horse battery staple').level).toBe(4);
    expect(strength('').level).toBe(0);
  });
});
