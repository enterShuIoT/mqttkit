package mqttkit

import (
	"context"
	"strings"
)

// DistributedLocker provides cross-process mutual exclusion by logical key.
// Typical use: several gateway instances subscribe to the same MQTT topics; only the
// instance that acquires the lock for a given device (or gateway–device pair) should
// process the message, avoiding duplicate side effects while keeping a stable pairing
// between one device and one handling instance.
//
// Implementations may use Redis, etcd, database advisory locks, etc. A nil locker skips locking.
//
// Contract: Acquire respects ctx cancellation; release must be called and may be idempotent.
type DistributedLocker interface {
	Acquire(ctx context.Context, key string) (release func(ctx context.Context) error, err error)
}

// ConsumerPartitionKey builds a lock key from a caller-chosen prefix (tenant/namespace),
// gateway replica identity, and device identity. Use in message handlers before business logic.
func ConsumerPartitionKey(prefix, gatewayID, deviceID string) string {
	return strings.Join([]string{prefix, gatewayID, deviceID}, ":")
}

// WithLock runs fn while holding key. If locker is nil, fn runs without locking.
func WithLock(ctx context.Context, locker DistributedLocker, key string, fn func(ctx context.Context) error) error {
	if locker == nil {
		return fn(ctx)
	}
	release, err := locker.Acquire(ctx, key)
	if err != nil {
		return err
	}
	defer func() { _ = release(ctx) }()
	return fn(ctx)
}
