package stack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// stateSchemaVersion is written into every state.json we persist.
const stateSchemaVersion = 1

// validName matches safe stack names -- rejects "..", "/", and anything
// else that could escape StacksDir when joined into a filesystem path.
var validName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ErrInvalidName is returned when a stack name fails validName.
var ErrInvalidName = fmt.Errorf("invalid stack name")

// ValidName reports whether name is safe to use as a stack directory / volume
// path component (no traversal, no separators). Callers that build host paths
// from a caller-supplied name must check this first.
func ValidName(name string) bool { return validName.MatchString(name) }

// validDisplayName is what a human may call a stack: letters, digits, spaces
// and . _ - (no control chars, no slashes), up to 64 runes. Paths never use
// it directly -- they use the storage slug or the numeric id.
var validDisplayName = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._-]{0,63}$`)

// ValidDisplayName reports whether name is an acceptable display name.
func ValidDisplayName(name string) bool { return validDisplayName.MatchString(name) }

// OriginInfo tracks where the stack came from.
type OriginInfo struct {
	Type           string `json:"type"` // "catalog" or "byo"
	AppID          string `json:"app_id,omitempty"`
	Variant        string `json:"variant,omitempty"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
	CatalogURL     string `json:"catalog_url,omitempty"`
}

// State represents state.json (schema_version 1) inside a stack directory.
type State struct {
	SchemaVersion int               `json:"schema_version"`
	Origin        OriginInfo        `json:"origin"`
	Variables     map[string]string `json:"variables,omitempty"`
	ComposeSHA256 string            `json:"compose_sha256"`
	Modified      bool              `json:"modified"`
	DesiredState  string            `json:"desired_state"`          // "running" | "stopped"
	Engine        string            `json:"engine,omitempty"`       // runtime this stack runs on; "" = podman
	DisplayName   string            `json:"display_name,omitempty"` // friendly label; identity stays the dir id
	Group         string            `json:"group,omitempty"`        // optional sidebar grouping label
	Order         int               `json:"order,omitempty"`        // manual sidebar sort position (1-based; 0 = unset, sorts last)
	AppVersion    string            `json:"app_version,omitempty"`
	InstalledAt   string            `json:"installed_at"`
	UpdatedAt     string            `json:"updated_at"`
	// Rollback is, per service, the image the last update replaced -- what
	// "Roll back" returns to, and what "Unpin" restores the compose from.
	Rollback map[string]RollbackImage `json:"rollback,omitempty"`
	// UpdatePolicy is how far updates apply themselves ("" = off; see
	// updates.Policy), and ServicePolicy overrides it per service -- a
	// database can be stricter than the app in front of it.
	UpdatePolicy  string            `json:"update_policy,omitempty"`
	ServicePolicy map[string]string `json:"service_policy,omitempty"`
}

// RollbackImage is one service's image before an update.
type RollbackImage struct {
	// Compose is the service's image: value as the compose had it, ${VAR}s
	// and all, so an unpin puts back what the operator wrote.
	Compose string `json:"compose"`
	// Ref and Digest are what the container actually ran: the resolved
	// "repo:tag" and the registry digest it was pulled as. Rollback pins to
	// Ref@Digest, fetched from the registry -- the old image itself does not
	// survive `podman image prune -af`, which maintenance runs.
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
	At     string `json:"at"`
}

// Stack represents a parsed directory containing a compose file.
type Stack struct {
	Name        string `json:"name"`        // stable identity: the directory / compose project id
	DisplayName string `json:"displayName"` // friendly label shown in the UI (falls back to Name)
	Dir         string `json:"dir"`
	Compose     string `json:"compose"`
	Env         string `json:"env"`
	// Director is the appjail-director.yml content when this stack runs on the
	// appjail engine via appjail-director (empty otherwise). Its presence tells
	// the UI to show the appjail-native spec instead of the compose.yaml, which
	// on a director stack is kept only for status/log service discovery.
	Director string `json:"director,omitempty"`
	// Makejail is the build recipe director uses per service (image source,
	// jail options); present alongside Director on a director stack.
	Makejail string `json:"makejail,omitempty"`
	// Icon is the file name of the app icon kept in the stack dir ("icon.svg"),
	// copied from the catalog at install so it survives the app leaving the
	// catalog or the catalog being removed. Empty when the stack has none.
	Icon  string `json:"icon,omitempty"`
	State *State `json:"state,omitempty"`
}

// IconFile returns the stack dir's icon file name ("icon.svg"), or "".
func IconFile(dir string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, "icon.*"))
	if len(matches) == 0 {
		return ""
	}
	return filepath.Base(matches[0])
}

