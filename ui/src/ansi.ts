// SGR colour codes in container logs -> styled runs, so a log looks the way it
// does in a terminal. Other escape sequences are dropped.

export type Style = { fg?: string; bg?: string; bold?: boolean };
export type Run = { t: string; s: Style };

// xterm's 16 colours.
const BASIC = [
  '#000000', '#cd0000', '#00cd00', '#cdcd00', '#0000ee', '#cd00cd', '#00cdcd', '#e5e5e5',
  '#7f7f7f', '#ff0000', '#00ff00', '#ffff00', '#5c5cff', '#ff00ff', '#00ffff', '#ffffff',
];

function rgb(r: number, g: number, b: number): string {
  return `rgb(${r},${g},${b})`;
}

// The 256-colour palette: 16 basic, a 6x6x6 cube, then 24 grays.
function color256(n: number): string | undefined {
  if (!(n >= 0 && n <= 255)) return undefined;
  if (n < 16) return BASIC[n];
  if (n < 232) {
    const v = (c: number) => (c === 0 ? 0 : 55 + c * 40);
    const i = n - 16;
    return rgb(v(Math.floor(i / 36)), v(Math.floor(i / 6) % 6), v(i % 6));
  }
  const g = 8 + (n - 232) * 10;
  return rgb(g, g, g);
}

// Apply one SGR parameter list to a style. Returns the new style.
function sgr(params: number[], cur: Style): Style {
  const s = { ...cur };
  if (params.length === 0) params = [0];
  for (let i = 0; i < params.length; i++) {
    const p = params[i];
    if (p === 0) {
      delete s.fg;
      delete s.bg;
      delete s.bold;
    } else if (p === 1) s.bold = true;
    else if (p === 22) delete s.bold;
    else if (p >= 30 && p <= 37) s.fg = BASIC[p - 30];
    else if (p >= 90 && p <= 97) s.fg = BASIC[p - 90 + 8];
    else if (p === 39) delete s.fg;
    else if (p >= 40 && p <= 47) s.bg = BASIC[p - 40];
    else if (p >= 100 && p <= 107) s.bg = BASIC[p - 100 + 8];
    else if (p === 49) delete s.bg;
    else if (p === 38 || p === 48) {
      let c: string | undefined;
      if (params[i + 1] === 5) {
        c = color256(params[i + 2]);
        i += 2;
      } else if (params[i + 1] === 2) {
        c = rgb(params[i + 2] ?? 0, params[i + 3] ?? 0, params[i + 4] ?? 0);
        i += 4;
      }
      if (c) {
        if (p === 38) s.fg = c;
        else s.bg = c;
      }
    }
  }
  return s;
}

// Any CSI sequence (SGR or not), or a lone two-byte escape.
const ESC = /\x1b(?:\[([0-9;?]*)([@-~])|[@-Z\\-_])/g;

// Split a log into lines of styled runs. Colour carries across a newline,
// as it does in a terminal.
export function parseAnsi(text: string): Run[][] {
  const lines: Run[][] = [];
  let style: Style = {};
  for (const line of text.split('\n')) {
    const runs: Run[] = [];
    let last = 0;
    ESC.lastIndex = 0;
    let m: RegExpExecArray | null;
    while ((m = ESC.exec(line)) !== null) {
      if (m.index > last) runs.push({ t: line.slice(last, m.index), s: style });
      if (m[2] === 'm') style = sgr(m[1] ? m[1].split(';').map((x) => parseInt(x || '0', 10)) : [], style);
      last = ESC.lastIndex;
    }
    if (last < line.length) runs.push({ t: line.slice(last), s: style });
    lines.push(runs);
  }
  return lines;
}

export function plain(runs: Run[]): string {
  return runs.map((r) => r.t).join('');
}

export function css(s: Style): string {
  let out = '';
  if (s.fg) out += `color:${s.fg};`;
  if (s.bg) out += `background-color:${s.bg};`;
  if (s.bold) out += 'font-weight:600;';
  return out;
}

// Block and half-block characters: a line drawn with them is pixel art and
// needs the font's own line height, or the rows show gaps.
const BLOCKS = /[▀-▟]/;
export function isBlockArt(line: string): boolean {
  return BLOCKS.test(line);
}
