package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	composepkg "github.com/daemonless/fjord/pkg/compose"
	"github.com/daemonless/fjord/pkg/engine"
	"github.com/daemonless/fjord/pkg/stack"

	"gopkg.in/yaml.v3"
)

// A name asked for at install is fjordd's to give, after the stack is up.
// The page used to send the request itself once the install's stream
// ended, so it was given only while that page was still open: close the
// tab or move on to the next install and the app came up with no name and
// nothing saying so. Now the request carries the name, the daemon gives it
// after the stay-up check, and the install's Output says what came of it,
// where it stays for any page opened later.

// nameAfter streams out, then gives the stack the name asked for and
// appends the outcome. Without a name it is the stream unchanged.
func (s *server) nameAfter(ctx context.Context, st *stack.Stack, label, origin string, out io.ReadCloser) io.ReadCloser {
	if strings.TrimSpace(label) == "" {
		return out
	}
	return &thenReader{r: out, c: out, after: func() string { return s.giveNameNow(ctx, st, label, origin) }}
}

// giveNameNow gives one stack the name asked for at install and returns the
// Output line about it. The stack is read back first: keepAfter may have
// pinned an address into its compose by now, and the engine's status says
// whether it is running at all. A stack that is not running gets no name,
// the Output already says why not.
func (s *server) giveNameNow(ctx context.Context, st *stack.Stack, label, origin string) string {
	p, err := s.findProvider(ctx)
	if err != nil || p == nil {
		return noNameLine(st, cmpErr(err, "nothing installed gives names"))
	}
	if cur, err := s.manager.Get(st.Name); err == nil {
		st = cur
	}
	status, err := s.backendFor(st).Status(ctx, st)
	if err != nil {
		return noNameLine(st, "could not read its status: "+err.Error())
	}
	if status.State != "running" && status.State != "partial" {
		return noNameLine(st, "it is not running")
	}
	n, ok := installNameRequest(st, status, label, origin, ownAddress)
	if !ok {
		return noNameLine(st, "it has no page to open")
	}
	n.ownOrigin = wantsOwnOrigin(st)
	n.icon = stackIcon(st)
	domain := ""
	if ps, _, err := p.status(ctx); err == nil {
		domain = ps.Domain
	}
	res := giveName(ctx, p, n, time.Sleep)
	if res.Host == nil {
		return noNameLine(st, cmpErr(nil, res.Error))
	}
	s.applyNameEnvLater(st.Name)
	if domain != "" {
		return fmt.Sprintf("[fjord] %s is at https://%s.%s\n", displayName(st), res.Host.Name, domain)
	}
	return fmt.Sprintf("[fjord] %s is at %s\n", displayName(st), res.Host.Name)
}

// giveName asks the provider, again while it cannot reach the app yet: the
// stay-up check saw the process, and a page can take longer than that.
// Any other refusal (a taken name, a provider that is not set up) is final.
func giveName(ctx context.Context, p *provider, n nameRequest, sleep func(time.Duration)) nameResult {
	var res nameResult
	for try := 0; try < 6; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return res
			default:
			}
			sleep(10 * time.Second)
		}
		res = p.name(ctx, n)
		if res.Host != nil || !strings.Contains(res.Error, "cannot reach") {
			return res
		}
	}
	return res
}

// installNameRequest is the name request for a stack just installed, by the
// same rule as Open: the app's own address and the port it listens on
// there when it has an address on a reachable network, else this host (the
// address the browser reached fjord at) and the port its page is published
// on. ok is false for a stack with no page.
func installNameRequest(st *stack.Stack, status engine.StackStatus, label, origin string, reachable func(string) bool) (nameRequest, bool) {
	n := nameRequest{Stack: st.Name, Label: label, HTTPS: webHTTPS(st)}
	views := stackServices(st, status)
	if addr := linkHost(views, reachable); addr != "" {
		port := containerWebPort(st)
		if port == 0 {
			return n, false
		}
		n.Address, n.Port = addr, port
		return n, true
	}
	port := webPortHint(st)
	if port == 0 {
		return n, false
	}
	n.Address, n.Port = originHost(origin), port
	return n, true
}

// containerWebPort is the port the app's page answers on inside its
// container: the container side of the published (or stashed) mapping
// whose host side is x-fjord.web_port; 0 when there is no such mapping.
func containerWebPort(st *stack.Stack) int {
	web := webPortHint(st)
	if web == 0 {
		return 0
	}
	for _, svc := range composepkg.ParseServices(st.Compose, st.EnvMap()) {
		for _, m := range append(append([]composepkg.PortMap{}, svc.Ports...), svc.Stashed...) {
			if m.Host == web && m.Container > 0 {
				return m.Container
			}
		}
	}
	return 0
}

// webHTTPS is the stack's x-fjord web_https: its page speaks HTTPS itself.
func webHTTPS(st *stack.Stack) bool {
	var x struct {
		XFjord struct {
			WebHTTPS bool `yaml:"web_https"`
		} `yaml:"x-fjord"`
	}
	return yaml.Unmarshal([]byte(st.Compose), &x) == nil && x.XFjord.WebHTTPS
}

// originHost is the host part of the address a browser reached fjord at,
// "" when unknown (the provider is then tried on this host's addresses).
func originHost(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func noNameLine(st *stack.Stack, why string) string {
	return fmt.Sprintf("[fjord] %s has no name yet: %s. Give it one from the menu beside Open.\n", displayName(st), why)
}

// displayName is what the person called the stack, else its id.
func displayName(st *stack.Stack) string {
	if st.State != nil && strings.TrimSpace(st.State.DisplayName) != "" {
		return st.State.DisplayName
	}
	return st.Name
}
