// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// clusterControlPlane 定义集群运行时需要的最小控制面能力。
type clusterControlPlane interface {
	// Start 注册本节点、加载初始视图并启动租约与 Watch。
	Start(context.Context, clusterMember, func(clusterView) error) error
	// Ready 判断本节点租约和多数派成员视图是否仍然有效。
	Ready() bool
	// View 返回最近一次成功加载的不可变成员视图。
	View() clusterView
	// Close 注销租约并释放客户端资源。
	Close() error
}

// etcdControlPlaneClient 是控制面使用的最小 etcd 客户端接口。
// 官方客户端与测试故障注入客户端都可实现此接口。
type etcdControlPlaneClient interface {
	Txn(context.Context) clientv3.Txn
	Grant(context.Context, int64) (*clientv3.LeaseGrantResponse, error)
	Revoke(context.Context, clientv3.LeaseID) (*clientv3.LeaseRevokeResponse, error)
	KeepAlive(context.Context, clientv3.LeaseID) (<-chan *clientv3.LeaseKeepAliveResponse, error)
	Get(context.Context, string, ...clientv3.OpOption) (*clientv3.GetResponse, error)
	Put(context.Context, string, string, ...clientv3.OpOption) (*clientv3.PutResponse, error)
	Watch(context.Context, string, ...clientv3.OpOption) clientv3.WatchChan
	Close() error
}

type etcdControlPlaneClientFactory func(clientv3.Config) (etcdControlPlaneClient, error)

func newOfficialEtcdControlPlaneClient(config clientv3.Config) (etcdControlPlaneClient, error) {
	return clientv3.New(config)
}

// etcdControlPlane 使用 etcd Lease 和线性一致读取维护集群成员视图。
type etcdControlPlane struct {
	// config 保存已经过校验的 etcd 参数。
	config clusterControlPlaneConfig
	// clusterID 标识当前节点所属的逻辑集群。
	clusterID string
	// expectedReplicas 保存首次启动副本数；运行时多数派读取已提交拓扑。
	expectedReplicas int
	// expectedMembers 是允许注册的候选节点白名单和固定地址。
	expectedMembers map[string]string
	// initialMembers 是首次创建拓扑时进入活动状态的成员集合。
	initialMembers []string

	// client 是 etcd v3 客户端；接口边界允许对租约和 Watch 做故障注入测试。
	client etcdControlPlaneClient
	// clientFactory 创建正式或测试 etcd 客户端。
	clientFactory etcdControlPlaneClientFactory
	// leaseID 是当前节点持有的 etcd 租约。
	leaseID clientv3.LeaseID
	// keepAliveChannel 持续接收 etcd 租约续期结果。
	keepAliveChannel <-chan *clientv3.LeaseKeepAliveResponse
	// member 保存当前节点注册信息。
	member clusterMember
	// memberPrefix 是当前集群成员的键前缀。
	memberPrefix string
	// memberKey 是当前节点占用的唯一成员键。
	memberKey string
	// viewEpochKey 持久记录最近一次成员变更 Revision，避免使用无关 etcd 写入推动任期。
	viewEpochKey string
	// topologyKey 保存经过 CAS 提交的活动成员集合。
	topologyKey string

	// context 控制 KeepAlive、Watch 和周期刷新协程的生命周期。
	context context.Context
	// cancel 停止所有控制面后台协程。
	cancel context.CancelFunc
	// waitGroup 等待控制面后台协程退出。
	waitGroup sync.WaitGroup
	// closeOnce 保证控制面只关闭一次。
	closeOnce sync.Once
	// refreshLock 防止 Watch 和周期任务并发刷新同一视图。
	refreshLock sync.Mutex

	// view 原子保存最近一次有效集群视图。
	view atomic.Pointer[clusterView]
	// leaseAlive 表示 KeepAlive 通道仍持有有效租约。
	leaseAlive atomic.Bool
	// viewApplied 表示最近观察到的成员视图已成功推进数据库 fence 并切换本地路由。
	viewApplied atomic.Bool
	// draining 防止并发重复更新本节点成员状态。
	draining atomic.Bool
	// lastViewRefresh 保存最近一次成功加载线性一致成员视图的 UnixNano 时间。
	lastViewRefresh atomic.Int64
	// onView 在成员视图变化时先提交数据库 fence，再通知集群路由层。
	onView func(clusterView) error
}

