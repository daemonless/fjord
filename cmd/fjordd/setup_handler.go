package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/daemonless/fjord/pkg/doctor"
)

// handleSetup reports host readiness: platform, deployment mode, and every
// doctor check with its remediation. Read-only -- the UI renders fixes as
// commands for the operator (an install endpoint may come later).
func (s *server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	report := doctor.Run(ctx, doctor.Config{Engines: s.engineNames(), FjordRoot: s.fjordRoot})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// handleSetupInstall fixes one doctor check itself: POST {"id": "epair"}
// runs that check's installer (a pinned, checksummed download, or "pkg
// install" of its package). The page runs the checks again afterwards.
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
	if err := doctor.Install(ctx, doctor.Config{Engines: s.engineNames(), FjordRoot: s.fjordRoot}, req.ID); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	log.Printf("setup: installed what the %q check needs", req.ID)
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
