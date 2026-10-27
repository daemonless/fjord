// How strong a new password looks, Fedora-style: the page says so and
// warns about a weak one, but the person decides. Rough on purpose: length
// counts most, then the obvious traps (common passwords, runs, repeats).

export type Strength = { level: 0 | 1 | 2 | 3 | 4; label: string; why: string }

// The most used passwords (public breach lists), plus this app's own words.
const common = new Set(`123456 123456789 12345678 password qwerty123 qwerty1 111111 12345 123123
1234567 1234 1234567890 000000 555555 666666 123321 654321 7777777 123 777777 1111 987654321
121212 abc123 112233 azerty 159753 1q2w3e4r 54321 pass@123 222222 qwertyuiop qwerty123456 0123456789
1q2w3e 123qwe 1qaz2wsx 11111111 1234qwer aa123456 asdfghjkl 88888888 qwer1234 iloveyou password1 admin welcome
monkey dragon letmein football baseball sunshine princess master shadow superman michael trustno1 starwars
passw0rd qazwsx 123abc zaq12wsx 1qazxsw2 password123 changeme default secret hello hello123 freedom whatever
login abcdef abcd1234 computer internet killer charlie jordan23 batman maggie hunter cheese summer winter
fjord fjordd daemonless homelab freebsd`.split(/\s+/))

const rows = ['abcdefghijklmnopqrstuvwxyz', '0123456789', 'qwertyuiop', 'asdfghjkl', 'zxcvbnm']

function isRun(pw: string): boolean {
  const p = pw.toLowerCase()
  if (p.length < 4) return false
  if (new Set(p).size <= 2 || /^(.+)\1+$/.test(p)) return true
  return rows.some((r) => r.includes(p) || [...r].reverse().join('').includes(p))
}

export function strength(pw: string): Strength {
  if (!pw) return { level: 0, label: '', why: '' }
  const bare = pw.toLowerCase().replace(/[^a-z0-9]/g, '')
  if (common.has(pw.toLowerCase()) || common.has(bare) || common.has(bare.replace(/\d+$/, '')))
    return { level: 1, label: 'Weak', why: 'It is one of the most common passwords.' }
  if (isRun(pw)) return { level: 1, label: 'Weak', why: 'Runs and repeats are guessed first.' }
  const kinds = [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/].filter((r) => r.test(pw)).length
  const words = pw.trim().split(/\s+/).length
  if (pw.length < 8) return { level: 1, label: 'Weak', why: 'Short passwords are guessed quickly.' }
  if (pw.length >= 16 || (pw.length >= 12 && (kinds >= 2 || words >= 3))) return { level: 4, label: 'Strong', why: '' }
  if (pw.length >= 12 || (pw.length >= 10 && kinds >= 3)) return { level: 3, label: 'Good', why: '' }
  return { level: 2, label: 'Fair', why: 'Longer is stronger: a short sentence is easy to remember.' }
}
