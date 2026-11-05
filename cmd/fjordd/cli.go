package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/gorilla/websocket"
	"golang.org/x/term"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/updates"
)

var cliSubcommands = map[string]bool{
	"ls":       true,
	"status":   true,
	"up":       true,
	"down":     true,
	"restart":  true,
	"update":   true,
	"logs":     true,
	"rollback": true,
	"delete":   true,
	"install":  true,
	"catalog":  true,
	"prune":    true,
	"exec":     true,
	"help":     true,
}

// isCLIInvocation reports whether argv invokes the `fjord` CLI rather than the
// `fjordd` daemon: either argv[0]'s basename is "fjord", or argv[1] names one
// of the CLI subcommands.
func isCLIInvocation(args []string) bool {
	if len(args) > 0 && filepath.Base(args[0]) == "fjord" {
		return true
	}
	if len(args) > 1 && cliSubcommands[args[1]] {
		return true
	}
	return false
}

// cliClient drives fjordd over its local Unix domain socket (default) or an
// HTTP URL (when FJORD_URL is set).
type cliClient struct {
	http       *http.Client
	baseURL    string
	wsDialer   *websocket.Dialer
	wsURL      string
	session    string
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	execRunner func(stackName, container string) error
}

func newCLIClient(stdin io.Reader, stdout, stderr io.Writer) *cliClient {
	stacksDir := envOr("FJORD_STACKS_DIR", defaultStacksDir)
	fjordRoot := filepath.Dir(stacksDir)
	sockPath := envOr("FJORD_SOCKET", filepath.Join(fjordRoot, "fjordd.sock"))
	rawURL := strings.TrimRight(os.Getenv("FJORD_URL"), "/")
	session := strings.TrimSpace(os.Getenv("FJORD_SESSION"))

	if rawURL != "" {
		wsBase := "ws" + strings.TrimPrefix(rawURL, "http")
		return &cliClient{
			http:     &http.Client{},
			baseURL:  rawURL,
			wsDialer: &websocket.Dialer{},
			wsURL:    wsBase,
			session:  session,
			stdin:    stdin,
			stdout:   stdout,
			stderr:   stderr,
		}
	}

	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sockPath)
	}
	return &cliClient{
		http: &http.Client{
			Transport: &http.Transport{DialContext: dial},
		},
		baseURL: "http://localhost",
		wsDialer: &websocket.Dialer{
			NetDialContext: dial,
		},
		wsURL:   "ws://localhost",
		session: session,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
	}
}

func (c *cliClient) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.session != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.session})
	}
	return req, nil
}

func readHTTPError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	text := strings.TrimSpace(string(b))
	var obj struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &obj) == nil && obj.Error != "" {
		text = obj.Error
	}
	if text == "" {
		text = resp.Status
	}
	return errors.New(text)
}

func (c *cliClient) doJSON(ctx context.Context, method, path string, body, out any) error {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// doActionStream streams a lifecycle or install response to stdout line by
// line and returns an error if any `[error]` line was emitted by the daemon.
func (c *cliClient) doActionStream(ctx context.Context, method, path string, body any) error {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	watch := &errorWatch{}
	_, copyErr := io.Copy(io.MultiWriter(c.stdout, watch), resp.Body)
	if watch.seen {
		msg := watch.Message()
		if msg == "" {
			msg = "operation failed"
		}
		return errors.New(msg)
	}
	return copyErr
}

// doRawStream streams a plain response (such as container logs) verbatim to
// stdout without interpreting `[error]` lines in app log output.
func (c *cliClient) doRawStream(ctx context.Context, path string) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	_, err = io.Copy(c.stdout, resp.Body)
	return err
}

func runCLI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	client := newCLIClient(stdin, stdout, stderr)
	if err := client.run(context.Background(), args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(stderr, "fjord: %v\n", err)
		}
		return 1
	}
	return 0
}

