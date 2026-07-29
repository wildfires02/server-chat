# 生产集群操作手册

> 文档信息
>
> - 类型：生产操作手册
> - 适用环境：`staging`、`production`
> - 前置条件：已完成目标环境部署评审和数据库备份恢复验证

本文适用于 3 节点 IM 集群。所有变更一次只能处理一个 IM 节点，且变更前后都必须确认其余节点仍构成多数派。Kubernetes 示例中的 StatefulSet 和非 Kubernetes 的 systemd 模板都遵循相同流程。

## 1. 发布前检查

1. 数据库迁移必须先以独立 Job 执行，并遵循 expand/migrate/contract；业务节点不得并发执行迁移。
2. 确认三个 etcd endpoint、数据库主地址和对象存储均可从 IM 节点访问。
3. 确认每个 IM 节点使用独立私钥，节点证书具有精确 DNS SAN `im-N` 以及服务端、客户端 EKU。
4. 用 `--validate_config` 校验实际生产配置和环境变量。
5. 确认所有节点 `/readyz` 成功，Cluster View epoch 一致，可靠 Lane 队列低于 80%。
6. 确认新旧二进制支持的集群协议范围存在交集。

Kubernetes 发布前还必须执行：

```bash
kubectl kustomize deployments/kubernetes/base >/tmp/im-kubernetes.yaml
kubectl apply --server-side --dry-run=server -f /tmp/im-kubernetes.yaml
```

服务端 dry-run 必须连接与生产版本一致的 Kubernetes API；离线机器只能完成 Kustomize 渲染和 YAML 结构检查，不能代替该门禁。

## 2. 单节点 Drain 和恢复

Drain 顺序由服务端保证：

1. 节点先退出 Readiness，拒绝新连接和写请求。
2. 当前成员以 `draining` 状态写回 etcd，并推动新的 Cluster View epoch。
3. Draining 节点退出 Owner Ring，数据库 fence 在新 Ring 生效前推进。
4. 可靠 Lane 排空或达到 `health.drain_timeout`。
5. 编排系统再向进程发送 SIGTERM。

手工触发时只能从节点回环地址调用：

```bash
curl --fail --request POST http://127.0.0.1:6060/drainz
```

恢复节点前检查证书、配置和二进制版本。节点重新注册新实例租约、应用当前 View 并通过数据库检查后才会恢复 Ready。

## 3. 滚动升级

Kubernetes：

```bash
kubectl -n im-system rollout status statefulset/im
kubectl -n im-system get pods -l app.kubernetes.io/name=im-server
```

systemd：

```bash
sudo systemctl stop im-server@im-0
sudo install -o root -g root -m 0755 im-server /usr/local/bin/im-server
sudo systemctl start im-server@im-0
curl --fail http://127.0.0.1:6060/readyz
```

只有当前节点重新 Ready，并且消息发布、历史同步和 fencing 拒绝指标正常后，才能处理下一节点。出现以下任一情况立即停止滚动：

- 任意未操作节点退出 Ready。
- 可靠请求最终失败或队列持续超过 80%。
- Cluster View epoch 在节点间不一致。
- 数据库出现旧 Owner 或 fence 回退拒绝。
- 新旧节点无法协商集群协议版本。

## 4. 节点证书轮换

叶子证书和私钥在每次新 TLS 握手时从文件重新加载，可以在不重启进程的情况下替换。必须原子替换成对文件，防止短暂的证书/私钥不匹配：

1. 把新证书和私钥写入同目录临时文件。
2. 校验证书 SAN、EKU、有效期和私钥匹配。
3. 依次原子重命名到 `cert.pem`、`key.pem`。
4. 观察 `/readyz`、TLS 握手失败和 Lane 重连指标。
5. 对三个节点逐一执行，不同时替换全部节点。

CA 信任池只在进程启动时加载，CA 轮换必须使用双信任窗口：

1. 发布同时包含旧 CA 和新 CA 的 Bundle，并逐节点 Drain、重启。
2. 确认所有节点都信任双 CA 后，逐节点换成新 CA 签发的叶子证书。
3. 确认全部连接已经使用新证书，再发布只包含新 CA 的 Bundle并逐节点 Drain、重启。
4. 任一步失败都保留双 CA Bundle，不能直接删除旧 CA。

## 5. etcd 客户端证书轮换

当前 etcd 客户端 TLS 凭据在进程启动时加载，因此证书、私钥或 CA 变更后需要逐节点 Drain、重启。每次重启后必须确认：

- 当前节点重新获得成员租约。
- `/readyz` 成功。
- Cluster View 包含三个 Serving 成员。
- 无 lease keepalive 或 watch 错误。

