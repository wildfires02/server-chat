// Package concurrency 实现即时通信服务端的协议、路由和业务逻辑。
package concurrency

// SimpleMutex 是基于容量为 1 的 Channel 实现的互斥锁。
type SimpleMutex chan struct{}

// NewSimpleMutex 创建并返回一个新的 SimpleMutex 互斥锁实例。
func NewSimpleMutex() SimpleMutex {
	return make(SimpleMutex, 1)
}

// Lock 获取互斥锁（若已被占用则阻塞等待）。
func (s SimpleMutex) Lock() {
	s <- struct{}{}
}

// TryLock 尝试获取互斥锁（非阻塞）。
// 如果成功获取锁返回 true，若锁已被占用则立即返回 false。
func (s SimpleMutex) TryLock() bool {
	select {
	case s <- struct{}{}:
		return true
	default:
		return false
	}
}

// Unlock 释放互斥锁。
func (s SimpleMutex) Unlock() {
	<-s
}
