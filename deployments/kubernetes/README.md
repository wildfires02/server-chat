# Kubernetes 三/五节点生产部署

`base/` 默认启动 3 副本 StatefulSet，并预声明 `im-0`～`im-4` 五个候选节点，
支持经过 etcd CAS 的 3→5→3 在线拓扑变更。目录同时提供 Headless Service、
客户端 Service、PDB、双向 NetworkPolicy、健康探针和同步 Drain；不会部署
数据库、etcd 或对象存储。

## 必备外部依赖

- 跨故障域的 PostgreSQL 高可用集群。
- 独立的 3 或 5 节点 etcd，必须启用双向 TLS。
- 共享 S3 兼容对象存储。
- Secrets Store CSI Driver 及企业 Secret Provider。
- 能固定镜像 digest 的镜像仓库。

## 必须准备的 Secret

1. 创建 `im-runtime-secrets`，键名参考 `secret.example.yaml`。不要把真实值写入 Git。
2. 创建 `im-etcd-client-tls` SecretProviderClass，向 `/run/secrets/etcd` 注入 `ca.pem`、`client.pem`、`client-key.pem`。
3. 创建 `im-cluster-node-tls` SecretProviderClass，按 Pod 身份向 `/run/secrets/cluster` 注入 `ca.pem`、`cert.pem`、`key.pem`。

集群节点证书必须由同一专用 CA 签发，并分别包含精确 DNS SAN
`im-0`～`im-4`。不能给 Pod 注入同一私钥，也不能用通配符 SAN；服务端会在
TLS 握手后再次把证书 SAN 与帧中的节点名比对。

如果当前 Secret Provider 不能按 StatefulSet Pod 身份签发不同证书，必须先通过 Vault Agent、SPIRE 文件投影或等价的准入注入方案解决，不能退回共享节点私钥。

## 发布前修改

- 在 overlay 中把 StatefulSet 镜像替换为真实不可变 digest。
- 修改 ConfigMap 中的 etcd 地址、逻辑 `cluster_id`、对象存储参数和容量限制。
- 根据实际命名空间标签收窄 NetworkPolicy 的数据库、etcd 和 HTTPS 出站范围。
- 确保数据库 Schema 已由独立迁移 Job 升级；不要让三个业务 Pod 并发执行迁移。
- 为外部网关配置 WebSocket、Long Polling 和 gRPC 的超时与连接保持。

## 校验与部署

```bash
./scripts/validate-cluster-manifests.sh
kubectl kustomize deployments/kubernetes/base > /tmp/im-kubernetes.yaml
kubectl apply --server-side --dry-run=server -f /tmp/im-kubernetes.yaml
kubectl apply --server-side -k deployments/kubernetes/base
kubectl -n im-system rollout status statefulset/im
```

验证：

```bash
kubectl -n im-system get pods,pdb
kubectl -n im-system get --raw /api/v1/namespaces/im-system/services/http:im-client:6060/proxy/readyz
kubectl -n im-system exec im-0 -- wget -qO- --post-data='' http://127.0.0.1:6060/drainz
```

Drain 会先把 etcd 成员标记为 `draining`，推动新的 Cluster View 和数据库 fence，迁移 Topic Owner，再等待可靠 Lane 排空。StatefulSet 的 60 秒终止宽限必须大于 `health.drain_timeout`。

## 在线扩容到五节点

配置中的 `nodes` 是五个候选身份白名单，`initial_members` 是首次活动的三节点。
不能只修改 `expected_replicas`；运行时多数派取自 etcd 已提交拓扑。

```bash
# 新 Pod 先注册为 Joining，因此此时 im-3/im-4 的 /readyz 返回 503。
kubectl -n im-system scale statefulset/im --replicas=5
kubectl -n im-system wait --for=jsonpath='{.status.phase}'=Running pod/im-3 pod/im-4 --timeout=120s

# 只能从 Pod 回环地址提交完整目标集合；接口使用 etcd ModRevision CAS。
kubectl -n im-system exec im-0 -- wget -qO- \
  --header='Content-Type: application/json' \
  --post-data='{"members":["im-0","im-1","im-2","im-3","im-4"]}' \
  http://127.0.0.1:6060/clusterz

kubectl -n im-system rollout status statefulset/im
```

## 在线缩容到三节点

```bash
kubectl -n im-system exec im-3 -- wget -qO- --post-data='' http://127.0.0.1:6060/drainz
kubectl -n im-system exec im-4 -- wget -qO- --post-data='' http://127.0.0.1:6060/drainz

kubectl -n im-system exec im-0 -- wget -qO- \
  --header='Content-Type: application/json' \
  --post-data='{"members":["im-0","im-1","im-2"]}' \
  http://127.0.0.1:6060/clusterz

kubectl -n im-system scale statefulset/im --replicas=3
kubectl -n im-system rollout status statefulset/im
```

扩容要求两个新节点均已注册、协议兼容且未 Drain；缩容要求两个待移除节点
先 Drain。每次只允许相邻奇数规模变更，同规模替换必须先 3→5，再 5→3。

证书轮换、滚动升级、数据库切换和回滚步骤见
[`docs/cluster-operations.md`](../../docs/cluster-operations.md)。
