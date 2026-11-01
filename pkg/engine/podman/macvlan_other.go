//go:build !linux

package podman

import (
	"context"
	"errors"

	"github.com/daemonless/fjord/pkg/engine"
)

func lanKind() (engine.NetworkKind, bool) { return engine.NetworkKind{}, false }

func (b *Backend) createMacvlan(context.Context, engine.NetworkSpec) (engine.Network, error) {
	return engine.Network{}, errors.New("macvlan networks are Linux only")
}

func linuxParents() []engine.NetworkParent { return nil }