func printCLIUsage(w io.Writer) {
	fmt.Fprintf(w, `fjord %s - FreeBSD & Linux container stack manager CLI

Usage:
  fjord <command> [flags] [args]

Commands:
  ls [--json]                                List stacks and their status
  status <stack> [--json]                    Show detailed stack and service status
  up <stack>                                 Bring a stack up
  down <stack>                               Stop and tear down a stack
  restart <stack>                            Restart a stack in place
  update <stack|--all> [svc...]              Pull and recreate a stack (or all stacks with updates)
  logs [-f] [--tail N] <stack> [svc...]      Stream container logs
  rollback <stack> [svc...]                  Roll back services to their previous image
  delete [--purge-data] [--yes] <stack>      Stop and remove a stack
  install <app> [flags]                      Install an app from the catalog
  catalog refresh                            Re-fetch all configured catalogs
  prune [--engine <engine>] [--all-images]   Reclaim unused container/image disk space
  exec <stack> [svc]                         Open an interactive shell in a service container
  passwd [--user NAME] [--password-stdin]    Set the web UI's user name and password
`, version)
}

func (c *cliClient) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printCLIUsage(c.stdout)
		return nil
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "-h", "--help", "help":
		printCLIUsage(c.stdout)
		return nil
	case "-v", "--version", "version":
		fmt.Fprintf(c.stdout, "fjord %s\n", version)
		return nil
	case "passwd":
		return runPasswd(rest, c.stdin, c.stdout)
	case "ls":
		return c.cmdLS(ctx, rest)
	case "status":
		return c.cmdStatus(ctx, rest)
	case "up":
		return c.cmdSimpleLifecycle(ctx, "up", rest)
	case "down":
		return c.cmdSimpleLifecycle(ctx, "down", rest)
	case "restart":
		return c.cmdSimpleLifecycle(ctx, "restart", rest)
	case "update":
		return c.cmdUpdate(ctx, rest)
	case "logs":
		return c.cmdLogs(ctx, rest)
	case "rollback":
		return c.cmdRollback(ctx, rest)
	case "delete":
		return c.cmdDelete(ctx, rest)
	case "install":
		return c.cmdInstall(ctx, rest)
	case "catalog":
		return c.cmdCatalog(ctx, rest)
	case "prune":
		return c.cmdPrune(ctx, rest)
	case "exec":
		return c.cmdExec(ctx, rest)
	default:
		return fmt.Errorf("unknown command %q (see `fjord --help`)", cmd)
	}
}

// resolveStack translates a stack id or display name into its stackWithStatus.
func (c *cliClient) resolveStack(ctx context.Context, nameOrID string) (stackWithStatus, error) {
	var detail stackWithStatus
	if err := c.doJSON(ctx, http.MethodGet, "/api/stacks/"+url.PathEscape(nameOrID), nil, &detail); err == nil {
		return detail, nil
	}
	var list []stackWithStatus
	if err := c.doJSON(ctx, http.MethodGet, "/api/stacks", nil, &list); err != nil {
		return detail, err
	}
	var matches []string
	for _, st := range list {
		if st.Stack != nil && strings.EqualFold(st.DisplayName, nameOrID) {
			matches = append(matches, st.Name)
		}
	}
	if len(matches) == 1 {
		if err := c.doJSON(ctx, http.MethodGet, "/api/stacks/"+url.PathEscape(matches[0]), nil, &detail); err == nil {
			return detail, nil
		}
	}
	if len(matches) > 1 {
		return detail, fmt.Errorf("multiple stacks named %q (%s): specify the stack id", nameOrID, strings.Join(matches, ", "))
	}
	return detail, fmt.Errorf("stack %q not found", nameOrID)
}

