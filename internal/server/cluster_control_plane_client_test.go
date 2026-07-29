package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type scriptedEtcdTxn struct {
	client *scriptedEtcdClient
}

func (transaction *scriptedEtcdTxn) If(...clientv3.Cmp) clientv3.Txn {
	return transaction
}

func (transaction *scriptedEtcdTxn) Then(...clientv3.Op) clientv3.Txn {
	return transaction
}

func (transaction *scriptedEtcdTxn) Else(...clientv3.Op) clientv3.Txn {
	return transaction
}

func (transaction *scriptedEtcdTxn) Commit() (*clientv3.TxnResponse, error) {
	transaction.client.lock.Lock()
	defer transaction.client.lock.Unlock()
	if len(transaction.client.txnResponses) == 0 {
		return nil, errors.New("unexpected etcd transaction")
	}
	response := transaction.client.txnResponses[0]
	transaction.client.txnResponses = transaction.client.txnResponses[1:]
	var err error
	if len(transaction.client.txnErrors) > 0 {
		err = transaction.client.txnErrors[0]
		transaction.client.txnErrors = transaction.client.txnErrors[1:]
	}
	return response, err
}

type scriptedEtcdClient struct {
	lock sync.Mutex

	txnResponses []*clientv3.TxnResponse
	txnErrors    []error
	grantIDs     []clientv3.LeaseID
	grantError   error
	keepAlive    chan *clientv3.LeaseKeepAliveResponse
	keepAliveErr error
	revoked      []clientv3.LeaseID
	watchStarted chan string
}

func (client *scriptedEtcdClient) Txn(context.Context) clientv3.Txn {
	return &scriptedEtcdTxn{client: client}
}

func (client *scriptedEtcdClient) Grant(
	context.Context,
	int64,
) (*clientv3.LeaseGrantResponse, error) {
	if client.grantError != nil {
		return nil, client.grantError
	}
	if len(client.grantIDs) == 0 {
		return nil, errors.New("unexpected lease grant")
	}
	id := client.grantIDs[0]
	client.grantIDs = client.grantIDs[1:]
	return &clientv3.LeaseGrantResponse{ID: id}, nil
}

func (client *scriptedEtcdClient) Revoke(
	_ context.Context,
	id clientv3.LeaseID,
) (*clientv3.LeaseRevokeResponse, error) {
	client.revoked = append(client.revoked, id)
	return &clientv3.LeaseRevokeResponse{}, nil
}

func (client *scriptedEtcdClient) KeepAlive(
	context.Context,
	clientv3.LeaseID,
) (<-chan *clientv3.LeaseKeepAliveResponse, error) {
	return client.keepAlive, client.keepAliveErr
}

func (*scriptedEtcdClient) Get(
	context.Context,
	string,
	...clientv3.OpOption,
) (*clientv3.GetResponse, error) {
	return &clientv3.GetResponse{}, nil
}

func (*scriptedEtcdClient) Put(
	context.Context,
	string,
	string,
	...clientv3.OpOption,
) (*clientv3.PutResponse, error) {
	return &clientv3.PutResponse{}, nil
}

func (client *scriptedEtcdClient) Watch(
	ctx context.Context,
	key string,
	_ ...clientv3.OpOption,
) clientv3.WatchChan {
	if client.watchStarted != nil {
		client.watchStarted <- key
	}
	responses := make(chan clientv3.WatchResponse)
	go func() {
		<-ctx.Done()
		close(responses)
	}()
	return responses
}

func (*scriptedEtcdClient) Close() error {
	return nil
}