// newEtcdControlPlane 创建尚未连接网络的 etcd 控制面。
func newEtcdControlPlane(
	config clusterControlPlaneConfig,
	clusterID string,
	expectedReplicas int,
	expectedMembers map[string]string,
	initialMembers []string,
) (*etcdControlPlane, error) {
	normalized, err := normalizeControlPlaneConfig(config)
	if err != nil {
		return nil, err
	}
	clusterID = strings.TrimSpace(clusterID)
	if !isValidClusterIdentifier(clusterID) {
		return nil, fmt.Errorf("cluster_id 只能包含字母、数字、点、下划线和连字符")
	}
	if expectedReplicas < 3 || expectedReplicas%2 == 0 {
		return nil, fmt.Errorf("expected_replicas 必须是大于等于 3 的奇数")
	}
	if len(expectedMembers) < expectedReplicas {
		return nil, fmt.Errorf("候选节点数量 %d 小于 expected_replicas=%d",
			len(expectedMembers), expectedReplicas)
	}
	topology, err := normalizeClusterTopology(clusterTopology{
		ExpectedReplicas: expectedReplicas,
		Members:          initialMembers,
		UpdatedAt:        time.Now().UTC(),
	}, expectedMembers)
	if err != nil {
		return nil, fmt.Errorf("初始集群拓扑无效: %w", err)
	}
	return &etcdControlPlane{
		config:           normalized,
		clusterID:        clusterID,
		expectedReplicas: expectedReplicas,
		expectedMembers:  expectedMembers,
		initialMembers:   topology.Members,
		clientFactory:    newOfficialEtcdControlPlaneClient,
	}, nil
}

// Start 注册节点租约，并在返回成功前加载一次线性一致成员快照。
func (control *etcdControlPlane) Start(
	parent context.Context,
	member clusterMember,
	onView func(clusterView) error,
) error {
	if parent == nil {
		parent = context.Background()
	}
	if control.client != nil {
		return errors.New("etcd 控制面已经启动")
	}

	leaseTTL, err := time.ParseDuration(control.config.LeaseTTL)
	if err != nil {
		return fmt.Errorf("解析控制面 lease_ttl 失败: %w", err)
	}
	dialTimeout, err := time.ParseDuration(control.config.DialTimeout)
	if err != nil {
		return fmt.Errorf("解析控制面 dial_timeout 失败: %w", err)
	}

	var tlsConfig *tls.Config
	if control.config.TLS != nil {
		tlsConfig, err = loadControlPlaneTLSConfig(*control.config.TLS)
		if err != nil {
			return err
		}
	}
	clientFactory := control.clientFactory
	if clientFactory == nil {
		clientFactory = newOfficialEtcdControlPlaneClient
	}
	client, err := clientFactory(clientv3.Config{
		Endpoints:   control.config.Endpoints,
		DialTimeout: dialTimeout,
		Username:    control.config.Username,
		Password:    control.config.Password,
		TLS:         tlsConfig,
	})
	if err != nil {
		return fmt.Errorf("创建 etcd 客户端失败: %w", err)
	}

	controlContext, cancel := context.WithCancel(parent)
	control.client = client
	control.context = controlContext
	control.cancel = cancel
	control.member = member
	control.onView = onView
	control.memberPrefix = path.Join(
		control.config.Namespace,
		"clusters",
		control.clusterID,
		"members",
	) + "/"
	control.memberKey = control.memberPrefix + member.Name
	control.viewEpochKey = path.Join(
		control.config.Namespace,
		"clusters",
		control.clusterID,
		"view-epoch",
	)
	control.topologyKey = path.Join(
		control.config.Namespace,
		"clusters",
		control.clusterID,
		"topology",
	)

	if err := control.initializeTopology(); err != nil {
		control.cleanupAfterStartFailure()
		return err
	}
	if err := control.register(leaseTTL); err != nil {
		control.cleanupAfterStartFailure()
		return err
	}
	if err := control.refreshView(); err != nil {
		control.cleanupAfterStartFailure()
		return fmt.Errorf("加载初始 Cluster View 失败: %w", err)
	}

	control.waitGroup.Add(4)
	go control.keepAliveLoop(leaseTTL)
	go control.watchLoop()
	go control.watchTopologyLoop()
	go control.refreshLoop(leaseTTL)
	return nil
}

