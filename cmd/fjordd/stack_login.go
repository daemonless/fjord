package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/manifest"
	"github.com/daemonless/fjord/pkg/stack"
)

// stackLoginView is how to sign in to a stack's app, for its page.
type stackLoginView struct {
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	// Default: the password is the app's own, the same in every install;
	// the page says to change it.
	Default bool   `json:"default,omitempty"`
	Note    string `json:"note,omitempty"`
}

// stackLogin answers GET /api/stacks/<name>/login: the login the stack's
// catalog app declares, with its ${VAR}s from the stack's .env. 204 when
// there is none to show: not a catalog install, an app that declares no
// login, or a stack installed before its app had one (its .env holds no
// password, so the app asks for none).
func (s *server) stackLogin(w http.ResponseWriter, r *http.Request, name string) {
	st, err := s.manager.Get(name)
	if err != nil {
		http.Error(w, "Stack not found", 404)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	v, ok := s.loginOf(ctx, st)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *server) loginOf(ctx context.Context, st *stack.Stack) (stackLoginView, bool) {
	if st.State == nil || st.State.Origin.Type != "catalog" || st.State.Origin.AppID == "" || s.cat == nil {
		return stackLoginView{}, false
	}
	raw, err := s.cat.ManifestAny(ctx, st.State.Origin.AppID)
	if err != nil {
		return stackLoginView{}, false
	}
	m, err := manifest.Parse(string(raw))
	if err != nil || m.Login == nil {
		return stackLoginView{}, false
	}
	return resolveLogin(*m.Login, st.EnvMap())
}

// resolveLogin fills a login's ${VAR}s from env. false when a password it
// names is not set there.
func resolveLogin(l manifest.Login, env map[string]string) (stackLoginView, bool) {
	fill := func(v string) (string, bool) {
		if name, ok := strings.CutPrefix(v, "${"); ok && strings.HasSuffix(name, "}") {
			return env[strings.TrimSuffix(name, "}")], true
		}
		return v, false
	}
	user, _ := fill(l.User)
	pass, fromEnv := fill(l.Password)
	if fromEnv && pass == "" {
		return stackLoginView{}, false
	}
	return stackLoginView{User: user, Password: pass, Default: pass != "" && !fromEnv, Note: l.Note}, true
}
