package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// runPasswd is `fjordd passwd`: sets the account's password (and user name)
// on the host, as root, and signs every browser out. Before the first start
// it creates the account, so there is no setup page. The daemon need not be
// running; a running one picks the change up.
func runPasswd(args []string, stdin io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("passwd", flag.ContinueOnError)
	fs.SetOutput(out)
	user := fs.String("user", "", "user name (default: keep the current one, or admin)")
	fromStdin := fs.Bool("password-stdin", false, "read the password from stdin (one line) instead of asking")
	stacksDir := fs.String("stacks-dir", envOr("FJORD_STACKS_DIR", defaultStacksDir), "the daemon's stacks directory (its parent holds auth.json)")
	fs.Usage = func() {
		fmt.Fprintf(out, "Usage: fjordd passwd [flags]\n\nSets the web UI's user name and password and signs every browser out.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The daemon's FJORD_STACKS_DIR (rc.conf, systemd) is not in a root
	// shell: writing beside no stacks dir is most likely the wrong place.
	if st, err := os.Stat(*stacksDir); err != nil || !st.IsDir() {
		fmt.Fprintf(out, "warning: %s does not exist; if fjordd keeps its stacks elsewhere, run this again with --stacks-dir\n", *stacksDir)
	}
	store := newAuthStore(filepath.Dir(*stacksDir))
	f, err := store.read()
	if err != nil {
		return err
	}
	name := strings.TrimSpace(*user)
	if name == "" {
		name = f.User
	}
	if name == "" {
		name = "admin"
	}

	var password string
	if *fromStdin {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		password = strings.TrimRight(line, "\r\n")
	} else {
		fd := int(os.Stdin.Fd())
		if !term.IsTerminal(fd) {
			return errors.New("no terminal to ask on: use --password-stdin")
		}
		fmt.Fprintf(out, "New password for %s: ", name)
		p1, err := term.ReadPassword(fd)
		fmt.Fprintln(out)
		if err != nil {
			return err
		}
		fmt.Fprint(out, "Again: ")
		p2, err := term.ReadPassword(fd)
		fmt.Fprintln(out)
		if err != nil {
			return err
		}
		if string(p1) != string(p2) {
			return errors.New("the two passwords differ; nothing changed")
		}
		password = string(p1)
	}
	if err := store.setAccount(name, password, "", "host", false); err != nil {
		return err
	}
	store.removeSetupCode()
	fmt.Fprintf(out, "Password set for %s in %s; every browser is signed out.\n", name, store.path)
	return nil
}