## 6. 数据库主备切换

数据库应通过稳定 HA endpoint 对外提供服务。切换前停止发布变更并确认消息入口有重试能力：

1. 确认数据库备库回放延迟满足 RPO 要求。
2. 执行数据库平台的受控主备切换。
3. 切换期间数据库主动检查失败会让 IM 节点退出 Ready 并拒绝写入；不能绕过门禁。
4. 新主可写后确认 Schema 版本、全局 cluster fence 和 Topic owner fence 未回退。
5. 使用相同客户端 `cid` 重试切换窗口内未确认的发布，并检查没有重复落库。
6. 三个 IM 节点恢复 Ready 后再恢复全部入口流量。

如果数据库平台改变了 DSN，先在 Secret 中更新新地址，再逐节点 Drain、重启。不能同时重启三个节点。

## 7. 回滚

二进制回滚必须满足数据库 Schema 和集群协议仍与 N-1 兼容：

1. 停止后续发布。
2. 选择最近变更的单个节点执行 Drain。
3. 恢复上一不可变镜像 digest 或已校验二进制。
4. 等待节点重新 Ready，验证跨节点发布、历史同步和 Owner 迁移。
5. 按相同步骤逐节点回滚。

如果已经执行不兼容的 contract Schema 迁移，不得直接回滚二进制；应先执行经过评审的前向修复。任何时候失去多数派都应保持 fail-closed，不能临时切换为 standalone。

## 8. 在线扩缩容

所有节点使用相同的候选白名单 `nodes`，首次活动集合由 `initial_members` 创建。
候选 Peer 和证书身份在进程启动时准备好，但候选节点只有同时满足“已注册、
协议兼容、未 Drain、已被 etcd 拓扑 CAS 提交”后才进入 Owner Ring。

### 8.1 三节点扩容到五节点

1. 确认 `im-3`、`im-4` 地址、专属 mTLS 证书和 Secret 已就绪。
2. 启动两个候选进程；确认 `/livez=200`、`/readyz=503`。
3. 从任一 Ready 节点的回环地址 POST `/clusterz`：

   ```json
   {"members":["im-0","im-1","im-2","im-3","im-4"]}
   ```

4. 确认五个节点的 `cluster_epoch` 和 `ring_signature` 收敛且全部 Ready。
5. 观察 Owner 迁移、Lane 队列、fencing 拒绝和发布/投递 p99。

### 8.2 五节点缩容到三节点

1. 分别在 `im-3`、`im-4` 回环地址 POST `/drainz`，等待两节点 Not Ready。
2. 确认可靠 Lane 排空、三个保留节点仍满足五节点多数派。
3. 从保留节点 POST `/clusterz`：

   ```json
   {"members":["im-0","im-1","im-2"]}
   ```

4. 确认三个保留节点收敛到同一新 epoch 后，再停止两个已移除进程。

控制面每次只接受增加或移除两个节点的相邻奇数规模变更。扩容要求完整旧集合
仍在线；缩容要求待移除节点仍注册且已经 Drain。同规模替换必须先扩容再缩容，
从而在切换过程中保留旧拓扑多数派。不能直接修改 `expected_replicas`、降低
多数派阈值，或在不同节点使用不同候选白名单。

## 9. 72 小时发布认证

单次本地故障矩阵通过后，在真实 staging 三个节点上执行：

```bash
export IM_TEST_CLUSTER_NODE0_HTTP=https://im-0.staging.example.com
export IM_TEST_CLUSTER_NODE1_HTTP=https://im-1.staging.example.com
export IM_TEST_CLUSTER_NODE2_HTTP=https://im-2.staging.example.com
export IM_TEST_CLUSTER_API_KEY='由 staging Secret 注入'
export IM_TEST_CLUSTER_USERNAME='cluster-certifier'
export IM_TEST_CLUSTER_PASSWORD='由 staging Secret 注入'
export IM_CLUSTER_RELEASE_ID='ghcr.io/example/im-server@sha256:...'
./scripts/certify-cluster-72h.sh
```

运行期间按发布演练窗口执行一次数据库主备切换、一次节点滚动升级和一次
3→5→3；同时保留监控、告警、数据库和 etcd 事件。脚本产生 `passed` 报告只
证明消息黑盒断言与 p99 达标，仍需把基础设施事件和备份恢复证据附到发布评审。
脚本强制时长不少于 72 小时，并要求 `IM_CLUSTER_RELEASE_ID` 使用完整的
64 位 SHA-256，防止缩短任务或使用可变镜像标签绕过发布门禁。
