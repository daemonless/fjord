import { describe, it, expect } from 'vitest';
import { parseEnv, refs, missingVars, isSecret, setEnvVar, usedVars } from './composeVars';

describe('parseEnv', () => {
  it('reads KEY=VALUE, skips comments, strips one pair of quotes', () => {
    expect(parseEnv('# c\nA=1\n\nB="two words"\nC=\'x\'\nD=a=b\nnot a pair\n')).toEqual({
      A: '1',
      B: 'two words',
      C: 'x',
      D: 'a=b',
    });
  });
});

describe('refs', () => {
  const env = { PORT: '8181', EMPTY: '', PUID: '1000' };
  const at = (text: string) => refs(text, env).map(({ name, state, value }) => ({ name, state, value }));

  it('resolves a set variable', () => {
    expect(at('- "${PORT}:8181"')).toEqual([{ name: 'PORT', state: 'set', value: '8181' }]);
  });

  // ${X:-d} takes the default when X is unset OR empty; ${X-d} only when unset.
  it('follows :- and - like compose does', () => {
    expect(at('${MISSING:-3000}')).toEqual([{ name: 'MISSING', state: 'default', value: '3000' }]);
    expect(at('${EMPTY:-3000}')).toEqual([{ name: 'EMPTY', state: 'default', value: '3000' }]);
    expect(at('${EMPTY-3000}')).toEqual([{ name: 'EMPTY', state: 'empty', value: '' }]);
    expect(at('${PUID:-1}')).toEqual([{ name: 'PUID', state: 'set', value: '1000' }]);
  });

  it('marks what nothing sets', () => {
    expect(at('${TZ}')).toEqual([{ name: 'TZ', state: 'unset', value: '' }]);
  });

  it('gives each reference its place in the text', () => {
    const [r] = refs('x: ${PORT}', env);
    expect([r.from, r.to]).toEqual([3, 10]);
  });
});

describe('missingVars', () => {
  it('lists unset variables without a default, once each', () => {
    expect(missingVars('${TZ} ${TZ} ${PUID} ${WEB:-80} ${DATA}', { PUID: '1' })).toEqual(['TZ', 'DATA']);
  });
});

describe('isSecret', () => {
  it('hides passwords, tokens and keys', () => {
    for (const n of ['MYSQL_ROOT_PASSWORD', 'API_TOKEN', 'SECRET_KEY', 'TS_AUTHKEY']) expect(isSecret(n)).toBe(true);
    for (const n of ['PUID', 'TZ', 'WEB_PORT']) expect(isSecret(n)).toBe(false);
  });
});

describe('setEnvVar', () => {
  it('replaces the variable in place and keeps every other line', () => {
    expect(setEnvVar('# ports\nWEB=80\nTZ=UTC\n', 'WEB', '8080')).toBe('# ports\nWEB=8080\nTZ=UTC\n');
  });
  it('adds a missing one at the end', () => {
    expect(setEnvVar('TZ=UTC\n\n', 'NOPE', '42')).toBe('TZ=UTC\nNOPE=42\n');
    expect(setEnvVar('', 'NOPE', '42')).toBe('NOPE=42\n');
  });
  it('never takes a commented line for the variable', () => {
    expect(setEnvVar('#WEB=1\n', 'WEB', '2')).toBe('#WEB=1\nWEB=2\n');
  });
  it('quotes values with spaces or #', () => {
    expect(setEnvVar('', 'MSG', 'a b')).toBe('MSG="a b"\n');
    expect(parseEnv(setEnvVar('', 'MSG', 'a #b')).MSG).toBe('a #b');
  });
});

describe('usedVars', () => {
  it('lists each variable once, unset first', () => {
    expect(usedVars('${A} ${B} ${A} ${C:-1}', { A: '1' }).map((r) => r.name)).toEqual(['B', 'A', 'C']);
  });
});
