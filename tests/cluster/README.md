# 集群故障与容量测试入口

执行离线核心套件：

```bash
./scripts/test-cluster.sh
```

它覆盖：

- gRPC 多 Lane 顺序、断流重建、同 Request ID 重试和显式背压。
- 有界去重、碰撞保护、并发合并和缓存满时 fail-closed。
- mTLS 节点身份、错误 SAN、叶子证书轮换和协议范围。
- Readiness、Drain、写门禁、队列排空和节点级 Fanout。
- 3/5 节点 Ring 分布、扩容最小迁移和缩容受影响 Owner 迁移。
- 核心集群测试连续 3 次 `-race`。
- Kubernetes 清单离线 Kustomize 校验。
- 真实进程黑盒测试包的编译和跳过门禁。

## 真实组件

以下变量启用有破坏性的隔离测试环境测试，绝不能指向生产数据库：

```bash
export IM_TEST_ETCD_ENDPOINTS=https://etcd-0:2379,https://etcd-1:2379,https://etcd-2:2379
export IM_TEST_POSTGRES_FENCE_DSN='postgresql://.../im_fence_test'
export IM_TEST_MYSQL_FENCE_DSN='im_test:...@tcp(...)/im_fence_test?parseTime=true'
./scripts/test-cluster.sh
```

etcd 测试覆盖三成员注册、成员专用 epoch、无关写隔离、定时任务领取、Drain 收敛和失去多数派。PostgreSQL/MySQL 测试会重建指定数据库，验证旧 Owner 拒绝、新 Owner 接管和 fence 防回退。

设置 `IM_CLUSTER_BENCHMARK=1` 可追加 Owner 路由微基准。

## 真实五节点进程认证

发布流水线执行：

```bash
IM_CLUSTER_PROCESS_E2E=1 ./scripts/test-cluster.sh
```

也可以直接运行 `./scripts/test-cluster-process.sh`。脚本只操作随机命名的
隔离容器和 `mktemp` 目录，自动生成一次性 mTLS CA/节点证书，并覆盖：

- 三节点 etcd mTLS、三节点冷启动和多数派 Readiness。
- 三节点跨边缘发布、远端 Owner 持久化和跨节点实时投递。
- Owner `SIGKILL`，15 秒 RTO、已 ACK 历史恢复和 seq 单调。
- Joining 节点、etcd CAS 3→5、五节点热点容量、Drain 5→3。
- etcd 多数派丢失、单节点隔离超过 Lease TTL、租约自动重新注册。
- 数据库完全失联/恢复和三节点逐个滚动重启。
- 256 路重连突发以及 ACK/投递 p99 发布门禁。

每次成功会在 `test-results/` 生成带 Git commit、工作区状态、被测二进制
SHA-256、RTO 和原始容量输出的报告。

仍不能由本地脚本替代的生产签字项是：真实 Kubernetes 的
server-side dry-run、数据库平台主备切换、备份恢复、安全扫描，以及在目标
硬件上实际跑满 72 小时。

## 72 小时 staging 门禁

先把三个变量设置为绕过负载均衡、分别指向三个真实 staging 节点的 HTTPS
入口，再执行：

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

脚本默认以 10 QPS 持续 72 小时跨节点发布和在线投递，每分钟原子更新
`test-results/cluster-soak-latest.json`，检查：

- 每条发布 ACK 和订阅者在线投递均成功。
- ACK seq、投递 seq 连续且一致。
- 每 1000 条相同 `cid` 重试返回 208 且不推进 seq。
- 每个窗口 ACK p99≤300ms、投递 p99≤500ms。

生产脚本拒绝少于 72 小时的时长，并要求 `IM_CLUSTER_RELEASE_ID` 是完整的
64 位 SHA-256；任务中断或任一断言失败时写出 `status=failed`，只有自然跑满
才会写出 `status=passed`，流水线必须据此拒绝发布。
