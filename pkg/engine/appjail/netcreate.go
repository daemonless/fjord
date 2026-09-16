package appjail

import (
	"context"
	"fmt"

	"github.com/daemonless/fjord/pkg/engine"
)

// networkKinds is appjail's contribution to Capabilities: none.
//
// fjord does not create appjail networks. `appjail network add` puts the
// gateway address on the bridge it creates, which for a LAN segment means
// claiming the router's address. Jails are attached to a bridge that already
// exists instead, and that bridge is described by a conflist the podman engine
// writes -- so a network is defined once and both engines attach to it.
//
// Declaring no kinds is what stops the UI offering a create form whose submit
// can only fail.
func networkKinds() []engine.NetworkKind { return nil }

// CreateNetwork is unsupported: a LAN network is defined by its conflist,
// which the podman backend writes, and both engines then attach to the same
// bridge. See networkKinds for why fjord never runs `appjail network add`.
func (b *Backend) CreateNetwork(ctx context.Context, spec engine.NetworkSpec) (engine.Network, error) {
	return engine.Network{}, fmt.Errorf("the appjail engine does not create networks; define one on the podman engine and both can attach to it")
}

// RemoveNetwork is unsupported for the same reason as CreateNetwork.
func (b *Backend) RemoveNetwork(ctx context.Context, name string, force bool) error {
	return fmt.Errorf("networks are managed on the podman engine; remove %q there", name)
}

// NetworkParents is empty: appjail does not create networks here, so there is
// no parent to pick.
func (b *Backend) NetworkParents(ctx context.Context) ([]engine.NetworkParent, error) {
	return nil, nil
}