// initializeTopology 仅在集群首次启动时创建活动成员集合。
// 后续节点（包括扩容 Joining 节点）只能接受已有拓扑，不能用本地配置覆盖它。
func (control *etcdControlPlane) initializeTopology() error {
	topology, err := normalizeClusterTopology(clusterTopology{
		ExpectedReplicas: control.expectedReplicas,
		Members:          control.initialMembers,
		UpdatedAt:        time.Now().UTC(),
	}, control.expectedMembers)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(topology)
	if err != nil {
		return fmt.Errorf("编码初始集群拓扑失败: %w", err)
	}
	response, err := control.client.Txn(control.context).
		If(clientv3.Compare(clientv3.Version(control.topologyKey), "=", 0)).
		Then(clientv3.OpPut(control.topologyKey, string(payload))).
		Else(clientv3.OpGet(control.topologyKey)).
		Commit()
	if err != nil {
		return fmt.Errorf("初始化集群拓扑失败: %w", err)
	}
	if response.Succeeded {
		return nil
	}
	rangeResponse := response.Responses[0].GetResponseRange()
	if len(rangeResponse.Kvs) != 1 {
		return errors.New("读取已有集群拓扑失败")
	}
	if _, err = decodeClusterTopology(rangeResponse.Kvs[0].Value, control.expectedMembers); err != nil {
		return fmt.Errorf("已有集群拓扑无效: %w", err)
	}
	return nil
}

// register 使用事务保证相同节点名称不会被两个存活实例同时占用。
func (control *etcdControlPlane) register(leaseTTL time.Duration) error {
	expectedAddress, allowed := control.expectedMembers[control.member.Name]
	if !allowed || expectedAddress != control.member.Address {
		return fmt.Errorf("节点 %q 的身份或广播地址不在候选节点白名单中", control.member.Name)
	}
	if control.member.ProtocolVersion < clusterMinProtocolVersion ||
		control.member.ProtocolVersion > clusterProtocolVersion {
		return fmt.Errorf(
			"节点 %q 的集群协议版本 %d 不在兼容范围 [%d,%d]",
			control.member.Name,
			control.member.ProtocolVersion,
			clusterMinProtocolVersion,
			clusterProtocolVersion,
		)
	}
	timeoutContext, cancel := context.WithTimeout(control.context, leaseTTL)
	defer cancel()

	lease, err := control.client.Grant(timeoutContext, int64(leaseTTL/time.Second))
	if err != nil {
		return fmt.Errorf("申请 etcd 节点租约失败: %w", err)
	}
	control.leaseID = lease.ID

	payload, err := json.Marshal(control.member)
	if err != nil {
		return fmt.Errorf("编码集群成员失败: %w", err)
	}
	response, err := control.client.Txn(timeoutContext).
		If(clientv3.Compare(clientv3.Version(control.memberKey), "=", 0)).
		Then(clientv3.OpPut(control.memberKey, string(payload), clientv3.WithLease(control.leaseID))).
		Commit()
	if err != nil {
		_, _ = control.client.Revoke(timeoutContext, lease.ID)
		return fmt.Errorf("注册集群成员失败: %w", err)
	}
	if !response.Succeeded {
		_, _ = control.client.Revoke(timeoutContext, lease.ID)
		return fmt.Errorf("集群节点名称 %q 已被其他存活实例占用", control.member.Name)
	}

	keepAlive, err := control.client.KeepAlive(control.context, control.leaseID)
	if err != nil {
		_, _ = control.client.Revoke(timeoutContext, lease.ID)
		return fmt.Errorf("启动 etcd 租约续期失败: %w", err)
	}
	control.leaseAlive.Store(true)
	control.keepAliveChannel = keepAlive
	return nil
}

