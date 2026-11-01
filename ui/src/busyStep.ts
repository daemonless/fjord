// What a busy stack is doing right now, in a few words, read from the
// action's output. The header said only "Installing…" for the whole pull;
// the step and the download's size were in the Output panel at the bottom.

/** busyStep names the step the output is on, or '' when it says nothing known. */
export function busyStep(output: string): string {
  // eslint-disable-next-line no-control-regex
  const lines = output.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, '').split(/\r?\n|\r/);
  let still = '';
  for (let i = lines.length - 1; i >= 0; i--) {
    const l = lines[i].trim();
    // "[fjord] still pulling x (100 MB) · 45s": the pull's time so far.
    const s = !still && l.match(/^\[fjord\] still .* · (\S+)$/);
    if (s) still = s[1];
    const pull = l.match(/^\$ (?:podman pull|buildah pull(?: --policy \w+)?) (\S+)/);
    if (pull) {
      const size = output.match(new RegExp(`\\[fjord\\] pulling ${reEscape(pull[1])}: (\\S+ \\S+) in`))?.[1];
      const name = shortImage(pull[1]);
      const sofar = [size, still].filter(Boolean).join(', ');
      return sofar ? `Downloading ${name} (${sofar})` : `Downloading ${name}`;
    }
    if (/^Writing manifest/.test(l)) return 'Saving the image';
    const jail = l.match(/^\[(\d+)\/(\d+)\] (Creating|Starting) (\S+)/);
    if (jail) return /^C/.test(jail[3]) ? `Building ${jail[4]} (${jail[1]} of ${jail[2]})` : `Starting ${jail[4]} (${jail[1]} of ${jail[2]})`;
    const cmd = l.match(/^\$ (\S+)(.*)$/);
    if (cmd) {
      const args = cmd[2];
      if (cmd[1] === 'podman-compose') {
        if (/\bup\b/.test(args)) return 'Creating the containers';
        if (/\bdown\b/.test(args)) return 'Removing the containers';
      }
      if (cmd[1] === 'appjail-director') {
        if (/\bup\b/.test(args)) return 'Building the jails';
        if (/\bdown\b/.test(args)) return 'Removing the jails';
      }
      if (cmd[1] === 'podman' && /^ start /.test(args)) return 'Starting the containers';
      if (cmd[1] === 'podman' && /^ stop /.test(args)) return 'Stopping the containers';
      return '';
    }
  }
  return '';
}

// ghcr.io/daemonless/openspeedtest:latest -> openspeedtest
function shortImage(ref: string): string {
  return ref.replace(/@.*$/, '').split('/').pop()!.replace(/:latest$/, '');
}

function reEscape(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
