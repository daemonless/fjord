package appjail

import (
	"context"
	"fmt"

	"github.com/daemonless/fjord/pkg/engine"
)

// networkKinds is appjail's contribution to Capabilities.
//
// fjord does not create appjail networks: `appjail network add` puts the
// gateway address on the bridge it creates, which for a LAN segment means
// claiming the router's address. A jail is instead attached to a bridge that
// already exists, exactly as a podman container is -- so the only kind here is
// the same "lan" kind podman offers, and a network means the same thing to
// both engines.
func networkKinds() []engine.NetworkKind {
	return []engine.NetworkKind{{
		ID:            "lan",
		Label:         "Own IP on a bridge",
		Help:          "Jails get their own address on the segment the bridge is on, so they can bind :80/:443 without colliding with the host.",
		ParentLabel:   "Bridge",
		NeedsGateway:  true,
		SupportsMTU:   true,
		SupportsRange: true,
	}}
}

// CreateNetwork is unsupported: a LAN network is defined by its conflist,
// which the podman backend writes, and both engines then attach to the same
// bridge. See networkKinds for why fjord never runs `appjail network add`.
func (b *Backend) CreateNetwork(ctx context.Context, spec engine.NetworkSpec) (engine.Network, error) {
	return engine.Network{}, fmt.Errorf("the appjail engine does not create networks; define one on the podman engine and both can attach to it")
}

// RemoveNetwork is unsupported for the same reason as CreateNetwork.
func (b *Backend) RemoveNetwork(ctx context.Context, name string, force bool) error {
	return fmt.Errorf("the appjail engine does not remove networks; remove it on the podman engine")
}

// NetworkParents is empty: appjail does not create networks here, so there is
// no parent to pick.
func (b *Backend) NetworkParents(ctx context.Context) ([]engine.NetworkParent, error) {
	return nil, nil
}
