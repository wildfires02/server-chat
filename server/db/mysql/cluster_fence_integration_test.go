//go:build mysql

package mysql

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"chat/server/store/types"
)

// TestClusterFencingIntegration 使用真实 MySQL 8 验证旧 Owner 无法越过数据库 fence。
// 默认跳过；设置 IM_TEST_MYSQL_FENCE_DSN 后会重建 DSN 指向的测试数据库。
func TestClusterFencingIntegration(t *testing.T) {
	dsn := os.Getenv("IM_TEST_MYSQL_FENCE_DSN")
	if dsn == "" {
		t.Skip("未设置 IM_TEST_MYSQL_FENCE_DSN")
	}

	database := &adapter{}
	config, err := json.Marshal(map[string]any{"dsn": dsn, "sql_timeout": 10})
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Open(config); err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err = database.CreateDb(true); err != nil {
		t.Fatal(err)
	}

	now := types.TimeNow()
	if _, err = database.db.Exec(
		"INSERT INTO users(id,createdat,updatedat,state) VALUES(?,?,?,?)",
		int64(0), now, now, types.StateOK,
	); err != nil {
		t.Fatal(err)
	}

	verifyClusterFenceLifecycle(t, database, now)
}

// verifyClusterFenceLifecycle 覆盖首次声明、旧任期拒绝、新 Owner 接管和 fence 防回退。
func verifyClusterFenceLifecycle(t *testing.T, database *adapter, now time.Time) {
	t.Helper()
	const clusterID = "im-fence-integration"
	if err := database.ClusterFenceAdvance(clusterID, 10); err != nil {
		t.Fatal(err)
	}
	if err := database.MessageSaveAtomic(fencedMySQLMessage(now, 1, clusterID, 10, "im-0")); err != nil {
		t.Fatalf("首次 Owner 写入失败：%v", err)
	}
	if err := database.ClusterFenceAdvance(clusterID, 11); err != nil {
		t.Fatal(err)
	}
	if err := database.MessageSaveAtomic(fencedMySQLMessage(now, 2, clusterID, 10, "im-0")); !errors.Is(err, types.ErrClusterFenced) {
		t.Fatalf("旧 Owner 写入错误 = %v，期望 %v", err, types.ErrClusterFenced)
	}
	if err := database.MessageSaveAtomic(fencedMySQLMessage(now, 2, clusterID, 11, "im-1")); err != nil {
		t.Fatalf("新 Owner 接管写入失败：%v", err)
	}
	if err := database.ClusterFenceAdvance(clusterID, 10); !errors.Is(err, types.ErrClusterFenced) {
		t.Fatalf("fence 回退错误 = %v，期望 %v", err, types.ErrClusterFenced)
	}

	var owner string
	var epoch int64
	var sequence int
	if err := database.db.QueryRow(
		"SELECT clusterowner,clusterepoch,seqid FROM topics WHERE name=?", "sys").
		Scan(&owner, &epoch, &sequence); err != nil {
		t.Fatal(err)
	}
	if owner != "im-1" || epoch != 11 || sequence != 2 {
		t.Fatalf("Topic fence 状态 = (%q,%d,%d)，期望 (im-1,11,2)", owner, epoch, sequence)
	}
}

// fencedMySQLMessage 创建只用于 MySQL 集成测试的集群消息。
func fencedMySQLMessage(
	now time.Time,
	sequence int,
	clusterID string,
	epoch int64,
	owner string,
) *types.Message {
	return &types.Message{
		ObjHeader: types.ObjHeader{CreatedAt: now, UpdatedAt: now},
		SeqId:     sequence,
		Topic:     "sys",
		ClusterId: clusterID, ClusterEpoch: epoch, ClusterOwner: owner,
		Content: "fence integration",
	}
}