// Ready 判断租约、etcd 联系时间、本节点身份和多数派成员是否均有效。
func (control *etcdControlPlane) Ready() bool {
	if !control.leaseAlive.Load() || !control.viewApplied.Load() {
		return false
	}
	leaseTTL, err := time.ParseDuration(control.config.LeaseTTL)
	if err != nil {
		return false
	}
	lastViewRefresh := time.Unix(0, control.lastViewRefresh.Load())
	if time.Since(lastViewRefresh) > leaseTTL {
		return false
	}

	view := control.view.Load()
	if view == nil || !view.hasServingMember(control.member.Name, control.member.InstanceID) {
		return false
	}
	requiredMembers := view.ExpectedReplicas/2 + 1
	return view.servingMemberCount() >= requiredMembers
}

// View 返回最近一次成功加载的成员视图副本。
func (control *etcdControlPlane) View() clusterView {
	view := control.view.Load()
	if view == nil {
		return clusterView{}
	}
	return clusterView{
		Epoch:            view.Epoch,
		ExpectedReplicas: view.ExpectedReplicas,
		Members:          append([]clusterMember(nil), view.Members...),
	}
}

// ChangeTopology 原子提交一次 3↔5 等相邻奇数规模变更。
// 扩容要求新增节点已经注册且未 Drain；缩容要求待移除节点先完成 Drain。
// 同规模替换必须拆成“先扩容、再缩容”，从而始终保留旧拓扑多数派。
func (control *etcdControlPlane) ChangeTopology(
	parent context.Context,
	desiredMembers []string,
) (clusterView, error) {
	if control == nil || control.client == nil {
		return clusterView{}, errors.New("etcd 控制面尚未启动")
	}
	if !control.Ready() {
		return clusterView{}, errors.New("当前节点没有活动拓扑多数派，拒绝变更成员")
	}
	if parent == nil {
		parent = context.Background()
	}
	changeContext, cancel := context.WithTimeout(parent, defaultControlPlaneDialTimeout)
	defer cancel()

	response, err := control.client.Txn(changeContext).Then(
		clientv3.OpGet(control.topologyKey),
		clientv3.OpGet(control.memberPrefix, clientv3.WithPrefix()),
	).Commit()
	if err != nil {
		return clusterView{}, fmt.Errorf("读取当前集群拓扑失败: %w", err)
	}
	topologyResponse := response.Responses[0].GetResponseRange()
	if len(topologyResponse.Kvs) != 1 {
		return clusterView{}, errors.New("当前集群拓扑不存在")
	}
	currentTopology, err := decodeClusterTopology(
		topologyResponse.Kvs[0].Value,
		control.expectedMembers,
	)
	if err != nil {
		return clusterView{}, err
	}
	desiredTopology, err := normalizeClusterTopology(clusterTopology{
		ExpectedReplicas: len(desiredMembers),
		Members:          desiredMembers,
		UpdatedAt:        time.Now().UTC(),
	}, control.expectedMembers)
	if err != nil {
		return clusterView{}, err
	}
	if equalStringSlices(currentTopology.Members, desiredTopology.Members) {
		return control.View(), nil
	}

	expanding := len(desiredTopology.Members) == len(currentTopology.Members)+2 &&
		isStringSubset(currentTopology.Members, desiredTopology.Members)
	shrinking := len(desiredTopology.Members)+2 == len(currentTopology.Members) &&
		isStringSubset(desiredTopology.Members, currentTopology.Members)
	if !expanding && !shrinking {
		return clusterView{}, errors.New(
			"每次只能扩容或缩容两个节点；同规模替换必须先扩容再缩容",
		)
	}

	memberResponse := response.Responses[1].GetResponseRange()
	registered, err := decodeRegisteredMembers(memberResponse.Kvs, control.expectedMembers)
	if err != nil {
		return clusterView{}, err
	}
	for _, name := range desiredTopology.Members {
		member, exists := registered[name]
		if !exists {
			return clusterView{}, fmt.Errorf("目标成员 %q 尚未注册，不能提交拓扑", name)
		}
		if member.Draining {
			return clusterView{}, fmt.Errorf("目标成员 %q 仍处于 Drain 状态", name)
		}
		if member.ProtocolVersion < clusterMinProtocolVersion ||
			member.ProtocolVersion > clusterProtocolVersion {
			return clusterView{}, fmt.Errorf("目标成员 %q 的协议版本不兼容", name)
		}
	}
	if shrinking {
		desiredSet := stringSet(desiredTopology.Members)
		for _, name := range currentTopology.Members {
			if _, remains := desiredSet[name]; remains {
				continue
			}
			member, exists := registered[name]
			if !exists || !member.Draining {
				return clusterView{}, fmt.Errorf("待移除成员 %q 必须先完成 Drain", name)
			}
		}
	}

	payload, err := json.Marshal(desiredTopology)
	if err != nil {
		return clusterView{}, fmt.Errorf("编码目标集群拓扑失败: %w", err)
	}
	transaction, err := control.client.Txn(changeContext).
		If(clientv3.Compare(
			clientv3.ModRevision(control.topologyKey),
			"=",
			topologyResponse.Kvs[0].ModRevision,
		)).
		Then(clientv3.OpPut(control.topologyKey, string(payload))).
		Commit()
	if err != nil {
		return clusterView{}, fmt.Errorf("提交目标集群拓扑失败: %w", err)
	}
	if !transaction.Succeeded {
		return clusterView{}, errors.New("集群拓扑已被其他操作修改，请重新读取后重试")
	}
	if err = control.advanceTopologyEpoch(transaction.Header.Revision); err != nil {
		return clusterView{}, fmt.Errorf("推进扩缩容 Cluster View epoch 失败: %w", err)
	}
	if err = control.refreshView(); err != nil {
		return clusterView{}, fmt.Errorf("应用扩缩容 Cluster View 失败: %w", err)
	}
	return control.View(), nil
}

