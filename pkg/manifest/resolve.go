package manifest

import (
	"fmt"
	composepkg "github.com/daemonless/fjord/pkg/compose"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProvisionDir is a host directory to create + chown before the stack starts.
type ProvisionDir struct {
	Path     string
	Uid, Gid int
	Mode     os.FileMode
}

// Resolved is the outcome of turning wizard input into concrete values.
type Resolved struct {
	Env           map[string]string // VAR -> value, written to .env
	Dirs          []ProvisionDir    // host dirs to mkdir + chown
	EmptyOptional []string          // optional vars left empty; drop their ${VAR} volume mounts
}

// Resolve maps user-supplied values (falling back to defaults) to .env entries
// and the host directories to provision:
//   - zfs_dataset: managed storage. A relative name becomes <storageBase>/<slug>/
//     <name> (a human, discoverable path like /containers/tautulli/config); an
//     absolute value is used verbatim (point a stack at existing data). Queued for
//     mkdir + chown -- but a pre-existing dir is ADOPTED (Provision won't touch its
//     ownership), so migrating in place is just naming the stack to match the dir.
//   - path: must be absolute; a required-but-empty path is an error; an empty
//     optional path is recorded so its volume mount can be dropped.
//   - port/string/secret: passed through; required-but-empty is an error.
func (m *Manifest) Resolve(values map[string]string, slug, storageBase string) (*Resolved, error) {
	res := &Resolved{Env: map[string]string{}}
	for _, v := range m.Variables {
		val := v.Default
		if got, ok := values[v.Name]; ok && got != "" {
			val = got
		} else if v.Given {
			// Only its owner has it, so the catalog's default is a
			// placeholder: cloudflared's YOUR_CLOUDFLARE_TOKEN_HERE was
			// installed and crash-looped with nothing said.
			val = ""
		} else {
			// A DEFAULT that is still a compose reference is not a value. A
			// manifest derived from a variabilized compose can carry
			// "${GARAGE_ZONE:-dc1}" as the default, and writing that into the
			// .env verbatim gave the container a zone literally named
			// ${GARAGE_ZONE:-dc1} -- an install that looks clean and runs
			// wrong. Resolved the way compose would, against nothing, so the
			// fallback wins and a bare ${VAR} becomes empty.
			//
			// Only the default. What the operator typed is theirs, even if it
			// looks like a reference.
			val = composepkg.ExpandEnv(val, nil)
		}

		switch v.Type {
		case "zfs_dataset":
			var abs string
			if filepath.IsAbs(val) {
				// explicit override: bind an existing/chosen host path -- one
				// that names where it goes ("/containers/x/../../etc" does not)
				if strings.Contains("/"+val+"/", "/../") {
					return nil, fmt.Errorf("%s must not climb out with .., got %q", v.Name, val)
				}
				abs = filepath.Clean(val)
			} else {
				name := val
				if name == "" {
					name = v.Name
				}
				// A relative name is ONE folder under the app's own dir; it must
				// not climb out of it ("../../root") -- these are created and
				// chowned as root.
				if filepath.Clean(name) != name || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
					return nil, fmt.Errorf("dataset %q must be a plain folder name or an absolute path, got %q", v.Name, name)
				}
				abs = filepath.Join(storageBase, slug, name)
			}
			uid, gid := v.Uid, v.Gid
			if uid == 0 && gid == 0 {
				uid, gid = 1000, 1000
			}
			mode := os.FileMode(0o755)
			if v.Mode != "" {
				if p, err := strconv.ParseUint(v.Mode, 8, 32); err == nil {
					mode = os.FileMode(p)
				}
			}
			res.Dirs = append(res.Dirs, ProvisionDir{Path: abs, Uid: uid, Gid: gid, Mode: mode})
			res.Env[v.Name] = abs

		case "path":
			if val == "" {
				if v.Optional {
					res.Env[v.Name] = ""
					res.EmptyOptional = append(res.EmptyOptional, v.Name)
					continue
				}
				return nil, fmt.Errorf("required path %q is empty", v.Name)
			}
			if !filepath.IsAbs(val) {
				return nil, fmt.Errorf("path %q must be absolute, got %q", v.Name, val)
			}
			res.Env[v.Name] = val

		case "public_url":
			if val == "" && !v.Optional {
				return nil, fmt.Errorf("required value %q is empty", v.Name)
			}
			if val != "" {
				if u, err := url.Parse(val); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					return nil, fmt.Errorf("%s must be an address like http://host:port, got %q", v.Name, val)
				}
			}
			res.Env[v.Name] = val

		default: // port, string, secret
			if val == "" && !v.Optional && v.Given {
				return nil, fmt.Errorf("%s is empty: it is yours to enter (fjord does not make it up)", v.Name)
			}
			if val == "" && !v.Optional {
				return nil, fmt.Errorf("required value %q is empty", v.Name)
			}
			// An optional port left blank must not stay as "${VAR}:443" in the
			// compose; record it so the install drops that publish line.
			if val == "" && v.Type == "port" {
				res.EmptyOptional = append(res.EmptyOptional, v.Name)
			}
			res.Env[v.Name] = val
		}
	}
	return res, nil
}

// Provision creates each resolved directory with its ownership/mode. It is
// idempotent: existing dirs are re-chowned, not failed on.
func Provision(dirs []ProvisionDir) error {
	for _, d := range dirs {
		// Adopt an existing dir as-is: don't chown someone's migrated data out
		// from under them. Only freshly-created levels get the ownership spec,
		// and never through a link a container could have planted.
		if _, err := MkdirOwned(d.Path, d.Mode, d.Uid, d.Gid); err != nil {
			return err
		}
	}
	return nil
}
