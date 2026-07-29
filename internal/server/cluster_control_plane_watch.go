package server

import (
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// keepAliveLoop 持续消费续租响应。租约丢失后先 fail-closed，再尝试重新注册。
func (control *etcdControlPlane) keepAliveLoop(leaseTTL time.Duration) {
	defer control.waitGroup.Done()
	for {
		keepAlive := control.keepAliveChannel
		leaseLost := false
		for !leaseLost {
			select {
			case response, open := <-keepAlive:
				if !open || response == nil {
					leaseLost = true
				}
			case <-control.context.Done():
				return
			}
		}
		control.leaseAlive.Store(false)
		control.viewApplied.Store(false)
		if control.context.Err() != nil {
			return
		}
		if control.recoverLease(leaseTTL) {
			continue
		}
		return
	}
}

func (control *etcdControlPlane) recoverLease(leaseTTL time.Duration) bool {
	retryDelay := 200 * time.Millisecond
	maxRetryDelay := leaseTTL / 3
	if maxRetryDelay < retryDelay {
		maxRetryDelay = retryDelay
	}
	for control.context.Err() == nil {
		if err := control.register(leaseTTL); err == nil {
			_ = control.refreshView()
			return true
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-timer.C:
		case <-control.context.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return false
		}
		if retryDelay < maxRetryDelay {
			retryDelay *= 2
			if retryDelay > maxRetryDelay {
				retryDelay = maxRetryDelay
			}
		}
	}
	return false
}

// watchLoop 监听成员变化；Watch 中断后从最近 epoch 自动恢复。
func (control *etcdControlPlane) watchLoop() {
	control.watchKeyLoop(control.memberPrefix, true)
}

// watchTopologyLoop 监听显式扩缩容提交。
func (control *etcdControlPlane) watchTopologyLoop() {
	control.watchKeyLoop(control.topologyKey, false)
}

func (control *etcdControlPlane) watchKeyLoop(key string, prefix bool) {
	defer control.waitGroup.Done()
	for {
		view := control.View()
		options := []clientv3.OpOption{clientv3.WithRev(view.Epoch + 1)}
		if prefix {
			options = append(options, clientv3.WithPrefix())
		}
		watch := control.client.Watch(control.context, key, options...)
		for response := range watch {
			if err := response.Err(); err != nil {
				break
			}
			if err := control.advanceTopologyEpoch(response.Header.Revision); err != nil {
				break
			}
			if err := control.refreshView(); err != nil {
				break
			}
		}
		if control.context.Err() != nil {
			return
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-timer.C:
		case <-control.context.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
	}
}

func (control *etcdControlPlane) refreshLoop(leaseTTL time.Duration) {
	defer control.waitGroup.Done()
	ticker := time.NewTicker(leaseTTL / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = control.refreshView()
		case <-control.context.Done():
			return
		}
	}
}
