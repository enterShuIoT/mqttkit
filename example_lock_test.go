package mqttkit_test

import (
	"context"
	"sync"
	"testing"

	"github.com/enterShuIoT/mqttkit"
)

// inProcLocker is a minimal mqttkit.DistributedLocker for tests (single process only).
// Production: use Redis/etcd/SQL etc. so only one gateway replica handles a device at a time.
type inProcLocker struct {
	mu sync.Mutex
	cv sync.Cond
	n  map[string]int // ref-count per key (re-entrant not supported)
}

func newInProcLocker() *inProcLocker {
	l := &inProcLocker{n: make(map[string]int)}
	l.cv.L = &l.mu
	return l
}

func (l *inProcLocker) Acquire(ctx context.Context, key string) (func(context.Context) error, error) {
	l.mu.Lock()
	for l.n[key] > 0 {
		l.cv.Wait()
		select {
		case <-ctx.Done():
			l.mu.Unlock()
			return nil, ctx.Err()
		default:
		}
	}
	l.n[key] = 1
	l.mu.Unlock()

	release := func(context.Context) error {
		l.mu.Lock()
		delete(l.n, key)
		l.cv.Broadcast()
		l.mu.Unlock()
		return nil
	}
	return release, nil
}

func TestWithLock_nilLocker(t *testing.T) {
	ctx := context.Background()
	err := mqttkit.WithLock(ctx, nil, "k", func(ctx context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
}

func TestWithLock_inProc(t *testing.T) {
	l := newInProcLocker()
	ctx := context.Background()
	err := mqttkit.WithLock(ctx, l, "seq:1", func(ctx context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
}
