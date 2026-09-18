// Package registry lists the tags an image ref has published, using the
// standard OCI distribution API + anonymous-token challenge -- so it works
// against any registry (ghcr, Docker Hub, a private one), nothing hardcoded.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 20 * time.Second}

// Tags returns the raw tag list for an image repo, e.g.
// "ghcr.io/daemonless/radarr" -> ["latest","6.3.0.10514",...]. Registries
// that paginate (Docker Hub, quay, ghcr past ~1000 tags) send the next page
// in a Link header; every page is followed, so the newest tags aren't
// silently missing from update checks.
func Tags(ctx context.Context, image string) ([]string, error) {
	host, repo := splitImage(image)
	u := "https://" + host + "/v2/" + repo + "/tags/list"
	var tags []string
	for page := 0; u != "" && page < 100; page++ {
		body, hdr, err := getAuthed(ctx, u, repo)
		if err != nil {
			return nil, err
		}
		var out struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("decode tags: %w", err)
		}
		tags = append(tags, out.Tags...)
		u = nextLink(u, hdr.Get("Link"))
	}
	return tags, nil
}

var linkNextRe = regexp.MustCompile(`<([^>]+)>\s*;[^,]*rel="?next"?`)

// nextLink resolves the rel="next" target of a Link header against the page
// it came from (registries send it path-relative). "" when there is none.
func nextLink(current, link string) string {
	m := linkNextRe.FindStringSubmatch(link)
	if m == nil {
		return ""
	}
	base, err := url.Parse(current)
	if err != nil {
		return ""
	}
	next, err := base.Parse(m[1])
	if err != nil {
		return ""
	}
	return next.String()
}

// manifestAccept asks for the multi-arch index first, then a plain manifest.
// Requesting the index means Digest returns the *index* digest for multi-arch
// images (what podman actually pulls) rather than one arch's manifest digest.
const manifestAccept = "application/vnd.oci.image.index.v1+json," +
	"application/vnd.docker.distribution.manifest.list.v2+json," +
	"application/vnd.oci.image.manifest.v1+json," +
	"application/vnd.docker.distribution.manifest.v2+json"

// Digest resolves an image ref ("repo:tag", "repo", or "host/repo:tag") to the
// content digest podman would pull -- the index digest for multi-arch images,
// else the single manifest digest. Used to pin a stack to an exact image.
func Digest(ctx context.Context, image string) (string, error) {
	host, repo := splitImage(image)
	ref := "latest"
	if slash := strings.LastIndex(repo, "/"); slash >= 0 || !strings.Contains(repo, "/") {
		if colon := strings.LastIndex(repo, ":"); colon > slash {
			ref = repo[colon+1:]
			repo = repo[:colon]
		}
	}
	u := "https://" + host + "/v2/" + repo + "/manifests/" + ref
	dg, err := manifestDigest(ctx, u, repo)
	if err != nil {
		return "", err
	}
	if dg == "" {
		return "", fmt.Errorf("registry returned no digest for %s", image)
	}
	return dg, nil
}

// manifestDigest resolves a tag to its Docker-Content-Digest with a HEAD
// request (index Accept header, 401 token challenge followed). HEAD matters:
// Docker Hub counts GET /manifests as a pull against the anonymous quota
// (100 per 6h), and the fleet check runs across every stack every 30 min. A
// registry that answers HEAD without the digest header gets one GET retry.
func manifestDigest(ctx context.Context, u, repo string) (string, error) {
	dg, status, err := manifestHead(ctx, http.MethodHead, u, repo)
	if err == nil && dg == "" && (status == http.StatusOK || status == http.StatusMethodNotAllowed) {
		dg, _, err = manifestHead(ctx, http.MethodGet, u, repo)
	}
	return dg, err
}

func manifestHead(ctx context.Context, method, u, repo string) (digest string, status int, err error) {
	resp, err := doMethod(ctx, method, u, "", manifestAccept)
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		tok, err := token(ctx, challenge, repo)
		if err != nil {
			return "", 0, err
		}
		if resp, err = doMethod(ctx, method, u, tok, manifestAccept); err != nil {
			return "", 0, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusMethodNotAllowed && method == http.MethodHead {
		return "", resp.StatusCode, nil // caller retries with GET
	}
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("registry %s: %s", u, resp.Status)
	}
	return resp.Header.Get("Docker-Content-Digest"), resp.StatusCode, nil
}

