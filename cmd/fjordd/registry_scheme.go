package main

import (
	"github.com/daemonless/fjord/pkg/registry"
)

// schemeFor resolves an image repo to its declared tag scheme, for the version
// pickers and the update check. Nil when the catalog does not describe the
// repo, which leaves the scheme to be inferred from the tags.
func (s *server) schemeFor(repo string) *registry.Scheme {
	channels, aliases := s.cat.TagScheme(repo)
	if len(channels) == 0 {
		return nil
	}
	return &registry.Scheme{Channels: channels, Aliases: aliases}
}
