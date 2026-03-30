package mqttkit

import (
	"context"
	"strings"
)

// DistributedLocker 分布式互斥抽象：按逻辑 key 跨进程加锁。
//
// 本库**仅定义接口**，不提供 Redis/etcd 等任何具体实现。使用方根据自有中间件在业务包中实现
// Acquire/release（例如基于 Redlock、etcd lease、数据库 advisory lock），再在消息处理或业务逻辑中
// 通过 WithLock / ConsumerPartitionKey 调用。
//
// 典型用途：多实例平台服务共享 MQTT 订阅时，对「同一网关 / 设备」的处理串行化或选主，避免重复副作用。
type DistributedLocker interface {
	Acquire(ctx context.Context, key string) (release func(ctx context.Context) error, err error)
}

// ConsumerPartitionKey 用前缀（租户/命名空间）、网关副本标识、设备标识拼锁 key，便于各实现统一约定。
func ConsumerPartitionKey(prefix, gatewayID, deviceID string) string {
	return strings.Join([]string{prefix, gatewayID, deviceID}, ":")
}

// WithLock 在持有 key 期间执行 fn；locker 为 nil 时不加锁直接执行 fn。
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
