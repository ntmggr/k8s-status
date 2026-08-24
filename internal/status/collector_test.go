package status

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ntmggr/srv-status/internal/argocd"
)

type fakeLister struct {
	calls atomic.Int64
	delay time.Duration

	mu   sync.Mutex
	list *argocd.ApplicationList
	err  error
}

func (f *fakeLister) ListApplications(context.Context) (*argocd.ApplicationList, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.list, f.err
}

func (f *fakeLister) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
	f.list = nil
}

type fakeClock struct{ nanos atomic.Int64 }

func (c *fakeClock) now() time.Time { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *fakeClock) advance(d time.Duration) {
	c.nanos.Add(int64(d))
}

func newTestCollector(t *testing.T, l Lister, ttl time.Duration) (*Collector, *fakeClock) {
	t.Helper()
	clock := &fakeClock{}
	clock.nanos.Store(time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC).UnixNano())
	c := NewCollector(l, Options{RootAppName: "root-app"}, ttl)
	c.now = clock.now
	return c, clock
}

func TestConcurrentCallersCollapseToOneFetch(t *testing.T) {
	lister := &fakeLister{list: loadFixture(t), delay: 20 * time.Millisecond}
	c, _ := newTestCollector(t, lister, 15*time.Second)

	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			snap, err := c.Get(context.Background())
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if snap.Summary.Total != 14 {
				t.Errorf("total = %d, want 14", snap.Summary.Total)
			}
		}()
	}
	wg.Wait()

	if got := lister.calls.Load(); got != 1 {
		t.Errorf("upstream fetches = %d, want 1", got)
	}
}

func TestTTLExpiryTriggersRefetch(t *testing.T) {
	lister := &fakeLister{list: loadFixture(t)}
	c, clock := newTestCollector(t, lister, 15*time.Second)

	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("first get: %v", err)
	}
	clock.advance(14 * time.Second)
	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("cached get: %v", err)
	}
	if got := lister.calls.Load(); got != 1 {
		t.Fatalf("fetches within TTL = %d, want 1", got)
	}

	clock.advance(2 * time.Second)
	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("refresh get: %v", err)
	}
	if got := lister.calls.Load(); got != 2 {
		t.Errorf("fetches after TTL = %d, want 2", got)
	}
}

func TestStaleSnapshotServedOnError(t *testing.T) {
	lister := &fakeLister{list: loadFixture(t)}
	c, clock := newTestCollector(t, lister, 15*time.Second)

	fresh, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	if fresh.Stale {
		t.Error("first snapshot should not be stale")
	}

	wantErr := errors.New("kubernetes api returned 503")
	lister.setErr(wantErr)
	clock.advance(30 * time.Second)

	snap, err := c.Get(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if snap == nil {
		t.Fatal("want last good snapshot, got nil")
	}
	if !snap.Stale {
		t.Error("snapshot should be marked stale")
	}
	if snap.Summary.Total != 14 {
		t.Errorf("stale snapshot lost data: total = %d", snap.Summary.Total)
	}

	cached, err := c.Get(context.Background())
	if err == nil || cached == nil || !cached.Stale {
		t.Errorf("repeat failure should keep serving the stale snapshot")
	}
}

func TestErrorWithoutPriorSnapshot(t *testing.T) {
	lister := &fakeLister{err: errors.New("connection refused")}
	c, _ := newTestCollector(t, lister, 15*time.Second)

	snap, err := c.Get(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if snap != nil {
		t.Errorf("want nil snapshot, got %+v", snap)
	}
}