func (c *cliClient) cmdLS(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	var stacks []stackWithStatus
	if err := c.doJSON(ctx, http.MethodGet, "/api/stacks", nil, &stacks); err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(stacks)
	}
	var fleet fleetResponse
	_ = c.doJSON(ctx, http.MethodGet, "/api/updates", nil, &fleet)

	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tENGINE\tSTATE\tADDRESS\tUPDATES")
	for _, st := range stacks {
		if st.Stack == nil {
			continue
		}
		name := st.Name
		if st.DisplayName != "" && st.DisplayName != st.Name {
			name = fmt.Sprintf("%s (%s)", st.Name, st.DisplayName)
		}
		eng := st.EngineName()
		if eng == "" {
			eng = "-"
		}
		state := st.Status.State
		if st.Busy != "" {
			state = st.Busy
		}
		if st.State != nil && st.State.LastFailure != nil {
			state += " (failed)"
		}
		addr := stackAddressSummary(st)
		upd := "-"
		if u, ok := fleet.Stacks[st.Name]; ok && u.State != "" {
			upd = u.State
			if u.NewTag != "" {
				upd += " (" + u.NewTag + ")"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", name, eng, state, addr, upd)
	}
	return tw.Flush()
}

func stackAddressSummary(st stackWithStatus) string {
	if st.LinkHost != "" {
		return st.LinkHost
	}
	for _, svc := range st.Services {
		if svc.Address != "" {
			return svc.Address
		}
	}
	var ports []string
	for _, svc := range st.Services {
		for _, p := range svc.Ports {
			if p.HostPort > 0 {
				ports = append(ports, fmt.Sprintf(":%d->%d", p.HostPort, p.ContainerPort))
			}
		}
	}
	if len(ports) > 0 {
		return strings.Join(ports, ",")
	}
	if st.NetworkMode != "" {
		return st.NetworkMode
	}
	return "-"
}

func (c *cliClient) cmdStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	pos, err := parseInterspersedFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: fjord status <stack> [--json]")
	}
	st, err := c.resolveStack(ctx, pos[0])
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	label := st.Name
	if st.DisplayName != "" && st.DisplayName != st.Name {
		label = fmt.Sprintf("%s (%s)", st.DisplayName, st.Name)
	}
	state := st.Status.State
	if st.Busy != "" {
		state = fmt.Sprintf("%s (busy: %s)", state, st.Busy)
	}
	fmt.Fprintf(c.stdout, "Stack:   %s\n", label)
	fmt.Fprintf(c.stdout, "Engine:  %s\n", st.EngineName())
	fmt.Fprintf(c.stdout, "State:   %s\n", state)
	if st.Restart.Policy != "" {
		fmt.Fprintf(c.stdout, "Restart: %s\n", st.Restart.Policy)
	}
	if st.State != nil && st.State.LastFailure != nil {
		fmt.Fprintf(c.stdout, "Failure: [%s] %s\n", st.State.LastFailure.Action, st.State.LastFailure.Message)
	}
	if len(st.Services) > 0 {
		fmt.Fprintln(c.stdout)
		tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SERVICE\tCONTAINER\tSTATE\tIMAGE\tNETWORK/PORTS")
		for _, svc := range st.Services {
			ctr := svc.Container
			if ctr == "" {
				ctr = "-"
			}
			stState := svc.State
			if stState == "" {
				stState = "stopped"
			}
			if svc.Detail != "" {
				stState += " (" + svc.Detail + ")"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				svc.Name, ctr, stState, svc.Image, serviceNetSummary(svc))
		}
		tw.Flush()
	}
	return nil
}

func serviceNetSummary(svc serviceView) string {
	var parts []string
	if svc.HostNet {
		parts = append(parts, "host")
	} else if svc.NoNet {
		parts = append(parts, "none")
	}
	for _, n := range svc.Networks {
		ip := svc.Live[n.Network]
		if ip == "" {
			ip = n.IP
		}
		if ip != "" {
			parts = append(parts, fmt.Sprintf("%s:%s", n.Network, ip))
		} else {
			parts = append(parts, n.Network)
		}
	}
	for _, p := range svc.Ports {
		if p.HostPort > 0 {
			parts = append(parts, fmt.Sprintf(":%d->%d/%s", p.HostPort, p.ContainerPort, p.Protocol))
		}
	}
	if len(parts) == 0 {
		return "bridge"
	}
	return strings.Join(parts, ", ")
}

func (c *cliClient) cmdSimpleLifecycle(ctx context.Context, action string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: fjord %s <stack>", action)
	}
	st, err := c.resolveStack(ctx, args[0])
	if err != nil {
		return err
	}
	return c.doActionStream(ctx, http.MethodPost, "/api/stacks/"+url.PathEscape(st.Name)+"/"+action, nil)
}

func (c *cliClient) cmdUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	all := fs.Bool("all", false, "update all stacks that have available updates")
	pos, err := parseInterspersedFlags(fs, args)
	if err != nil {
		return err
	}
	if *all {
		if len(pos) > 0 {
			return errors.New("usage: fjord update --all")
		}
		return c.updateAllStacks(ctx)
	}
	if len(pos) < 1 {
		return errors.New("usage: fjord update <stack|--all> [svc...]")
	}
	st, err := c.resolveStack(ctx, pos[0])
	if err != nil {
		return err
	}
	var body any
	if len(pos) > 1 {
		body = map[string][]string{"services": pos[1:]}
	}
	return c.doActionStream(ctx, http.MethodPost, "/api/stacks/"+url.PathEscape(st.Name)+"/update", body)
}

