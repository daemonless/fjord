package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// The cni-epair plugin fjord installs, pinned by version and checksum. It is
// not in the FreeBSD ports tree, so fjord offers to put it in place itself;
// fjordd runs as root, so what it writes must be exactly this file.
// Bump all three together when a new cni-epair is released.
var (
	epairVersion = "v1.1.1"
	epairSHA256  = "edf36de370a42859eb3d027e0db8f26eb63aef0fffa8331a5f8b6fc4aabe2d3e"
	epairURL     = "https://raw.githubusercontent.com/daemonless/cni-epair/" + epairVersion + "/epair"
	epairDest    = "/usr/local/libexec/cni/epair"
)

// installEpair downloads the pinned plugin, checks it byte for byte against
// its checksum, and moves it into place in one rename: a failed or tampered
// download never replaces a working plugin.
func installEpair(ctx context.Context, w io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	fmt.Fprintf(w, "$ fetch -o %s %s\n", epairDest, epairURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, epairURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download cni-epair %s: %w", epairVersion, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download cni-epair %s: %s", epairVersion, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("download cni-epair %s: %w", epairVersion, err)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != epairSHA256 {
		return fmt.Errorf("cni-epair %s did not match its checksum (got %s) -- not installed", epairVersion, got)
	}
	fmt.Fprintf(w, "%d bytes, sha256 %s: matches %s\n$ chmod 755 %s\n", len(body), epairSHA256, epairVersion, epairDest)
	dir := filepath.Dir(epairDest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".epair-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), epairDest)
}

// epairCommands is what installEpair does, as the commands a person would
// type: fjordd does the same in Go, and refuses a download that does not
// match the pinned checksum.
func epairCommands() []string {
	return []string{
		"fetch -o " + epairDest + " " + epairURL,
		"# refused unless its sha256 is " + epairSHA256,
		"chmod 755 " + epairDest,
	}
}
