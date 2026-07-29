# Go 分布式压力测试

`im-load` 是仓库内置的即时通信压力测试工具，支持本机运行，也支持一台控制器协调多台执行节点。业务流量直接使用 WebSocket 连接目标服务；控制器只负责分配连接数、账号、统一开始时间以及汇总指标。

旧的 Gatling、Tsung 和 Erlang 脚本已经移除，压测代码、协议客户端和分布式控制面现在都使用 Go 实现。

## 1. 能力范围

### 压测场景

| 场景 | 参数 | 行为 |
| --- | --- | --- |
| `me` | `--scenario=me` | 登录、订阅个人主题 `me`，并保持连接到测试结束 |
| `mixed` | `--scenario=mixed` | 登录、订阅 `me`、读取已有订阅、随机遍历主题、发布消息并离开主题 |
| `hot-topic` | `--scenario=hot-topic --topic=<主题>` | 所有连接订阅同一个主题，升压完成后集中发布消息并保持在线 |

`mixed` 场景不会向频道主题发布消息。`--max-topics=0` 表示不限制每个连接遍历的主题数。

### 采集指标

- 连接尝试数、成功数和当前活跃数。
- 登录、订阅、发布确认和消息投递数量。
- 按阶段分类的错误计数。
- 发布确认延迟与消息投递延迟的直方图及 P50、P95、P99。
- 每个执行节点的累计报告和控制器的全局汇总报告。

运行日志写入标准错误，最终 JSON 报告写入标准输出。使用 `--output` 可以同时保存报告文件。

## 2. 构建

在仓库根目录执行：

```bash
mkdir -p ./bin
go build -o ./bin/im-load ./tests/load/cmd/im-load
```

查看完整参数：

```bash
./bin/im-load --help
```

工具复用仓库已有的 Go 依赖，不需要安装 Java、Scala、Erlang、Gatling 或 Tsung。

## 3. 账号文件

账号文件必须是带表头的 CSV：

```csv
username,password,token
alice,alice123,
bob,,已缓存的登录令牌
```

- 必须包含 `username` 和 `password` 列，`token` 列可选。
- 每一行必须提供用户名，并至少提供密码或令牌。
- 连接数多于账号数时，账号会循环复用。
- 多机模式下，控制器会把账号稳定分片后随任务发送给各执行节点。
- 热点主题场景使用的账号必须已经拥有目标主题的访问权限。

仓库中的 [`users.csv`](users.csv) 只是格式样例，不应直接用于正式容量测试。

## 4. 本机压测

以下命令均在仓库根目录执行。

### 长连接场景

```bash
./bin/im-load \
  --mode=local \
  --run-id=me-20260729 \
  --scenario=me \
  --ws-url=ws://127.0.0.1:6060/v0/channels \
  --api-key='请替换为测试环境接口密钥' \
  --accounts=tests/load/users.csv \
  --sessions=10000 \
  --ramp=5m \
  --duration=20m \
  --output=artifacts/me-20260729.json
```

### 综合场景

```bash
./bin/im-load \
  --mode=local \
  --run-id=mixed-20260729 \
  --scenario=mixed \
  --ws-url=ws://127.0.0.1:6060/v0/channels \
  --api-key='请替换为测试环境接口密钥' \
  --accounts=tests/load/users.csv \
  --sessions=1000 \
  --ramp=2m \
  --duration=10m \
  --max-topics=10 \
  --publish-count=2 \
  --publish-interval=3s
```

### 热点主题场景

```bash
./bin/im-load \
  --mode=local \
  --run-id=hot-topic-20260729 \
  --scenario=hot-topic \
  --topic=grpYOrcDwORhPg \
  --ws-url=ws://127.0.0.1:6060/v0/channels \
  --api-key='请替换为测试环境接口密钥' \
  --accounts=tests/load/users.csv \
  --sessions=1500 \
  --ramp=3m \
  --duration=15m \
  --publish-count=2 \
  --publish-interval=30s
```

## 5. 多机分布式压测

多机模式由一个控制器和若干执行节点组成：

1. 控制器读取全局配置和账号文件，等待指定数量的执行节点注册。
2. 全部节点到齐后，控制器按节点分配全局连接数和账号，并下发同一个开始时间。
3. 执行节点直接连接目标 WebSocket 服务，定期向控制器报告累计指标。
4. 所有节点完成后，控制器输出全局汇总报告。