// EngineName is the runtime this stack runs on, as recorded in state.json.
// Empty only for a stack with no state at all (see MigrateEngine); the
// daemon treats that as an unavailable engine rather than guessing.
func (s *Stack) EngineName() string {
	if s.State != nil {
		return s.State.Engine
	}
	return ""
}

// EnvMap parses the stack's .env content into a map: KEY=VALUE lines, blanks
// and #comments skipped, whitespace trimmed. The single parser for everything
// that resolves ${VAR}s against a stack (preflight, update checks).
func (s *Stack) EnvMap() map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(s.Env, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if eq := strings.IndexByte(line, '='); eq > 0 {
			env[strings.TrimSpace(line[:eq])] = strings.TrimSpace(line[eq+1:])
		}
	}
	return env
}

// Manager handles CRUD operations for stacks on the filesystem.
type Manager struct {
	StacksDir string
}

// NewManager initializes a new stack manager.
func NewManager(stacksDir string) *Manager {
	return &Manager{StacksDir: stacksDir}
}

// List scans the StacksDir and returns basic info for all valid stacks.
// A valid stack is a directory that contains a compose.yaml file.
func (m *Manager) List() ([]*Stack, error) {
	entries, err := os.ReadDir(m.StacksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Stack{}, nil
		}
		return nil, err
	}

	var stacks []*Stack
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		composePath := filepath.Join(m.StacksDir, name, "compose.yaml")
		directorPath := filepath.Join(m.StacksDir, name, "appjail-director.yml")

		// A stack is a dir with a compose.yaml, or a native appjail stack with
		// just an appjail-director.yml.
		_, composeErr := os.Stat(composePath)
		_, directorErr := os.Stat(directorPath)
		if composeErr == nil || directorErr == nil {
			st, _ := m.LoadState(name) // absent/unreadable state.json -> nil
			dir := filepath.Join(m.StacksDir, name)
			stacks = append(stacks, &Stack{
				Name:        name,
				DisplayName: displayName(name, st),
				Dir:         dir,
				Icon:        IconFile(dir),
				State:       st,
			})
		}
	}
	return stacks, nil
}

// Get reads the full compose.yaml and .env content for a specific stack.
func (m *Manager) Get(name string) (*Stack, error) {
	if !ValidName(name) {
		return nil, ErrInvalidName
	}
	dir := filepath.Join(m.StacksDir, name)
	composePath := filepath.Join(dir, "compose.yaml")
	envPath := filepath.Join(dir, ".env")

	// appjail-director.yml is present only on a director-backed appjail stack;
	// absent for compose/oci-run stacks. A native appjail stack (hand-written
	// director + Makejail) has no compose.yaml at all.
	directorBytes, _ := os.ReadFile(filepath.Join(dir, "appjail-director.yml"))
	makejailBytes, _ := os.ReadFile(filepath.Join(dir, "Makejail"))

	composeBytes, err := os.ReadFile(composePath)
	if err != nil && len(directorBytes) == 0 {
		return nil, fmt.Errorf("failed to read compose.yaml for stack %s: %w", name, err)
	}

	// .env is optional, so we ignore NotExist errors
	envBytes, _ := os.ReadFile(envPath)

	st, _ := m.LoadState(name) // absent/unreadable state.json -> nil

	return &Stack{
		Name:        name,
		DisplayName: displayName(name, st),
		Dir:         dir,
		Compose:     string(composeBytes),
		Env:         string(envBytes),
		Director:    string(directorBytes),
		Makejail:    string(makejailBytes),
		Icon:        IconFile(dir),
		State:       st,
	}, nil
}

// Save creates or updates a stack directory, writing compose.yaml and .env.
func (m *Manager) Save(stack *Stack) error {
	if !ValidName(stack.Name) {
		return ErrInvalidName
	}
	dir := filepath.Join(m.StacksDir, stack.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create stack directory: %w", err)
	}

	// A native appjail stack has no compose at all; don't leave an empty one
	// behind that the compose path would then try to run.
	if stack.Compose != "" || stack.Director == "" {
		composePath := filepath.Join(dir, "compose.yaml")
		if err := writeFileAtomic(composePath, []byte(stack.Compose), 0644); err != nil {
			return fmt.Errorf("failed to write compose.yaml: %w", err)
		}
	}

	envPath := filepath.Join(dir, ".env")
	if stack.Env != "" {
		// Write .env tightly locked down (0600) to protect secrets
		if err := writeFileAtomic(envPath, []byte(stack.Env), 0600); err != nil {
			return fmt.Errorf("failed to write .env: %w", err)
		}
	} else {
		// Remove .env if it was cleared out
		os.Remove(envPath)
	}

	// Director-backed stacks: the edited appjail-director.yml is the runtime
	// spec, written verbatim (director reconciles changes on the next up).
	// Empty means "not a director stack"; the file is never removed here.
	if stack.Director != "" {
		body := strings.TrimRight(stack.Director, "\n") + "\n"
		if err := writeFileAtomic(filepath.Join(dir, "appjail-director.yml"), []byte(body), 0644); err != nil {
			return fmt.Errorf("failed to write appjail-director.yml: %w", err)
		}
	}
	if stack.Makejail != "" {
		// Trailing newline is load-bearing: appjail's Makejail parser drops an
		// unterminated final line (an `OPTION from=` lost that way builds an
		// empty jail instead of the OCI container).
		body := strings.TrimRight(stack.Makejail, "\n") + "\n"
		if err := writeFileAtomic(filepath.Join(dir, "Makejail"), []byte(body), 0644); err != nil {
			return fmt.Errorf("failed to write Makejail: %w", err)
		}
	}

	stack.Dir = dir
	return nil
}

