package server

import (
	"time"

	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

const (
	minimumMessageRetentionDays = 1
	maximumMessageRetentionDays = 3650
	maximumRetentionBatchSize   = 10000
	maximumRetentionBatches     = 10
)

// startMessageRetention 启动消息主库保留期清理器。
// 清理会移除正文、发送者、搜索文本和附件引用，但保留 SeqId 墓碑，
// 避免 Topic 同步游标因历史清理而倒退或产生歧义。
func startMessageRetention(config *messageRetentionConfig) func() {
	if config == nil || !config.Enabled {
		return func() {}
	}
	if config.Days < minimumMessageRetentionDays || config.Days > maximumMessageRetentionDays ||
		config.ScanPeriod < 1 || config.BatchSize < 1 || config.BatchSize > maximumRetentionBatchSize {
		logs.Err.Fatalln("Invalid message_retention config")
	}

	retention := time.Duration(config.Days) * 24 * time.Hour
	period := time.Duration(config.ScanPeriod) * time.Second
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		retireExpiredMessages(retention, config.BatchSize)
		for {
			select {
			case <-ticker.C:
				retireExpiredMessages(retention, config.BatchSize)
			case <-stop:
				return
			}
		}
	}()
	logs.Info.Printf("Message retention enabled: %d days, batch %d", config.Days, config.BatchSize)
	return func() {
		close(stop)
		<-done
		logs.Info.Println("Stopped message retention worker")
	}
}

func retireExpiredMessages(retention time.Duration, batchSize int) {
	cutoff := types.TimeNow().Add(-retention)
	retired := 0
	for batch := 0; batch < maximumRetentionBatches; batch++ {
		messageIDs, err := store.Messages.RetireExpired(cutoff, batchSize)
		if err != nil {
			logs.Warn.Printf("message retention cleanup failed: %v", err)
			return
		}
		retired += len(messageIDs)
		if len(messageIDs) < batchSize {
			break
		}
	}
	if retired > 0 {
		logs.Info.Printf("Message retention retired %d message(s) older than %s",
			retired, cutoff.Format(types.TimeFormatRFC3339))
	}
}