`--sessions` 在控制器上表示全局连接总数，不是每台执行节点的连接数。例如 3 台执行节点和 `--sessions=30000` 会尽可能均匀地分成每台约 10000 个连接。

### 启动控制器

在控制器主机执行：

```bash
./bin/im-load \
  --mode=controller \
  --run-id=distributed-20260729 \
  --control-listen=:19090 \
  --control-token='请替换为随机生成的共享令牌' \
  --workers=3 \
  --start-delay=20s \
  --scenario=hot-topic \
  --topic=grpYOrcDwORhPg \
  --ws-url=wss://load.example.internal/v0/channels \
  --api-key='请替换为测试环境接口密钥' \
  --accounts=tests/load/users.csv \
  --sessions=30000 \
  --ramp=5m \
  --duration=20m \
  --publish-count=2 \
  --publish-interval=30s \
  --output=artifacts/distributed-20260729.json
```

### 启动执行节点

分别在三台执行节点上运行，确保 `--worker-id` 唯一：

```bash
./bin/im-load \
  --mode=worker \
  --controller=http://10.0.0.10:19090 \
  --control-token='请替换为与控制器相同的共享令牌' \
  --worker-id=load-worker-01
```

```bash
./bin/im-load \
  --mode=worker \
  --controller=http://10.0.0.10:19090 \
  --control-token='请替换为与控制器相同的共享令牌' \
  --worker-id=load-worker-02
```

```bash
./bin/im-load \
  --mode=worker \
  --controller=http://10.0.0.10:19090 \
  --control-token='请替换为与控制器相同的共享令牌' \
  --worker-id=load-worker-03
```

执行节点只需要控制面参数；WebSocket 地址、场景、连接数和账号由控制器下发。

### 查看控制器状态

```bash
curl \
  -H 'Authorization: Bearer 请替换为共享令牌' \
  http://10.0.0.10:19090/v1/status
```

## 6. 关键参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--mode` | `local` | `local`、`controller` 或 `worker` |
| `--sessions` | `100` | 本机连接数，或控制器分配的全局连接总数 |
| `--ramp` | `10s` | 平滑建立全部连接的时间 |
| `--duration` | `1m` | 升压完成后的运行时间 |
| `--request-timeout` | `15s` | WebSocket 协议请求超时 |
| `--publish-count` | `1` | 每个连接在每个目标主题中的发布次数 |
| `--publish-interval` | `1s` | 相邻发布之间的最大随机间隔 |
| `--max-topics` | `10` | 综合场景每个连接最多访问的已有主题数 |
| `--workers` | `1` | 控制器等待的执行节点数量 |
| `--start-delay` | `10s` | 全部节点注册完成后预留的统一启动时间 |
| `--report-interval` | `2s` | 执行节点向控制器报告指标的间隔 |
| `--summary-interval` | `5s` | 本机或控制器打印运行摘要的间隔 |

时间参数使用 Go 时长格式，例如 `500ms`、`30s`、`5m` 或 `2h`。

## 7. 安全与运行要求

- 只对明确授权的测试环境执行压力测试，不要直接压测生产环境。
- 控制面会传输接口密钥、账号密码或令牌。跨主机运行时必须设置高强度 `--control-token`，并使用受控的内网；跨越不可信网络时，应通过反向代理为控制面启用 HTTPS。
- 不要把真实密钥、令牌、账号文件或压测报告提交到仓库。
- 所有主机应启用时间同步。跨节点消息投递延迟使用发送端时间戳计算，时钟偏差会直接污染结果。
- 控制器的 `--start-delay` 应覆盖最慢节点获取任务所需的时间。
- 压测机通常需要提高文件描述符上限，并确认本机端口范围、内存、带宽和网卡队列足以承载目标连接数。
- 压测机与服务端监控应同步采集 CPU、内存、网络、垃圾回收、数据库连接池、慢查询和队列长度。

## 8. 结果解释

工具输出的是客户端观察结果，不等同于服务端容量结论。正式容量报告至少应保存：

- 完整命令、代码版本、配置文件摘要和运行时间。
- 压测机、服务端、数据库与网络拓扑。
- 最终 JSON 报告及运行期间的客户端错误日志。
- 服务端和数据库监控。
- 稳态区间内的吞吐、错误率、P50/P95/P99，以及达到瓶颈时首先饱和的资源。

只有在目标硬件、目标数据库和目标配置上重复验证后，结果才可以作为发布容量基线。