func TestEtcdControlPlaneRegisterLease(t *testing.T) {
	keepAlive := make(chan *clientv3.LeaseKeepAliveResponse, 1)
	client := &scriptedEtcdClient{
		txnResponses: []*clientv3.TxnResponse{{Succeeded: true}},
		grantIDs:     []clientv3.LeaseID{17},
		keepAlive:    keepAlive,
	}
	controlContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	control := &etcdControlPlane{
		client:          client,
		context:         controlContext,
		expectedMembers: map[string]string{"im-0": "im-0:12000"},
		memberKey:       "/members/im-0",
		member: clusterMember{
			Name:            "im-0",
			Address:         "im-0:12000",
			ProtocolVersion: clusterProtocolVersion,
		},
	}

	if err := control.register(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if control.leaseID != 17 || !control.leaseAlive.Load() {
		t.Fatalf("注册后租约状态 = %d, %t", control.leaseID, control.leaseAlive.Load())
	}
	if control.keepAliveChannel != keepAlive {
		t.Fatal("未保存 KeepAlive 响应通道")
	}
}

func TestEtcdControlPlaneRegisterConflictRevokesLease(t *testing.T) {
	client := &scriptedEtcdClient{
		txnResponses: []*clientv3.TxnResponse{{Succeeded: false}},
		grantIDs:     []clientv3.LeaseID{23},
	}
	control := &etcdControlPlane{
		client:          client,
		context:         context.Background(),
		expectedMembers: map[string]string{"im-0": "im-0:12000"},
		memberKey:       "/members/im-0",
		member: clusterMember{
			Name:            "im-0",
			Address:         "im-0:12000",
			ProtocolVersion: clusterProtocolVersion,
		},
	}

	if err := control.register(5 * time.Second); err == nil {
		t.Fatal("节点名称冲突未返回错误")
	}
	if len(client.revoked) != 1 || client.revoked[0] != 23 {
		t.Fatalf("冲突后回收租约 = %v", client.revoked)
	}
}

func TestEtcdControlPlaneClaimTask(t *testing.T) {
	for _, test := range []struct {
		name       string
		succeeded  bool
		wantClaim  bool
		wantRevoke bool
	}{
		{name: "claimed", succeeded: true, wantClaim: true},
		{name: "conflict", succeeded: false, wantRevoke: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &scriptedEtcdClient{
				txnResponses: []*clientv3.TxnResponse{{Succeeded: test.succeeded}},
				grantIDs:     []clientv3.LeaseID{31},
			}
			control := readyScriptedControlPlane(client)

			claimed, err := control.ClaimTask(context.Background(), "scheduled-1", 1500*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			if claimed != test.wantClaim {
				t.Fatalf("领取结果 = %t", claimed)
			}
			if got := len(client.revoked) == 1; got != test.wantRevoke {
				t.Fatalf("是否回收冲突租约 = %t, 租约=%v", got, client.revoked)
			}
		})
	}
}

func TestEtcdControlPlaneWatchStopsWithContext(t *testing.T) {
	client := &scriptedEtcdClient{watchStarted: make(chan string, 1)}
	controlContext, cancel := context.WithCancel(context.Background())
	control := &etcdControlPlane{
		client:       client,
		context:      controlContext,
		memberPrefix: "/members/",
	}
	control.waitGroup.Add(1)
	go control.watchLoop()

	select {
	case key := <-client.watchStarted:
		if key != control.memberPrefix {
			t.Fatalf("Watch key = %q", key)
		}
	case <-time.After(time.Second):
		t.Fatal("Watch 未启动")
	}
	cancel()

	done := make(chan struct{})
	go func() {
		control.waitGroup.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("上下文取消后 Watch 未退出")
	}
}

func readyScriptedControlPlane(client etcdControlPlaneClient) *etcdControlPlane {
	control := &etcdControlPlane{
		client:    client,
		context:   context.Background(),
		clusterID: "test",
		config: clusterControlPlaneConfig{
			Namespace: "/im",
			LeaseTTL:  "10s",
		},
		member: clusterMember{Name: "im-0", InstanceID: 1},
	}
	control.leaseAlive.Store(true)
	control.viewApplied.Store(true)
	control.lastViewRefresh.Store(time.Now().UnixNano())
	view := clusterView{
		Epoch:            1,
		ExpectedReplicas: 3,
		Members: []clusterMember{
			{Name: "im-0", InstanceID: 1, Active: true},
			{Name: "im-1", InstanceID: 2, Active: true},
			{Name: "im-2", InstanceID: 3, Active: true},
		},
	}
	control.view.Store(&view)
	return control
}
