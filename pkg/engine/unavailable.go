package engine

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/daemonless/fjord/pkg/stack"
)

// ErrUnavailable is returned by every method of an Unavailable backend. Callers
// can errors.Is() it to distinguish "no engine" from a runtime failure.
var ErrUnavailable = errors.New("engine unavailable")

// Unavailable returns a Backend standing in for an engine that isn't
// registered: name is the engine a stack asked for, or "" when no engine at
// all is available. Every operation fails with ErrUnavailable instead of the
// caller dereferencing a nil interface, so the daemon (and its System page,
// which shows how to fix the host) stays up when podman is missing at boot.
func Unavailable(name string) Backend {
	return unavailable{name: name}
}

type unavailable struct{ name string }

func (u unavailable) err() error {
	if u.name == "" {
		return fmt.Errorf("%w: no container engine is available on this host", ErrUnavailable)
	}
	return fmt.Errorf("%w: engine %q is not available on this host", ErrUnavailable, u.name)
}

func (u unavailable) Up(context.Context, *stack.Stack) (io.ReadCloser, error)   { return nil, u.err() }
func (u unavailable) Down(context.Context, *stack.Stack) (io.ReadCloser, error) { return nil, u.err() }
func (u unavailable) Update(context.Context, *stack.Stack) (io.ReadCloser, error) {
	return nil, u.err()
}
func (u unavailable) Restart(context.Context, *stack.Stack) (io.ReadCloser, error) {
	return nil, u.err()
}
func (u unavailable) Logs(context.Context, *stack.Stack, int, bool, []string) (io.ReadCloser, error) {
	return nil, u.err()
}
func (u unavailable) Exec(context.Context, ExecOptions) (ExecSession, error) { return nil, u.err() }
func (u unavailable) Status(context.Context, *stack.Stack) (StackStatus, error) {
	return StackStatus{}, u.err()
}
func (u unavailable) Networks(context.Context) ([]Network, error) { return nil, u.err() }
func (u unavailable) Volumes(context.Context) ([]Volume, error)   { return nil, u.err() }
func (u unavailable) CreateNetwork(context.Context, NetworkSpec) (Network, error) {
	return Network{}, u.err()
}
func (u unavailable) RemoveNetwork(context.Context, string, bool) error { return u.err() }
func (u unavailable) NetworkParents(context.Context) ([]NetworkParent, error) {
	return nil, u.err()
}
func (u unavailable) CreateVolume(context.Context, VolumeSpec) (Volume, error) {
	return Volume{}, u.err()
}
func (u unavailable) RemoveVolume(context.Context, string, bool) error     { return u.err() }
func (u unavailable) UsedPorts(context.Context) (map[string]string, error) { return nil, u.err() }
func (u unavailable) DiskUsage(context.Context) ([]DiskRow, error)         { return nil, u.err() }
func (u unavailable) PruneCapabilities() PruneCapabilities                 { return PruneCapabilities{} }
func (u unavailable) Prune(context.Context, PruneOptions) (PruneReport, error) {
	return PruneReport{}, u.err()
}
func (u unavailable) ImageRepoDigests(context.Context, string) ([]string, error) { return nil, u.err() }
func (u unavailable) RunningImages(context.Context, *stack.Stack) ([]RunningImage, error) {
	return nil, u.err()
}
func (u unavailable) Capabilities() Capabilities                                  { return Capabilities{} }
func (u unavailable) StoreSMBCredentials(server, username, password string) error { return u.err() }