// Version is one published pin within a train.
type Version struct {
	Version string `json:"version"`
	Tag     string `json:"tag"`
}

// Scheme is an image's tag scheme as its catalog entry declares it: every
// rolling channel tag the image publishes, and which of those are aliases
// following another channel. Passing one replaces guessing which tags are
// channels; nil infers the scheme from the tag list alone, which is all an
// adopted stack running some third-party image can do.
type Scheme struct {
	Channels []string          // every rolling tag, canonical and alias alike
	Aliases  map[string]string // alias tag -> the canonical channel it follows
}

// Key fingerprints a scheme for cache keying. Nil and empty are the same key.
func (s *Scheme) Key() string {
	if s == nil {
		return ""
	}
	ch := append([]string(nil), s.Channels...)
	sort.Strings(ch)
	al := make([]string, 0, len(s.Aliases))
	for a, c := range s.Aliases {
		al = append(al, a+"="+c)
	}
	sort.Strings(al)
	return strings.Join(ch, ",") + "|" + strings.Join(al, ",")
}

// Trains groups an image's published tags into release trains, keyed by the
// train's rolling tag (e.g. "latest", "pkg"), each with its pinned versions
// newest-first. sch is the image's declared scheme, or nil to discover one
// from the tags themselves -- channel tags vs "<version>-<channel>" pins.
func Trains(ctx context.Context, image string, sch *Scheme) (map[string][]Version, error) {
	tags, err := Tags(ctx, image)
	if err != nil {
		return nil, err
	}
	return discoverTrains(filterArchTags(tags), sch), nil
}

// Only amd64 and aarch64 are supported. archWords are the recognized arch tag
// suffixes; hostArchWords are the ones meaning "this host".
var archWords = map[string]bool{
	"amd64": true, "x86_64": true, "x64": true,
	"aarch64": true, "arm64": true,
}

func hostArchWords() map[string]bool {
	if runtime.GOARCH == "arm64" {
		return map[string]bool{"aarch64": true, "arm64": true}
	}
	return map[string]bool{"amd64": true, "x86_64": true, "x64": true}
}

// filterArchTags strips arch-suffixed noise from a version picker: drop the
// other arch entirely (e.g. "-aarch64" on amd64, not runnable here), and drop a
// host-arch tag ("1.3-amd64") when a plain multi-arch equivalent ("1.3") exists,
// since podman pulls the right arch from the multi-arch tag anyway. A host-arch
// tag with NO plain equivalent is kept so nothing installable is lost.
func filterArchTags(tags []string) []string {
	host := hostArchWords()
	plain := map[string]bool{} // tags with no recognized arch suffix
	for _, t := range tags {
		if i := strings.LastIndex(t, "-"); i < 0 || !archWords[t[i+1:]] {
			plain[t] = true
		}
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if i := strings.LastIndex(t, "-"); i >= 0 && archWords[t[i+1:]] {
			suf, base := t[i+1:], t[:i]
			if !host[suf] || plain[base] {
				continue // other arch, or redundant with a multi-arch tag
			}
		}
		out = append(out, t)
	}
	return out
}

