package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// BeginDrain 在保留租约的同时把本节点从 Owner Ring 和多数派服务节点中移除。
func (control *etcdControlPlane) BeginDrain(parent context.Context) error {
	if control == nil || control.client == nil || control.leaseID == 0 {
		return errors.New("etcd 控制面尚未启动")
	}
	if !control.draining.CompareAndSwap(false, true) {
		return nil
	}
	succeeded := false
	defer func() {
		if !succeeded {
			control.draining.Store(false)
		}
	}()

	if parent == nil {
		parent = context.Background()
	}
	drainContext, cancel := context.WithTimeout(parent, defaultControlPlaneDialTimeout)
	defer cancel()
	member := control.member
	member.Draining = true
	payload, err := json.Marshal(member)
	if err != nil {
		return fmt.Errorf("编码 Drain 成员状态失败: %w", err)
	}
	response, err := control.client.Txn(drainContext).
		If(clientv3.Compare(
			clientv3.LeaseValue(control.memberKey),
			"=",
			control.leaseID,
		)).
		Then(clientv3.OpPut(
			control.memberKey,
			string(payload),
			clientv3.WithLease(control.leaseID),
		)).
		Commit()
	if err != nil {
		return fmt.Errorf("提交 Drain 成员状态失败: %w", err)
	}
	if !response.Succeeded {
		return errors.New("提交 Drain 失败：本节点租约已经失效")
	}
	if err = control.advanceTopologyEpoch(response.Header.Revision); err != nil {
		return fmt.Errorf("推进 Drain Cluster View epoch 失败: %w", err)
	}
	if err = control.refreshView(); err != nil {
		return fmt.Errorf("应用 Drain Cluster View 失败: %w", err)
	}
	control.member = member
	succeeded = true
	return nil
}

// ClaimTask 使用独立短租约保证同一定时任务在 Owner 切换期间只有一个领取者。
func (control *etcdControlPlane) ClaimTask(
	parent context.Context,
	taskID string,
	ttl time.Duration,
) (bool, error) {
	if control == nil || control.client == nil || !control.Ready() {
		return false, errors.New("etcd 控制面未就绪，不能领取定时任务")
	}
	if strings.TrimSpace(taskID) == "" {
		return false, errors.New("定时任务 ID 不能为空")
	}
	if ttl <= 0 {
		ttl = defaultScheduledTaskClaimTTL
	}
	if parent == nil {
		parent = context.Background()
	}
	claimContext, cancel := context.WithTimeout(parent, defaultControlPlaneDialTimeout)
	defer cancel()
	leaseSeconds := int64((ttl + time.Second - 1) / time.Second)
	lease, err := control.client.Grant(claimContext, leaseSeconds)
	if err != nil {
		return false, fmt.Errorf("申请定时任务领取租约失败: %w", err)
	}
	taskHash := sha256.Sum256([]byte(taskID))
	claimKey := path.Join(
		control.config.Namespace,
		"clusters",
		control.clusterID,
		"task-claims",
		fmt.Sprintf("%x", taskHash[:]),
	)
	claimValue := fmt.Sprintf(
		"%s/%d/%d",
		control.member.Name,
		control.member.InstanceID,
		time.Now().UnixNano(),
	)
	response, err := control.client.Txn(claimContext).
		If(clientv3.Compare(clientv3.Version(claimKey), "=", 0)).
		Then(clientv3.OpPut(
			claimKey,
			claimValue,
			clientv3.WithLease(lease.ID),
		)).
		Commit()
	if err != nil {
		_, _ = control.client.Revoke(claimContext, lease.ID)
		return false, fmt.Errorf("领取定时任务失败: %w", err)
	}
	if !response.Succeeded {
		_, _ = control.client.Revoke(claimContext, lease.ID)
		return false, nil
	}
	return true, nil
}