// Close 注销当前节点租约并停止后台任务。
func (control *etcdControlPlane) Close() error {
	var closeError error
	control.closeOnce.Do(func() {
		if control.cancel != nil {
			control.cancel()
		}
		control.waitGroup.Wait()
		control.leaseAlive.Store(false)

		if control.client == nil {
			return
		}
		if control.leaseID != 0 {
			revokeContext, cancel := context.WithTimeout(context.Background(), defaultControlPlaneDialTimeout)
			_, closeError = control.client.Revoke(revokeContext, control.leaseID)
			cancel()
		}
		if err := control.client.Close(); closeError == nil {
			closeError = err
		}
	})
	return closeError
}

// cleanupAfterStartFailure 回收部分完成的 etcd 初始化资源。
func (control *etcdControlPlane) cleanupAfterStartFailure() {
	control.leaseAlive.Store(false)
	if control.cancel != nil {
		control.cancel()
	}
	if control.client != nil {
		if control.leaseID != 0 {
			revokeContext, cancel := context.WithTimeout(context.Background(), defaultControlPlaneDialTimeout)
			_, _ = control.client.Revoke(revokeContext, control.leaseID)
			cancel()
		}
		_ = control.client.Close()
		control.client = nil
	}
}

// refreshView 线性一致读取成员前缀并发布新的 Cluster View。
func (control *etcdControlPlane) refreshView() error {
	control.refreshLock.Lock()
	defer control.refreshLock.Unlock()

	response, err := control.client.Txn(control.context).Then(
		clientv3.OpGet(control.memberPrefix, clientv3.WithPrefix()),
		clientv3.OpGet(control.viewEpochKey),
		clientv3.OpGet(control.topologyKey),
	).Commit()
	if err != nil {
		return err
	}
	memberResponse := response.Responses[0].GetResponseRange()
	epochResponse := response.Responses[1].GetResponseRange()
	topologyResponse := response.Responses[2].GetResponseRange()
	if len(topologyResponse.Kvs) != 1 {
		return errors.New("成员视图不包含已提交拓扑")
	}
	topology, err := decodeClusterTopology(
		topologyResponse.Kvs[0].Value,
		control.expectedMembers,
	)
	if err != nil {
		return err
	}
	epoch := maxMemberRevision(memberResponse.Kvs)
	if topologyResponse.Kvs[0].ModRevision > epoch {
		epoch = topologyResponse.Kvs[0].ModRevision
	}
	if len(epochResponse.Kvs) > 0 {
		persistedEpoch, parseErr := strconv.ParseInt(string(epochResponse.Kvs[0].Value), 10, 64)
		if parseErr != nil || persistedEpoch <= 0 {
			return fmt.Errorf("解析成员视图 epoch %q 失败", epochResponse.Kvs[0].Value)
		}
		if persistedEpoch > epoch {
			epoch = persistedEpoch
		}
	}
	if epoch <= 0 {
		return errors.New("成员视图不包含有效 epoch")
	}
	view, err := buildClusterView(
		epoch,
		memberResponse.Kvs,
		control.expectedMembers,
		topology,
	)
	if err != nil {
		return err
	}
	current := control.view.Load()
	if current != nil && current.Epoch == view.Epoch {
		control.lastViewRefresh.Store(time.Now().UnixNano())
		return nil
	}
	// 已观察到新视图但尚未完成数据库 fence 时立即摘除 Readiness，不能继续使用旧任期写入。
	control.viewApplied.Store(false)
	if control.onView != nil {
		if err := control.onView(view); err != nil {
			return fmt.Errorf("应用 Cluster View epoch=%d 失败: %w", view.Epoch, err)
		}
	}
	// 只有数据库 fencing 和本地路由都应用成功后，才公开新视图并刷新 Ready 时钟。
	control.view.Store(&view)
	control.viewApplied.Store(true)
	control.lastViewRefresh.Store(time.Now().UnixNano())
	return nil
}

// advanceTopologyEpoch 使用 CAS 单调记录成员 Put/Delete 事件的 Revision。
// Marker 位于 members 前缀之外，不会反过来触发成员 Watch。
func (control *etcdControlPlane) advanceTopologyEpoch(epoch int64) error {
	if epoch <= 0 {
		return errors.New("成员变更 epoch 必须大于 0")
	}
	value := fmt.Sprintf("%020d", epoch)
	for {
		response, err := control.client.Get(control.context, control.viewEpochKey)
		if err != nil {
			return err
		}
		var revision int64
		if len(response.Kvs) > 0 {
			revision = response.Kvs[0].ModRevision
			current, parseErr := strconv.ParseInt(string(response.Kvs[0].Value), 10, 64)
			if parseErr != nil {
				return fmt.Errorf("解析已有成员视图 epoch 失败: %w", parseErr)
			}
			if current >= epoch {
				return nil
			}
		}
		transaction, err := control.client.Txn(control.context).
			If(clientv3.Compare(clientv3.ModRevision(control.viewEpochKey), "=", revision)).
			Then(clientv3.OpPut(control.viewEpochKey, value)).
			Commit()
		if err != nil {
			return err
		}
		if transaction.Succeeded {
			return nil
		}
	}
}
