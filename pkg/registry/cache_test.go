package registry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func stubTrains(t *testing.T, calls *int32, delay time.Duration, err error) {
	t.Helper()
	ResetTrainsCache()
	orig, origNow := trainsFn, nowFn
	trainsFn = func(ctx context.Context, image string, sch *Scheme) (map[string][]Version, error) {
		atomic.AddInt32(calls, 1)
		time.Sleep(delay)
		if err != nil {
			return nil, err
		}
		return map[string][]Version{"latest": {{Version: "1.0", Tag: "1.0"}}}, nil
	}
	t.Cleanup(func() { trainsFn, nowFn = orig, origNow; ResetTrainsCache() })
}

func TestCachedTrainsHitsWithinTTL(t *testing.T) {
	var calls int32
	stubTrains(t, &calls, 0, nil)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := CachedTrains(ctx, "ghcr.io/x/app", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := CachedTrains(ctx, "ghcr.io/x/other", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 fetches (one per image), got %d", calls)
	}
}

func TestCachedTrainsRefetchesAfterTTL(t *testing.T) {
	var calls int32
	stubTrains(t, &calls, 0, nil)
	ctx := context.Background()
	CachedTrains(ctx, "img", nil)
	base := time.Now()
	nowFn = func() time.Time { return base.Add(TrainsTTL + time.Second) }
	CachedTrains(ctx, "img", nil)
	if calls != 2 {
		t.Fatalf("expected refetch after TTL, got %d fetches", calls)
	}
}

func TestCachedTrainsSingleFlight(t *testing.T) {
	var calls int32
	stubTrains(t, &calls, 50*time.Millisecond, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := CachedTrains(context.Background(), "img", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("expected 1 shared fetch, got %d", calls)
	}
}

func TestCachedTrainsDoesNotCacheErrors(t *testing.T) {
	var calls int32
	stubTrains(t, &calls, 0, errors.New("registry down"))
	ctx := context.Background()
	if _, err := CachedTrains(ctx, "img", nil); err == nil {
		t.Fatal("expected error")
	}
	CachedTrains(ctx, "img", nil)
	if calls != 2 {
		t.Fatalf("errors must not be cached, got %d fetches", calls)
	}
}