func discoverTrains(tags []string, sch *Scheme) map[string][]Version {
	var clean []string
	for _, t := range tags {
		if t == "" || strings.HasPrefix(t, "sha256-") || strings.HasSuffix(t, ".att") || strings.HasSuffix(t, ".sig") {
			continue // cosign attestation/signature noise
		}
		clean = append(clean, t)
	}
	// A channel is a tag other tags pin against ("<version>-<channel>"), or a
	// tag with no version at all ("latest", "pkg"). The first rule matters for
	// images that publish one rolling channel per major -- redis's
	// "8.6-pkg-latest" with pins "8.6.6-8.6-pkg-latest": it starts with a digit
	// but is a channel, not a version, and its pins must not be pooled into
	// "pkg-latest" with every other major's (which made 8.6 offer an
	// "upgrade" to 8.10).
	isChannel := map[string]bool{}
	// A declared channel is a channel, full stop -- including one that starts
	// with a digit ("18") or that nothing ever pins against ("18-pkg", an
	// alias). Only tags the image actually publishes are seeded, so a channel
	// retired from the registry does not conjure an empty train.
	if sch != nil {
		published := map[string]bool{}
		for _, t := range clean {
			published[t] = true
		}
		for _, c := range sch.Channels {
			if published[c] {
				isChannel[c] = true
			}
		}
	}
	for _, t := range clean {
		if isChannel[t] {
			continue
		}
		if t[0] < '0' || t[0] > '9' {
			isChannel[t] = true
			continue
		}
		for _, other := range clean {
			if other != t && strings.HasSuffix(other, "-"+t) {
				isChannel[t] = true
				break
			}
		}
	}
	// Second rule: a tag that extends a channel is itself a channel, not a
	// version of it. postgres publishes a rolling "18" plus "18-pkg" and
	// "18-pkg-latest" -- the same channel built from FreeBSD packages. Nothing
	// is tagged "18.4-18-pkg" (the pins are "18.4-18-pkg-latest"), so the rule
	// above never sees "18-pkg" pinned against and files it as a VERSION of
	// the "pkg" train. That pooled every major's channel into one train and
	// offered a stack on 17-pkg an "upgrade" to 18-pkg: a major PostgreSQL
	// jump that will not start on an existing data directory.
	// Iterated so "18" promotes "18-pkg", which promotes nothing further; the
	// bound just stops a pathological tag set from spinning.
	//
	// This runs even with a declared scheme, and deliberately. A scheme
	// predating the aliases (a catalog fetched before they were published)
	// names "18" but not "18-pkg", and skipping the rule there would file
	// "18-pkg" as a *version* of the "pkg" train -- offering a stack on
	// 14-pkg an "upgrade" to 18-pkg, the exact major jump this all exists to
	// prevent. With a complete scheme the rule is a no-op: the aliases are
	// already seeded. Its cost is over-promoting a pre-release ("2.1-rc1"),
	// which only ever yields an empty train, and so no upgrade at all.
	for i := 0; i < 4; i++ {
		grew := false
		for _, t := range clean {
			if isChannel[t] {
				continue
			}
			for c := range isChannel {
				if strings.HasPrefix(t, c+"-") {
					isChannel[t] = true
					grew = true
					break
				}
			}
		}
		if !grew {
			break
		}
	}

	var channels, pins []string
	for _, t := range clean {
		if isChannel[t] {
			channels = append(channels, t)
		} else {
			pins = append(pins, t)
		}
	}
	// Longest channel first so "pkg-latest" beats "pkg" when suffix-matching.
	sort.Slice(channels, func(i, j int) bool { return len(channels[i]) > len(channels[j]) })

	out := map[string][]Version{}
	for _, ch := range channels {
		out[ch] = []Version{}
	}
	suffixUsed := map[string]bool{}
	var bare []Version
	for _, p := range pins {
		// Classify by the tag minus any arch suffix: a host-arch pin kept by
		// filterArchTags ("1.2-nightly-amd64", seen while CI has pushed the
		// per-arch tags but not yet the multi-arch one) belongs to its
		// channel, not to the default train as a "bare" version.
		base := p
		if i := strings.LastIndex(p, "-"); i >= 0 && archWords[p[i+1:]] {
			base = p[:i]
		}
		matched := false
		for _, ch := range channels {
			if strings.HasSuffix(base, "-"+ch) {
				out[ch] = append(out[ch], Version{Version: strings.TrimSuffix(base, "-"+ch), Tag: p})
				suffixUsed[ch] = true
				matched = true
				break
			}
		}
		if !matched {
			bare = append(bare, Version{Version: base, Tag: p})
		}
	}
	// Bare "<version>" pins belong to the default train: a channel that never
	// appears as a version suffix (typically "latest").
	if len(bare) > 0 {
		// "latest" when it exists: a bare "<version>" pin is a version of the
		// default channel, and picking whichever unused channel happens to
		// sort last put them in "18-pkg" once the per-major channels were
		// classified correctly.
		def := ""
		for _, ch := range channels {
			if ch == "latest" {
				def = ch
				break
			}
		}
		if def == "" {
			// Else the shortest channel nothing pins against: "pkg" before
			// "18-pkg-latest", since a bare pin is least specific.
			for i := len(channels) - 1; i >= 0; i-- {
				if !suffixUsed[channels[i]] {
					def = channels[i]
					break
				}
			}
		}
		if def == "" && len(channels) > 0 {
			def = channels[0]
		}
		if def != "" {
			out[def] = append(out[def], bare...)
		}
	}
	for k, vs := range out {
		sort.Slice(vs, func(i, j int) bool { return naturalLess(vs[j].Version, vs[i].Version) })
		out[k] = vs
	}
	// An alias is a second name for a channel, so nothing pins against it:
	// "18-pkg" is the same rolling tag as "18", and every pin is "<ver>-18".
	// Left alone it is an empty train, and a stack installed from :18-pkg is
	// offered no versions and so never an update. Give it the versions of the
	// channel it follows -- but only if it has none of its own, since an alias
	// carrying pins from a retired scheme ("17.7-pkg" under today's "pkg",
	// which now follows 18) would otherwise be offered a major-version jump.
	if sch != nil {
		for alias, canon := range sch.Aliases {
			if vs, ok := out[alias]; ok && len(vs) == 0 && len(out[canon]) > 0 {
				out[alias] = append([]Version(nil), out[canon]...) // own slice: trains are sorted in place
			}
		}
	}
	return out
}