func (c *cliClient) updateAllStacks(ctx context.Context) error {
	var stacks []stackWithStatus
	if err := c.doJSON(ctx, http.MethodGet, "/api/stacks", nil, &stacks); err != nil {
		return err
	}
	var targets []string
	for _, st := range stacks {
		if st.Stack == nil {
			continue
		}
		var u updates.Status
		if err := c.doJSON(ctx, http.MethodGet, "/api/stacks/"+url.PathEscape(st.Name)+"/update-check", nil, &u); err != nil {
			return fmt.Errorf("check %s: %w", st.Name, err)
		}
		if u.State == "available" {
			targets = append(targets, st.Name)
		}
	}
	if len(targets) == 0 {
		fmt.Fprintln(c.stdout, "All stacks are up to date.")
		return nil
	}
	var failed []string
	for _, name := range targets {
		fmt.Fprintf(c.stdout, "==> Updating %s\n", name)
		if err := c.doActionStream(ctx, http.MethodPost, "/api/stacks/"+url.PathEscape(name)+"/update", nil); err != nil {
			fmt.Fprintf(c.stderr, "fjord: update %s: %v\n", name, err)
			failed = append(failed, name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("update failed for: %s", strings.Join(failed, ", "))
	}
	return nil
}

func (c *cliClient) cmdLogs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	follow := fs.Bool("f", false, "follow log output")
	tail := fs.Int("tail", 200, "number of lines from the end of the logs")
	pos, err := parseInterspersedFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return errors.New("usage: fjord logs [-f] [--tail N] <stack> [svc...]")
	}
	st, err := c.resolveStack(ctx, pos[0])
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("tail", strconv.Itoa(*tail))
	if *follow {
		q.Set("follow", "1")
	}
	for _, svcOrCtr := range pos[1:] {
		ctr := resolveContainerForService(st, svcOrCtr)
		q.Add("container", ctr)
	}
	return c.doRawStream(ctx, "/api/stacks/"+url.PathEscape(st.Name)+"/logs?"+q.Encode())
}

func resolveContainerForService(st stackWithStatus, svcOrCtr string) string {
	for _, c := range st.Status.Containers {
		if c.Name == svcOrCtr {
			return c.Name
		}
	}
	for _, c := range st.Status.Containers {
		if c.Service == svcOrCtr && c.Name != "" {
			return c.Name
		}
	}
	for _, svc := range st.Services {
		if svc.Name == svcOrCtr && svc.Container != "" {
			return svc.Container
		}
	}
	return svcOrCtr
}

func (c *cliClient) cmdRollback(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: fjord rollback <stack> [svc...]")
	}
	st, err := c.resolveStack(ctx, args[0])
	if err != nil {
		return err
	}
	services := args[1:]
	if len(services) == 0 {
		if st.State != nil && len(st.State.Rollback) > 0 {
			for svc := range st.State.Rollback {
				services = append(services, svc)
			}
			slices.Sort(services)
		}
		if len(services) == 0 {
			return fmt.Errorf("%s has no recorded rollback image", st.Name)
		}
	}
	body := map[string][]string{"services": services}
	return c.doActionStream(ctx, http.MethodPost, "/api/stacks/"+url.PathEscape(st.Name)+"/rollback", body)
}

func (c *cliClient) cmdDelete(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	purgeData := fs.Bool("purge-data", false, "also delete the stack's app-data directories")
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	yesShort := fs.Bool("y", false, "skip confirmation prompt (shorthand)")
	pos, err := parseInterspersedFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: fjord delete [--purge-data] [--yes] <stack>")
	}
	st, err := c.resolveStack(ctx, pos[0])
	if err != nil {
		return err
	}
	if !*yes && !*yesShort {
		prompt := fmt.Sprintf("Delete stack %s?", st.Name)
		if *purgeData {
			prompt = fmt.Sprintf("Delete stack %s AND permanently remove its app data?", st.Name)
		}
		fmt.Fprintf(c.stdout, "%s [y/N]: ", prompt)
		line, _ := bufio.NewReader(c.stdin).ReadString('\n')
		ans := strings.ToLower(strings.TrimSpace(line))
		if ans != "y" && ans != "yes" {
			return errors.New("aborted")
		}
	}
	path := "/api/stacks/" + url.PathEscape(st.Name)
	if *purgeData {
		path += "?data=1"
	}
	if err := c.doJSON(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "Deleted %s\n", st.Name)
	return nil
}

type kvFlag []string

