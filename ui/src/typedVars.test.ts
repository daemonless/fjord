import { describe, expect, it } from 'vitest';
import {
  boolChecked,
  boolToggled,
  choiceOptions,
  choiceProblem,
  effectiveType,
  formatSize,
  intProblem,
  isBoolVar,
  parseSize,
  sizeProblem,
  sizeUnits,
  typedVarProblem,
} from './typedVars';

describe('typedVars', () => {
  it('treats true/false string defaults as bool automatically', () => {
    expect(isBoolVar({ name: 'ENABLE_STATS', type: 'string', default: 'true' })).toBe(true);
    expect(isBoolVar({ name: 'SKIP_CHOWN', default: 'false' })).toBe(true);
    expect(isBoolVar({ name: 'TZ', type: 'string', default: 'UTC' })).toBe(false);
    expect(effectiveType({ name: 'ENABLE_STATS', type: 'string', default: 'true' })).toBe('bool');
  });

  it('supports custom on/off values for bool variables', () => {
    const v = { name: 'LEGACY', type: 'bool', default: 'yes', on: 'yes', off: 'no' };
    expect(boolChecked(v, 'yes')).toBe(true);
    expect(boolChecked(v, 'no')).toBe(false);
    expect(boolChecked(v, '')).toBe(true); // falls back to default
    expect(boolToggled(v, false)).toBe('no');
    expect(boolToggled(v, true)).toBe('yes');
  });

  it('validates int variables and min/max bounds', () => {
    const v = { name: 'WORKERS', type: 'int', min: 1, max: 16 };
    expect(intProblem(v, '4')).toBe('');
    expect(intProblem(v, '0')).toBe('Must be at least 1');
    expect(intProblem(v, '32')).toBe('Must be at most 16');
    expect(intProblem(v, 'abc')).toBe('Must be a whole number');
    expect(typedVarProblem(v, '0')).toBe('Must be at least 1');
  });

  it('normalizes and validates choice variables', () => {
    const v = {
      name: 'LOG_LEVEL',
      type: 'choice',
      options: ['debug', { value: 'info', label: 'Info' }],
    };
    expect(choiceOptions(v)).toEqual([
      { value: 'debug', label: 'debug' },
      { value: 'info', label: 'Info' },
    ]);
    expect(choiceProblem(v, 'info')).toBe('');
    expect(choiceProblem(v, 'trace')).toBe('Pick one of debug, info');
  });

  it('parses, formats, and validates size variables', () => {
    expect(sizeUnits('50g')).toEqual(['k', 'm', 'g', 't']);
    expect(sizeUnits('10G')).toEqual(['K', 'M', 'G', 'T']);
    expect(parseSize('50g', '50g')).toEqual({ amount: '50', unit: 'g' });
    expect(parseSize('', '10G')).toEqual({ amount: '', unit: 'G' });
    expect(formatSize('100', 'g')).toBe('100g');
    expect(sizeProblem('50g')).toBe('');
    expect(sizeProblem('512M')).toBe('');
    expect(sizeProblem('0g')).not.toBe('');
    expect(sizeProblem('50x')).not.toBe('');
  });
});
