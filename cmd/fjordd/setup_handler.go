package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/doctor"
)

// handleSetup reports host readiness: platform, deployment mode, and every
// doctor check with its remediation. Read-only -- the UI renders fixes as
// commands for the operator; /api/setup/install fixes the installable ones.
func (s *server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.registerNewEngines()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	engines := s.doctorEngines()
	// ?engine=podman,appjail asks about those engines, installed or not: the
	// setup page shows what the engines you pick would need before they are
	// on the host.
	if q := r.URL.Query().Get("engine"); q != "" {
		engines = nil
		for _, name := range strings.Split(q, ",") {
			if _, ok := descriptor(name); !ok {
				http.Error(w, "unknown engine: "+name, 400)
				return
			}
			engines = append(engines, name)
		}
	}
	report := doctor.Run(ctx, doctor.Config{Engines: engines, FjordRoot: s.fjordRoot})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// handleSetupInstall fixes one doctor check itself: POST {"id": "epair"}
// runs that check's installer (a pinned, checksummed download, or "pkg
// install" of its package). The page runs the checks again afterwards.
//
// With ?stream=1 the answer is the installer's terminal session as it
// happens, ending in a line "[done]" or "[error] <why>": the status is sent
// before the outcome is known.
func (s *server) handleSetupInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, "id required", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	cfg := doctor.Config{Engines: s.doctorEngines(), FjordRoot: s.fjordRoot}
	if r.URL.Query().Get("stream") == "1" {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		out := flushWriter{w}
		if err := doctor.Install(ctx, cfg, req.ID, out); err != nil {
			fmt.Fprintf(out, "[error] %v\n", err)
			return
		}
		log.Printf("setup: installed what the %q check needs", req.ID)
		s.registerNewEngines()
		fmt.Fprintln(out, "[done]")
		return
	}
	var out bytes.Buffer
	if err := doctor.Install(ctx, cfg, req.ID, &out); err != nil {
		http.Error(w, err.Error()+"\n"+out.String(), 500)
		return
	}
	log.Printf("setup: installed what the %q check needs", req.ID)
	s.registerNewEngines()
	w.WriteHeader(http.StatusNoContent)
}

// logDoctor runs the doctor once at startup and logs every non-ok check, so a
// mis-provisioned host is named in the daemon log before the first stack-up
// fails cryptically.
//
// EVERY registered engine, not just the default one. A host whose default is
// appjail still runs podman stacks, and checking only the default hid every
// podman problem on it -- including a missing dnsname plugin, which makes a
// multi-service stack fail in a way that looks like the app's fault.
func logDoctor(fjordRoot string, engineNames []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	report := doctor.Run(ctx, doctor.Config{Engines: engineNames, FjordRoot: fjordRoot})
	log.Printf("doctor: %s, %s mode, engines %s", report.OS, report.Mode, strings.Join(engineNames, ", "))
	for _, c := range report.Checks {
		if c.Status == doctor.OK {
			continue
		}
		msg := c.Detail
		if c.Fix != "" {
			msg += " | fix: " + c.Fix
		}
		log.Printf("doctor: [%s] %s: %s", c.Status, c.Name, msg)
	}
}

// flushWriter sends every write to the client at once, so a long pkg install
// shows up line by line instead of all at the end.
type flushWriter struct{ w http.ResponseWriter }

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}