func (f *kvFlag) String() string { return strings.Join(*f, ",") }
func (f *kvFlag) Set(v string) error {
	if !strings.Contains(v, "=") {
		return fmt.Errorf("expected KEY=VAL, got %q", v)
	}
	*f = append(*f, v)
	return nil
}

func (c *cliClient) cmdInstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	name := fs.String("name", "", "display/stack name")
	eng := fs.String("engine", "", "runtime engine (podman|appjail)")
	catalogID := fs.String("catalog", "", "catalog source id")
	appData := fs.String("app-data", "", "App data root folder")
	tag := fs.String("tag", "", "image tag or train to deploy")
	netName := fs.String("network", "", "network to attach to (or host|bridge|none)")
	ipAddr := fs.String("ip", "", "pinned IPv4 address on --network")
	macAddr := fs.String("mac", "", "pinned MAC address on --network")
	givenName := fs.String("given-name", "", "local DNS name to assign (or 'none' to skip)")
	var sets kvFlag
	var choices kvFlag
	fs.Var(&sets, "set", "override variable KEY=VAL (repeatable)")
	fs.Var(&choices, "choice", "select choice ID=OPTION (repeatable)")

	pos, err := parseInterspersedFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: fjord install <app> [--name NAME] [--engine ENGINE] [--network NET] [--ip IP] [--set KEY=VAL...] [--choice ID=OPT...]")
	}
	appID := pos[0]

	q := url.Values{}
	if *eng != "" {
		q.Set("engine", *eng)
	}
	if *catalogID != "" {
		q.Set("catalog", *catalogID)
	}
	if *name != "" {
		q.Set("name", *name)
	}
	if *appData != "" {
		q.Set("appData", *appData)
	}
	for _, ch := range choices {
		q.Add("choice", ch)
	}
	planPath := "/api/apps/" + url.PathEscape(appID) + "/plan"
	if enc := q.Encode(); enc != "" {
		planPath += "?" + enc
	}
	var plan installPlan
	if err := c.doJSON(ctx, http.MethodGet, planPath, nil, &plan); err != nil {
		return err
	}
	req := plan.installRequest
	if req.Values == nil {
		req.Values = map[string]string{}
	}
	for _, kv := range sets {
		k, v, _ := strings.Cut(kv, "=")
		req.Values[k] = v
		if req.Paths != nil {
			if _, isPath := req.Paths[k]; isPath {
				req.Paths[k] = []string{v}
			}
		}
	}
	if *tag != "" {
		req.Tag = *tag
	}
	if *givenName != "" {
		if *givenName == "none" {
			req.GivenName = ""
		} else {
			req.GivenName = *givenName
		}
	}
	if *netName != "" || *ipAddr != "" || *macAddr != "" {
		applyInstallCLIOverride(&req, plan.Interfaces, *netName, *ipAddr, *macAddr)
	}
	return c.doActionStream(ctx, http.MethodPost, "/api/apps/install", req)
}

// applyInstallCLIOverride applies --network / --ip / --mac overrides to the
// daemon's install plan.
func applyInstallCLIOverride(req *installRequest, planIfaces map[string][]planIface, netName, ipAddr, macAddr string) {
	if composepkg.BuiltIn(netName) {
		req.Network = netName
		req.IP = ""
		req.MAC = ""
		req.Networks = nil
		req.NetworkModes = nil
		return
	}
	if len(req.Networks) == 0 && len(req.NetworkModes) == 0 {
		if netName != "" {
			req.Network = netName
		}
		if ipAddr != "" {
			req.IP = ipAddr
		}
		if macAddr != "" {
			req.MAC = macAddr
		}
		return
	}
	// Per-service plan: update the primary service's first non-private interface
	// (or its single built-in mode row when switching to a named network).
	var svcs []string
	for svc := range planIfaces {
		svcs = append(svcs, svc)
	}
	slices.Sort(svcs)
	updated := false
	for _, svc := range svcs {
		rows := planIfaces[svc]
		for i := range rows {
			if rows[i].Network != "private" && !updated {
				if netName != "" {
					rows[i].Network = netName
				}
				if ipAddr != "" {
					rows[i].IP = ipAddr
				}
				if macAddr != "" {
					rows[i].MAC = macAddr
				}
				planIfaces[svc] = rows
				updated = true
				break
			}
		}
		if updated {
			break
		}
	}
	req.Networks, req.NetworkModes = splitPlanInterfaces(planIfaces, svcs)
	req.Network = ""
}

