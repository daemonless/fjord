package main

import (
	"context"
	"errors"

	"github.com/daemonless/fjord/pkg/stack"
)

// originManifest is the catalog manifest a stack was installed from: from
// the catalog it came from when that is known, so an app id two catalogs
// share reads the right one; by id across every catalog for a stack
// installed before the catalog was recorded, or when that catalog is gone.
func (s *server) originManifest(ctx context.Context, st *stack.Stack) ([]byte, error) {
	if st == nil || st.State == nil || st.State.Origin.AppID == "" || s.cat == nil {
		return nil, errors.New("not installed from a catalog")
	}
	o := st.State.Origin
	if o.Catalog != "" {
		if b, err := s.cat.Manifest(ctx, o.Catalog, o.AppID); err == nil {
			return b, nil
		}
	}
	return s.cat.ManifestAny(ctx, o.AppID)
}
