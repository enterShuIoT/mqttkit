package mqttkit_test

import (
	"context"
	"sync"
	"testing"

	"github.com/enterShuIoT/mqttkit"
)

// inProcLocker 是测试用的进程内分布式锁占位实现（非真分布式）。
// 生产环境请用 Redis/etcd/SQL 等实现 mqttkit.DistributedLocker，保证多副本间互斥。
type inProcLocker struct {
	mu sync.Mutex
	cv sync.Cond
	n  map[string]int // 每个 key 占用计数（不支持可重入）
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

// TestWithLock_nilLocker 验证 locker 为 nil 时 WithLock 直接执行回调。
func TestWithLock_nilLocker(t *testing.T) {
	ctx := context.Background()
	err := mqttkit.WithLock(ctx, nil, "k", func(ctx context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
}

// TestWithLock_inProc 验证 WithLock 与简单锁配合可正常进出临界区。
func TestWithLock_inProc(t *testing.T) {
	l := newInProcLocker()
	ctx := context.Background()
	err := mqttkit.WithLock(ctx, l, "seq:1", func(ctx context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
}
