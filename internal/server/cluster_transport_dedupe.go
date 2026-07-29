package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"chat/api/pbx"
)

var (
	// errClusterDedupeWindowFull 表示去重窗口全被执行中的请求占用。
	errClusterDedupeWindowFull = errors.New("cluster: Request ID 去重窗口已满")
	// errClusterRequestIDCollision 表示相同 Request ID 对应了不同业务负载。
	errClusterRequestIDCollision = errors.New("cluster: Request ID 对应的负载不一致")
)

// clusterDedupeEntry 保存一次可靠调用的执行状态和最终响应。
type clusterDedupeEntry struct {
	// kind 是请求的稳定协议类型。
	kind pbx.ClusterFrameKind
	// digest 用于拒绝 Request ID 碰撞或错误复用。
	digest [sha256.Size]byte
	// ready 在首次执行完成后关闭，唤醒并发重试。
	ready chan struct{}
	// response 是成功或业务失败时需要回放的响应负载。
	response []byte
	// errorText 是需要原样回放给调用方的业务错误。
	errorText string
	// completed 表示首次执行已经结束。
	completed bool
	// expiresAt 是记录允许被清理的时间。
	expiresAt time.Time
}

// clusterDedupeCache 是有容量和 TTL 上限的节点级 Request ID 去重窗口。
type clusterDedupeCache struct {
	// lock 保护所有去重记录。
	lock sync.Mutex
	// entries 按来源实例和 Request ID 索引执行结果。
	entries map[string]*clusterDedupeEntry
	// capacity 限制异常流量下的最大内存占用。
	capacity int
	// ttl 决定完成结果可被重放的时间窗。
	ttl time.Duration
}

// newClusterDedupeCache 创建空的有界去重窗口。
func newClusterDedupeCache(capacity int, ttl time.Duration) *clusterDedupeCache {
	return &clusterDedupeCache{
		entries:  make(map[string]*clusterDedupeEntry, capacity),
		capacity: capacity,
		ttl:      ttl,
	}
}

// Execute 保证相同来源实例和 Request ID 的业务函数在窗口内最多执行一次。
func (cache *clusterDedupeCache) Execute(
	context context.Context,
	key string,
	kind pbx.ClusterFrameKind,
	payload []byte,
	dispatch func() ([]byte, error),
) ([]byte, error) {
	if cache == nil {
		return dispatch()
	}
	digest := sha256.Sum256(payload)
	now := time.Now()

	cache.lock.Lock()
	if entry := cache.entries[key]; entry != nil {
		if entry.kind != kind || entry.digest != digest {
			cache.lock.Unlock()
			return nil, errClusterRequestIDCollision
		}
		if entry.completed && !entry.expiresAt.After(now) {
			delete(cache.entries, key)
		} else {
			ready := entry.ready
			cache.lock.Unlock()
			statsInc("ClusterDedupeHits", 1)
			select {
			case <-ready:
				return cloneClusterDedupeResult(entry)
			case <-context.Done():
				return nil, context.Err()
			}
		}
	}
	if len(cache.entries) >= cache.capacity && !cache.evictOneCompletedLocked(now) {
		cache.lock.Unlock()
		return nil, errClusterDedupeWindowFull
	}
	entry := &clusterDedupeEntry{
		kind:   kind,
		digest: digest,
		ready:  make(chan struct{}),
	}
	cache.entries[key] = entry
	cache.lock.Unlock()

	response, err := dispatch()
	cache.lock.Lock()
	entry.response = append([]byte(nil), response...)
	if err != nil {
		entry.errorText = err.Error()
	}
	entry.completed = true
	entry.expiresAt = time.Now().Add(cache.ttl)
	close(entry.ready)
	cache.lock.Unlock()
	return append([]byte(nil), response...), err
}

// evictOneCompletedLocked 只清理已过 TTL 的记录，不缩短已承诺的去重窗口。
func (cache *clusterDedupeCache) evictOneCompletedLocked(now time.Time) bool {
	for key, entry := range cache.entries {
		if !entry.completed {
			continue
		}
		if !entry.expiresAt.After(now) {
			delete(cache.entries, key)
			statsInc("ClusterDedupeEvictions", 1)
			return true
		}
	}
	return false
}

// cloneClusterDedupeResult 复制缓存结果，避免调用方修改共享切片。
func cloneClusterDedupeResult(entry *clusterDedupeEntry) ([]byte, error) {
	response := append([]byte(nil), entry.response...)
	if entry.errorText != "" {
		return response, fmt.Errorf("cluster: 去重响应回放: %s", entry.errorText)
	}
	return response, nil
}
