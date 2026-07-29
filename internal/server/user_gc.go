package server

import (
	"math/rand"
	"time"

	"chat/server/logs"
	"chat/server/store"
)

// garbageCollectUsers 每隔 'period' 运行一次，最多删除 'blockSize' 个
// 过期的未验证用户账号，这些账号至少 'minAccountAgeHours' 小时未更新
// 返回可用于停止过程的 Channel
func garbageCollectUsers(period time.Duration, blockSize, minAccountAgeHours int) chan<- bool {
	// 无缓冲停止 Channel。停止 GC 的人必须等待过程完成
	stop := make(chan bool)
	go func() {
		// 为 tick 周期添加随机性以去同步集群节点的运行：
		// 0.75 * period + rand(0, 0.5) * period
		period = period - (period >> 2) + time.Duration(rand.Intn(int(period>>1)))
		gcTicker := time.Tick(period)
		logs.Info.Printf("Stale account GC started with period %s, block size %d, min account age %d hours",
			period.Round(time.Second), blockSize, minAccountAgeHours)
		staleAge := time.Hour * time.Duration(minAccountAgeHours)
		for {
			select {
			case <-gcTicker:
				if uids, err := store.Users.GetUnvalidated(time.Now().Add(-staleAge), blockSize); err == nil {
					if len(uids) > 0 {
						logs.Info.Println("Stale account GC will delete uids:", uids)
						for _, uid := range uids {
							if err = store.Users.Delete(uid, true); err != nil {
								logs.Warn.Printf("Stale account GC failed to delete %s: %+v", uid.UserId(), err)
							}
						}
					}
				} else {
					logs.Warn.Println("Stale account GC error:", err)
				}
			case <-stop:
				return
			}
		}
	}()

	return stop
}
