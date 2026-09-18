package registry

import (
	"context"
	"sync"
	"time"
)

// TrainsTTL bounds how stale a cached tag list may be. Tags only change when
// CI publishes, so minutes of staleness is invisible in the install wizard.
const TrainsTTL = 10 * time.Minute

var (
	trainsFn  = Trains   // swapped in tests
	nowFn     = time.Now // swapped in tests
	trainsMu  sync.Mutex
	trainsMap = map[string]*trainsEntry{}
)

type trainsEntry struct {
	done   chan struct{} // closed when the fetch finished
	at     time.Time
	trains map[string][]Version
	err    error
}

// CachedTrains is Trains memoized per image for TrainsTTL, single-flight:
// concurrent callers for the same image share one registry round trip.
// Errors are not cached, so a registry hiccup is retried on the next call.
// The declared scheme is part of the key: a catalog refresh that changes it
// must not be masked by an entry grouped under the old one.
func CachedTrains(ctx context.Context, image string, sch *Scheme) (map[string][]Version, error) {
	key := image + "\x00" + sch.Key()
	trainsMu.Lock()
	e, ok := trainsMap[key]
	if ok {
		select {
		case <-e.done: // finished: fresh hit, or stale/failed and needs a refetch
			if e.err == nil && nowFn().Sub(e.at) < TrainsTTL {
				trainsMu.Unlock()
				return e.trains, nil
			}
			ok = false
		default: // in flight: join it
		}
	}
	if !ok {
		e = &trainsEntry{done: make(chan struct{})}
		trainsMap[key] = e
		trainsMu.Unlock()
		e.trains, e.err = trainsFn(ctx, image, sch)
		e.at = nowFn()
		close(e.done)
		return e.trains, e.err
	}
	trainsMu.Unlock()
	select {
	case <-e.done:
		return e.trains, e.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ResetTrainsCache drops every cached entry (tests, or a forced refresh).
func ResetTrainsCache() {
	trainsMu.Lock()
	trainsMap = map[string]*trainsEntry{}
	trainsMu.Unlock()
}
