import type { ITheme } from '@xterm/xterm';
import { pageIsLight } from './theme';

// xterm paints its own canvas, so the page's CSS never reaches it: the
// colours are handed over here, and again whenever the theme changes.
const DARK: ITheme = {
  background: '#161619',
  foreground: '#cbd5e1',
  cursor: '#cbd5e1',
  selectionBackground: '#334155',
};

// xterm's default ANSI palette is for a dark screen: its white, bright white
// and yellow vanish on white. The GNOME palette's darker steps hold up.
const LIGHT: ITheme = {
  foreground: '#2a2a31',
  cursor: '#2a2a31',
  cursorAccent: '#ffffff',
  selectionBackground: '#c0d6f5',
  black: '#2a2a31',
  red: '#c01c28',
  green: '#26a269',
  yellow: '#9c6e03',
  blue: '#1c71d8',
  magenta: '#813d9c',
  cyan: '#0f7b8a',
  white: '#7c7c87',
  brightBlack: '#61616b',
  brightRed: '#e01b24',
  brightGreen: '#2ec27e',
  brightYellow: '#c88800',
  brightBlue: '#3584e4',
  brightMagenta: '#9141ac',
  brightCyan: '#1a9fb3',
  brightWhite: '#45454e',
};

/** The xterm colours for the current theme. In light the terminal takes the
 *  background of the element it sits in, so it reads as part of that card. */
export function xtermTheme(host: HTMLElement): ITheme {
  if (!pageIsLight()) return DARK;
  const bg = getComputedStyle(host).backgroundColor;
  return { ...LIGHT, background: bg };
}
