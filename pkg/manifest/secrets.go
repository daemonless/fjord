package manifest

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"sort"
	"strings"
)

// MakeUpSecrets fills each secret MadeUp says fjord owns and values leaves
// empty, and names the ones it filled. Run after secrets kept with old data
// are put back, so a reinstall never locks the app out of its database.
func (m *Manifest) MakeUpSecrets(values map[string]string) []string {
	login := ""
	if m.Login != nil {
		login = strings.TrimSuffix(strings.TrimPrefix(m.Login.Password, "${"), "}")
	}
	var out []string
	for _, v := range m.Variables {
		if v.MadeUp() && values[v.Name] == "" {
			switch {
			case v.Name == login:
				values[v.Name] = loginSecret()
			case v.Format == "base64":
				values[v.Name] = base64Secret()
			default:
				values[v.Name] = hexSecret()
			}
			out = append(out, v.Name)
		}
	}
	sort.Strings(out)
	return out
}

// hexSecret is 32 random bytes as 64 hex characters: the shape the apps that
// ask for one want (`openssl rand -hex 32`: sparkyfitness's encryption key,
// garage's RPC secret), and plain letters and digits for every password.
func hexSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system's randomness is gone; nothing sane to do
	}
	return hex.EncodeToString(b)
}

// base64Secret is 32 random bytes as 44 standard base64 characters
// (`openssl rand -base64 32`), for apps that base64-decode a 32-byte key.
func base64Secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// loginChars: letters and digits a person reads back without a doubt (no
// 0/O, 1/l/I).
const loginChars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// loginSecret is the password a person signs in with, which they may have
// to type (ssh, a TV's app): 20 of loginChars, about 116 bits, where
// hexSecret's 64 characters are for machines.
func loginSecret() string {
	// A byte at or above the last whole multiple of len(loginChars) is
	// drawn again: taken modulo, it would favour the first characters.
	limit := 256 - 256%len(loginChars)
	out := make([]byte, 0, 20)
	b := make([]byte, 32)
	for len(out) < cap(out) {
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		for _, c := range b {
			if int(c) < limit && len(out) < cap(out) {
				out = append(out, loginChars[int(c)%len(loginChars)])
			}
		}
	}
	return string(out)
}
