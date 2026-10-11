package manifest

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
)

// MakeUpSecrets fills each secret MadeUp says fjord owns and values leaves
// empty, and names the ones it filled. Run after secrets kept with old data
// are put back, so a reinstall never locks the app out of its database.
func (m *Manifest) MakeUpSecrets(values map[string]string) []string {
	var out []string
	for _, v := range m.Variables {
		if v.MadeUp() && values[v.Name] == "" {
			values[v.Name] = hexSecret()
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