func (c *cliClient) cmdCatalog(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "refresh" {
		return errors.New("usage: fjord catalog refresh")
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/catalog/refresh", map[string]string{}, nil); err != nil {
		return err
	}
	fmt.Fprintln(c.stdout, "Catalogs refreshed.")
	return nil
}

func (c *cliClient) cmdPrune(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	eng := fs.String("engine", "", "engine to prune (default: host default engine)")
	allImages := fs.Bool("all-images", false, "prune all unused images (not only dangling)")
	volumes := fs.Bool("volumes", false, "also prune unused volumes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	payload := struct {
		Engine string `json:"engine,omitempty"`
		engine.PruneOptions
	}{
		Engine: *eng,
		PruneOptions: engine.PruneOptions{
			Containers: true,
			Images:     true,
			AllImages:  *allImages,
			Volumes:    *volumes,
			Networks:   true,
			Build:      true,
		},
	}
	var report engine.PruneReport
	if err := c.doJSON(ctx, http.MethodPost, "/api/maintenance/prune", payload, &report); err != nil {
		return err
	}
	if strings.TrimSpace(report.Output) != "" {
		fmt.Fprintln(c.stdout, strings.TrimRight(report.Output, "\n"))
	}
	if report.Reclaimed != "" {
		fmt.Fprintf(c.stdout, "Reclaimed: %s\n", report.Reclaimed)
	}
	return nil
}

func (c *cliClient) cmdExec(ctx context.Context, args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: fjord exec <stack> [svc]")
	}
	st, err := c.resolveStack(ctx, args[0])
	if err != nil {
		return err
	}
	if len(st.Status.Containers) == 0 {
		return fmt.Errorf("%s has no running containers", st.Name)
	}
	container := st.Status.Containers[0].Name
	if len(args) == 2 {
		container = resolveContainerForService(st, args[1])
	}
	if c.execRunner != nil {
		return c.execRunner(st.Name, container)
	}
	return c.runExecSession(ctx, st.Name, container)
}

func (c *cliClient) runExecSession(ctx context.Context, stackName, container string) error {
	q := url.Values{}
	q.Set("container", container)
	wsEndpoint := fmt.Sprintf("%s/api/stacks/%s/exec?%s", c.wsURL, url.PathEscape(stackName), q.Encode())
	headers := http.Header{}
	if c.session != "" {
		headers.Set("Cookie", (&http.Cookie{Name: sessionCookie, Value: c.session}).String())
	}
	conn, resp, err := c.wsDialer.DialContext(ctx, wsEndpoint, headers)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			return readHTTPError(resp)
		}
		return err
	}
	defer conn.Close()

	// Put stdin into raw mode when attached to a real terminal.
	var restore func()
	if f, ok := c.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fd := int(f.Fd())
		if oldState, err := term.MakeRaw(fd); err == nil {
			restore = func() { _ = term.Restore(fd, oldState) }
			defer restore()
		}
		if cols, rows, err := term.GetSize(fd); err == nil && cols > 0 && rows > 0 {
			resizeJSON, _ := json.Marshal(struct{ Cols, Rows int }{Cols: cols, Rows: rows})
			_ = conn.WriteMessage(websocket.BinaryMessage, append([]byte{'1'}, resizeJSON...))
		}
	}

	done := make(chan error, 1)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if len(data) > 0 {
				_, _ = c.stdout.Write(data)
			}
			if err != nil {
				var closeErr *websocket.CloseError
				if errors.As(err, &closeErr) && (closeErr.Code == websocket.CloseNormalClosure || closeErr.Code == websocket.CloseGoingAway) {
					done <- nil
					return
				}
				if errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "normal") {
					done <- nil
					return
				}
				done <- nil
				return
			}
		}
	}()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := c.stdin.Read(buf)
			if n > 0 {
				frame := make([]byte, n+1)
				frame[0] = '0'
				copy(frame[1:], buf[:n])
				if werr := conn.WriteMessage(websocket.BinaryMessage, frame); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	return <-done
}

// parseInterspersedFlags parses flags and positional arguments in any order
// (so both `fjord status mystack --json` and `fjord status --json mystack` work).
func parseInterspersedFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	var flagArgs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		flagArgs = append(flagArgs, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	if err := fs.Parse(flagArgs); err != nil {
		return nil, err
	}
	pos = append(pos, fs.Args()...)
	return pos, nil
}
