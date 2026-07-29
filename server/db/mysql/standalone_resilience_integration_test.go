//go:build mysql
// +build mysql

// Package mysql 提供基于真实 MySQL 的单机数据库韧性回归。
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"
)

// TestStandaloneMySQLLatencyAndPoolExhaustion 使用隔离测试库验证分级查询延迟、
// 有界连接池等待超时以及连接释放后的自动恢复。
func TestStandaloneMySQLLatencyAndPoolExhaustion(t *testing.T) {
	dsn := os.Getenv("IM_TEST_STANDALONE_MYSQL_DSN")
	if dsn == "" {
		t.Skip("未设置 IM_TEST_STANDALONE_MYSQL_DSN，跳过真实 MySQL 韧性测试")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("打开单机韧性测试数据库失败: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(time.Minute)
	if err := db.Ping(); err != nil {
		t.Fatalf("单机韧性测试数据库不可用: %v", err)
	}

	t.Run("分级查询延迟", func(t *testing.T) {
		delays := []time.Duration{10 * time.Millisecond, 50 * time.Millisecond, 200 * time.Millisecond}
		for _, delay := range delays {
			started := time.Now()
			var sleepResult int
			if err := db.QueryRow("SELECT SLEEP(?)", delay.Seconds()).Scan(&sleepResult); err != nil {
				t.Fatalf("执行 %s 延迟查询失败: %v", delay, err)
			}
			elapsed := time.Since(started)
			// 调度误差可能使观测值略低于目标，保留 20% 容差。
			if elapsed < delay*8/10 {
				t.Errorf("目标延迟=%s，实际耗时=%s，延迟注入未生效", delay, elapsed)
			}
			if elapsed > delay+2*time.Second {
				t.Errorf("目标延迟=%s，实际耗时=%s，出现非预期额外阻塞", delay, elapsed)
			}
			t.Logf("数据库分级延迟：目标=%s，实际=%s", delay, elapsed)
		}
	})

	t.Run("连接池耗尽与恢复", func(t *testing.T) {
		const blockerCount = 2
		blockerErrors := make(chan error, blockerCount)
		for index := 0; index < blockerCount; index++ {
			go func() {
				var sleepResult int
				blockerErrors <- db.QueryRow("SELECT SLEEP(1)").Scan(&sleepResult)
			}()
		}

		// 确认两条数据库连接都已经被阻塞查询占用，再验证第三个请求。
		deadline := time.Now().Add(2 * time.Second)
		for db.Stats().InUse < blockerCount && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if inUse := db.Stats().InUse; inUse != blockerCount {
			t.Fatalf("连接池占用=%d，期望=%d", inUse, blockerCount)
		}

		waitStarted := time.Now()
		waitCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		err := db.PingContext(waitCtx)
		cancel()
		waitElapsed := time.Since(waitStarted)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("连接池耗尽请求错误=%v，期望 context deadline exceeded", err)
		}
		if waitElapsed < 100*time.Millisecond || waitElapsed > time.Second {
			t.Fatalf("连接池耗尽等待=%s，没有在上下文边界内结束", waitElapsed)
		}

		for index := 0; index < blockerCount; index++ {
			if err := <-blockerErrors; err != nil {
				t.Fatalf("第 %d 个阻塞查询失败: %v", index, err)
			}
		}
		if err := db.Ping(); err != nil {
			t.Fatalf("连接释放后数据库没有恢复: %v", err)
		}
		stats := db.Stats()
		if stats.MaxOpenConnections != blockerCount {
			t.Errorf(
				"最大连接数=%d，期望=%d",
				stats.MaxOpenConnections,
				blockerCount,
			)
		}
		if stats.WaitCount == 0 {
			t.Error("连接池耗尽后 WaitCount=0，未观察到有界等待")
		}
		t.Logf(
			"连接池耗尽恢复：最大连接=%d，等待次数=%d，等待耗时=%s",
			stats.MaxOpenConnections,
			stats.WaitCount,
			stats.WaitDuration,
		)
	})
}
