import { describe, expect, it } from 'vitest';
import { css, isBlockArt, parseAnsi, plain } from './ansi';

describe('parseAnsi', () => {
  it('keeps plain text as one run', () => {
    expect(parseAnsi('hello')).toEqual([[{ t: 'hello', s: {} }]]);
  });

  it('reads truecolor foreground and background', () => {
    const [line] = parseAnsi('\x1b[38;2;226;61;49m\x1b[48;2;49;63;78m▀\x1b[0m');
    expect(line).toEqual([{ t: '▀', s: { fg: 'rgb(226,61,49)', bg: 'rgb(49,63,78)' } }]);
  });

  it('reads the 256-colour gray ramp and cube', () => {
    expect(parseAnsi('\x1b[38;5;235mx')[0][0].s.fg).toBe('rgb(38,38,38)');
    expect(parseAnsi('\x1b[38;5;196mx')[0][0].s.fg).toBe('rgb(255,0,0)');
  });

  it('reads basic colours, bold and reset', () => {
    const [line] = parseAnsi('\x1b[1;31mERR\x1b[0m ok');
    expect(line).toEqual([
      { t: 'ERR', s: { fg: '#cd0000', bold: true } },
      { t: ' ok', s: {} },
    ]);
  });

  it('carries colour across a newline', () => {
    const lines = parseAnsi('\x1b[32mone\ntwo\x1b[m');
    expect(lines[1]).toEqual([{ t: 'two', s: { fg: '#00cd00' } }]);
  });

  it('drops escape sequences that are not colour', () => {
    expect(plain(parseAnsi('\x1b[2K\x1b[1Gdone')[0])).toBe('done');
  });

  it('keeps empty lines', () => {
    expect(parseAnsi('a\n\nb').map(plain)).toEqual(['a', '', 'b']);
  });
});

describe('css', () => {
  it('turns a style into inline css', () => {
    expect(css({ fg: '#fff', bg: 'rgb(1,2,3)', bold: true })).toBe(
      'color:#fff;background-color:rgb(1,2,3);font-weight:600;',
    );
    expect(css({})).toBe('');
  });
});

describe('isBlockArt', () => {
  it('spots half-block pixel art', () => {
    expect(isBlockArt('  ▄▄▀▀')).toBe(true);
    expect(isBlockArt('[init] Starting')).toBe(false);
  });
});
