package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
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

// logDoctor runs the doctor once at startup and logs every non-ok check, so a
// mis-provisioned host is named in the daemon log before the first stack-up
// fails cryptically.
func logDoctor(fjordRoot, engineName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	report := doctor.Run(ctx, doctor.Config{Engines: []string{engineName}, FjordRoot: fjordRoot})
	log.Printf("doctor: %s, %s mode, engine %s", report.OS, report.Mode, engineName)
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
