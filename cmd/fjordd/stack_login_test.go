package main

import (
	"testing"

	"github.com/daemonless/fjord/pkg/manifest"
)

func TestResolveLogin(t *testing.T) {
	env := map[string]string{"CUSTOM_USER": "admin", "PASSWORD": "s3cret"}
	v, ok := resolveLogin(manifest.Login{User: "${CUSTOM_USER}", Password: "${PASSWORD}"}, env)
	if !ok || v.User != "admin" || v.Password != "s3cret" || v.Default {
		t.Errorf("made-up login: %+v %v", v, ok)
	}
	// The app's own default: shown, marked so the page says to change it.
	v, ok = resolveLogin(manifest.Login{User: "admin", Password: "admin123"}, env)
	if !ok || !v.Default || v.Password != "admin123" {
		t.Errorf("default login: %+v %v", v, ok)
	}
	// Installed before the app had a login: no password, so nothing to show.
	if v, ok := resolveLogin(manifest.Login{Password: "${PASSWORD}"}, map[string]string{}); ok {
		t.Errorf("no password set: %+v", v)
	}
	if v, ok := resolveLogin(manifest.Login{Note: "see Logs"}, env); !ok || v.Note != "see Logs" || v.Default {
		t.Errorf("note only: %+v %v", v, ok)
	}
}