// Delete removes a stack's directory and everything in it (compose.yaml,
// .env, state.json). It only ever touches fjord's own stack dir -- container
// data on bind-mounted host paths is never removed. The name is re-validated
// here because this calls os.RemoveAll: a traversal name must not escape
// StacksDir.
func (m *Manager) Delete(name string) error {
	if !validName.MatchString(name) {
		return ErrInvalidName
	}
	dir := filepath.Join(m.StacksDir, name)
	if _, err := os.Stat(dir); err != nil {
		return err // NotExist bubbles up so the handler can 404
	}
	return os.RemoveAll(dir)
}

// Slug turns a display name into a stack id: lowercase, runs of anything but
// [a-z0-9] collapsed to "-", trimmed ("Tautulli 4K" -> "tautulli-4k").
// fallback (the catalog app id) is used when nothing usable is left.
func Slug(name, fallback string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return fallback
	}
	return slug
}

// AllocateName picks the id for a new stack from its name, the way compose
// and Portainer do: the slug itself when free, else "<slug>-2", "-3", ...
// The id is the stack's dir, its compose project, and so the prefix of its
// containers, network and named volumes ("radarr_radarr_1", "radarr_default")
// -- readable where they show up (podman ps, jls). Uniqueness is among
// stacks that exist now; a name whose stack was deleted is free again and,
// like `compose down` then `up`, picks up any named volumes it left (Delete
// keeps data on purpose). Ghost container records can't collide: Down and Up
// sweep the stack's namespace.
func (m *Manager) AllocateName(name, fallback string) string {
	base := Slug(name, fallback)
	if !ValidName(base) || base == "" {
		base = "stack"
	}
	id := base
	for n := 2; m.exists(id); n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

func (m *Manager) exists(id string) bool {
	_, err := os.Stat(filepath.Join(m.StacksDir, id))
	return err == nil
}

// MigrateEngine records engine on every stack that has none. Stacks written
// before multi-engine (fjord <= 0.1.1) never recorded one; the daemon passes
// the engine those releases used so the fact lives in each stack's own
// state.json from then on, not in code. Returns the stacks it touched.
func (m *Manager) MigrateEngine(engine string) ([]string, error) {
	stacks, err := m.List()
	if err != nil {
		return nil, err
	}
	var migrated []string
	for _, s := range stacks {
		if s.State != nil && s.State.Engine != "" {
			continue
		}
		st := s.State
		if st == nil {
			st = &State{SchemaVersion: stateSchemaVersion}
		}
		st.Engine = engine
		if err := m.SaveState(s.Name, st); err != nil {
			return migrated, fmt.Errorf("%s: %w", s.Name, err)
		}
		migrated = append(migrated, s.Name)
	}
	return migrated, nil
}

// SetDisplayName updates a stack's friendly label -- an instant metadata edit
// (state.json only). Identity (dir, compose project, containers, volumes) is
// unchanged, so a rename never stops or recreates anything.
func (m *Manager) SetDisplayName(id, name string) error {
	st, err := m.LoadState(id)
	if err != nil {
		return err
	}
	if st == nil {
		st = &State{SchemaVersion: stateSchemaVersion}
	}
	st.DisplayName = strings.TrimSpace(name)
	return m.SaveState(id, st)
}

// SetUpdatePolicy records a stack's update policy and per-service overrides.
// Validation is the caller's: this package does not know the policy names.
func (m *Manager) SetUpdatePolicy(id, policy string, services map[string]string) error {
	st, err := m.LoadState(id)
	if err != nil {
		return err
	}
	if st == nil {
		st = &State{SchemaVersion: stateSchemaVersion}
	}
	st.UpdatePolicy = policy
	st.ServicePolicy = services
	if len(services) == 0 {
		st.ServicePolicy = nil
	}
	return m.SaveState(id, st)
}

// SetEngine records which runtime a stack runs on (set once at install).
func (m *Manager) SetEngine(id, engine string) error {
	st, err := m.LoadState(id)
	if err != nil {
		return err
	}
	if st == nil {
		st = &State{SchemaVersion: stateSchemaVersion}
	}
	st.Engine = engine
	return m.SaveState(id, st)
}

// SetAppID records the catalog app this stack was installed from, so the UI can
// resolve its icon/links by a stable id (the stack's own name is a numeric id
// and its display name is user-editable, so neither is a reliable icon key).
func (m *Manager) SetAppID(id, appID string) error {
	if appID == "" {
		return nil
	}
	st, err := m.LoadState(id)
	if err != nil {
		return err
	}
	if st == nil {
		st = &State{SchemaVersion: stateSchemaVersion}
	}
	st.Origin.AppID = appID
	return m.SaveState(id, st)
}

// displayName resolves what the UI shows: the stored label, else the id.
func displayName(id string, st *State) string {
	if st != nil && st.DisplayName != "" {
		return st.DisplayName
	}
	return id
}

// statePath returns the state.json path for a stack.
func (m *Manager) statePath(name string) string {
	return filepath.Join(m.StacksDir, name, "state.json")
}

// LoadState reads a stack's state.json. Returns (nil, nil) when the stack has
// no state file yet -- e.g. a BYO stack dropped into StacksDir over ssh.
func (m *Manager) LoadState(name string) (*State, error) {
	data, err := os.ReadFile(m.statePath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse state.json for %s: %w", name, err)
	}
	return &st, nil
}

// SaveState atomically writes a stack's state.json via a temp file in the same
// directory + rename, so a crash mid-write can't leave a truncated file.
func (m *Manager) SaveState(name string, st *State) error {
	if !validName.MatchString(name) {
		return ErrInvalidName
	}
	st.SchemaVersion = stateSchemaVersion

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	return writeFileAtomic(m.statePath(name), data, 0600)
}

// writeData is the write writeFileAtomic makes; tests swap it to fail.
var writeData = func(f *os.File, b []byte) (int, error) { return f.Write(b) }

// writeFileAtomic replaces path with data, or leaves it exactly as it was.
//
// os.WriteFile truncates first and writes second, so a write that fails in
// between leaves an EMPTY file. On netlab a full disk did exactly that to a
// stack's compose.yaml during a version change: the container kept running,
// and fjord lost every image, port and volume it knew for the stack. Written
// to a temp file in the same directory and renamed over the old one, a failed
// save is an error and nothing else.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := writeData(tmp, data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// EnsureState creates state.json (desired_state "stopped") the first time a
// stack's compose is saved, otherwise just bumps UpdatedAt. It never changes
// a desired_state already set by up/down.
func (m *Manager) EnsureState(name string) error {
	st, err := m.LoadState(name)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if st == nil {
		st = &State{DesiredState: "stopped", InstalledAt: now}
	}
	if st.InstalledAt == "" {
		st.InstalledAt = now
	}
	st.UpdatedAt = now
	return m.SaveState(name, st)
}

// SetDesiredState records the operator's intent ("running" after up, "stopped"
// after down) so start-on-boot can restore it. Creates state.json if absent.
func (m *Manager) SetDesiredState(name, desired string) error {
	st, err := m.LoadState(name)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if st == nil {
		st = &State{InstalledAt: now}
	}
	if st.InstalledAt == "" {
		st.InstalledAt = now
	}
	st.DesiredState = desired
	st.UpdatedAt = now
	return m.SaveState(name, st)
}

// SetGroup sets a stack's optional sidebar grouping label (empty = ungrouped).
func (m *Manager) SetGroup(name, group string) error {
	st, err := m.LoadState(name)
	if err != nil {
		return err
	}
	if st == nil {
		st = &State{InstalledAt: time.Now().UTC().Format(time.RFC3339)}
	}
	st.Group = group
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return m.SaveState(name, st)
}

// OrderItem is one stack's desired sidebar placement: its group and its
// top-to-bottom rank (the caller supplies items already in display order).
type OrderItem struct {
	Name  string `json:"name"`
	Group string `json:"group"`
}

// Reorder persists sidebar placement for a set of stacks in one pass: each
// item's Order becomes its 1-based position in the list, and its Group is set
// (drag-and-drop moves position and group together). Unknown/missing stacks are
// skipped so a stale client list can't error the whole batch.
func (m *Manager) Reorder(items []OrderItem) error {
	for i, it := range items {
		st, err := m.LoadState(it.Name)
		if err != nil || st == nil {
			continue // stack vanished between the client's read and this write
		}
		st.Order = i + 1
		st.Group = strings.TrimSpace(it.Group)
		st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := m.SaveState(it.Name, st); err != nil {
			return err
		}
	}
	return nil
}