// naturalLess compares strings with embedded numbers numerically ("6.9" < "6.10").
func naturalLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if isDigit(a[i]) && isDigit(b[j]) {
			an, ni := grabNum(a, i)
			bn, nj := grabNum(b, j)
			if an != bn {
				return an < bn
			}
			i, j = ni, nj
			continue
		}
		if a[i] != b[j] {
			return a[i] < b[j]
		}
		i++
		j++
	}
	return len(a)-i < len(b)-j
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func grabNum(s string, i int) (int, int) {
	n := 0
	for i < len(s) && isDigit(s[i]) {
		n = n*10 + int(s[i]-'0')
		i++
	}
	return n, i
}

// getAuthed does the GET, and if the registry answers 401 with a Bearer
// challenge, fetches an anonymous token from the advertised realm and retries.
// Returns the response headers too (pagination lives in Link).
func getAuthed(ctx context.Context, u, repo string) ([]byte, http.Header, error) {
	resp, err := do(ctx, u, "")
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		tok, err := token(ctx, challenge, repo)
		if err != nil {
			return nil, nil, err
		}
		if resp, err = do(ctx, u, tok); err != nil {
			return nil, nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("registry %s: %s", u, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return body, resp.Header, err
}

var challengeRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

func token(ctx context.Context, challenge, repo string) (string, error) {
	m := map[string]string{}
	for _, kv := range challengeRe.FindAllStringSubmatch(challenge, -1) {
		m[kv[1]] = kv[2]
	}
	realm := m["realm"]
	if realm == "" {
		return "", fmt.Errorf("registry gave no token realm")
	}
	scope := m["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	u := realm + "?scope=" + url.QueryEscape(scope)
	if s := m["service"]; s != "" {
		u += "&service=" + url.QueryEscape(s)
	}
	resp, err := do(ctx, u, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token: %s", resp.Status)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	json.Unmarshal(body, &t)
	if t.Token != "" {
		return t.Token, nil
	}
	return t.AccessToken, nil
}

func do(ctx context.Context, u, bearer string, accept ...string) (*http.Response, error) {
	return doMethod(ctx, http.MethodGet, u, bearer, accept...)
}

func doMethod(ctx context.Context, method, u, bearer string, accept ...string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if len(accept) > 0 && accept[0] != "" {
		req.Header.Set("Accept", accept[0])
	}
	return client.Do(req)
}

// splitImage splits "host/path/repo" into host + repo. A first segment with a
// "." or ":" (or "localhost") is the registry; otherwise it's a Docker Hub
// short name.
func splitImage(image string) (host, repo string) {
	s := strings.TrimSpace(image)
	i := strings.IndexByte(s, '/')
	if i < 0 || (!strings.ContainsAny(s[:i], ".:") && s[:i] != "localhost") {
		// Bare "alpine" / "user/app": Docker Hub's API host, not the website.
		if i < 0 {
			s = "library/" + s
		}
		return "registry-1.docker.io", s
	}
	host = s[:i]
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
	return host, s[i+1:]
}

// Repo returns an image ref's registry/repo, dropping any tag and @digest.
func Repo(image string) string {
	ref := image
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		ref = ref[:colon]
	}
	return ref
}
