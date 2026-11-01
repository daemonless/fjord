//go:build !freebsd

package doctor

import (
	"os"
	"runtime"
)

// hostMode is "container" when a container's markers are present, else "host".
func hostMode() string {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return "container"
		}
	}
	return "host"
}

// platform on platforms without a dedicated leaf yet. Mode is "host" unless
// the usual container markers are present -- host mode is what unlocks the
// directory browser and folder creation, which a native Linux host should
// have. canInstall stays false (no package-manager map yet), so nothing
// tries `pkg` here. A real platform_linux.go (apt/dnf/apk discovery, Pkg map)
// would replace this stub via its build tag.
func platform(cfg Config) platformInfo {
	return platformInfo{
		os:         runtime.GOOS,
		mode:       hostMode(),
		canInstall: false,
		checks: append([]Check{
			{
				ID: "podman", Name: "podman CLI", Engine: "podman",
				Probe: binProbe("podman"),
				Why:   "Runs the containers. Every stack on the podman engine is created, started and inspected through it.",
				Fix:   "# with your distribution's package manager, e.g.\nsudo apt install -y podman\n# or: sudo dnf install -y podman",
			},
			{
				ID: "socket", Name: "podman API socket", Engine: "podman",
				Probe: socketProbe,
				Why:   "fjord talks to podman over its API socket for status, logs and shells. Without it the podman engine is blind, even with podman installed.",
				Fix:   "sudo systemctl enable --now podman.socket",
			},
			{
				ID: "compose", Name: "podman-compose", Engine: "podman",
				Probe: binProbe("podman-compose"),
				Why:   "Turns a stack's compose.yaml into containers. fjord hands it the file on every up, down and update.",
				Fix:   "# with your distribution's package manager, e.g.\nsudo apt install -y podman-compose\n# or: sudo dnf install -y podman-compose",
			},
			{
				ID: "conmon", Name: "conmon", Engine: "podman", HostOnly: true,
				Probe: binProbe("conmon"),
				Why:   "The per-container monitor podman starts everything through. It holds a container's I/O and exit status while podman itself isn't running.",
				Fix:   "# with your distribution's package manager, e.g.\nsudo apt install -y conmon\n# or: sudo dnf install -y conmon",
			},
			rootCheck(cfg.FjordRoot),
		}, osChecks(cfg)...),
	}
}
