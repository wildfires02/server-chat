# 安装 IM

配置文件 [`im.conf`](./server/im.conf) 中包含了配置服务器的详细说明。

## 通过预编译二进制文件安装

1. 访问 Releases 页面，选择最新的或最适合的发布版本。从二进制文件列表中下载对应你所使用的数据库（支持：MySQL、PostgreSQL、MongoDB、RethinkDB）和操作系统平台（Linux ARM 或 Intel、Windows、Mac ARM 或 Intel）的文件。下载完成后解压到你选择的目录，并 `cd` 进入该目录。

2. 确保你的数据库正在运行，并且已配置为允许来自 `localhost` 的连接。在 MySQL 的情况下，IM 默认尝试连接 `localhost:3306`（用户名 `root`，密码在配置文件中指定）。在 PostgreSQL 的情况下，IM 将尝试连接 `postgres` 用户。有关如何配置 IM 使用其他用户名或密码的说明，请参考下文（_从源码构建_ 第 5 节）。
   - 需要 **MySQL 5.7 或更高版本**（推荐 **8.0+**，必须使用 `InnoDB` 存储引擎及 `utf8mb4` 字符集，不要使用 `MyISAM`）。
   - 需要 **PostgreSQL 12 或更高版本**（推荐 **13+**）。
   - 需要 **MongoDB 4.4 或更高版本**（推荐 **8.x**，需作为单节点副本集 Single Node Replica Set 运行）。

3. 运行数据库初始化工具 `im-db`（或 `init-db`，Windows 上为 `.exe`）：
	```bash
	./im-db -config=./server/im.conf -data=./init-db/data.json
	```

4. 运行 `im-server`（或 `im`，Windows 上为 `.exe`）服务器。指定配置文件路径直接运行：
	```bash
	./im-server -config=./server/im.conf
	```

5. 在浏览器中访问 http://localhost:6060/ 以测试你的安装。


## Docker 安装

请参阅 [Docker 说明文档](./docker/README.md) 或 [Docker Compose 说明文档](./docker/docker-compose/README.md)。


## 从源码构建安装

1. 安装 [Go 开发环境](https://golang.org/doc/install)。以下安装说明适用于 **Go 1.22 及更新版本**（建议 Go 1.26+）。建议使用最新的 Go 环境进行构建。

2. **可选步骤**（仅在你打算修改 protobuf 或 gRPC 定义时需要）：安装 [protobuf](https://developers.google.com/protocol-buffers/) 和 [gRPC](https://grpc.io/docs/languages/go/quickstart/) 以及适用于 Go 的代码生成器。

3. 确保以下数据库之一已安装并正在运行：
 - **MySQL 5.7 或更高版本**，配置为 `InnoDB` 引擎（推荐 8.0+，`utf8mb4_0900_ai_ci` 排序规则）。
 - **PostgreSQL 12 或更高版本**（推荐 13+）。
 - **MongoDB 4.4 或更高版本**（推荐 8.x）。
 - **RethinkDB**（已弃用）。

4. 编译 IM 服务器 (`im-server`) 和数据库初始化工具 (`im-db`)：
  - **MySQL**:
	```bash
	go build -tags mysql -o im-server ./server
	go build -tags mysql -o im-db ./init-db
	```
  - **PostgreSQL**:
	```bash
	go build -tags postgres -o im-server ./server
	go build -tags postgres -o im-db ./init-db
	```
  - **MongoDB**:
	```bash
	go build -tags mongodb -o im-server ./server
	go build -tags mongodb -o im-db ./init-db
	```
  - **RethinkDB**:
	```bash
	go build -tags rethinkdb -o im-server ./server
	go build -tags rethinkdb -o im-db ./init-db
	```
  - **包含所有数据库**（打包上述所有数据库适配器）：
	```bash
	go build -tags "mysql rethinkdb mongodb postgres" -o im-server ./server
	go build -tags "mysql rethinkdb mongodb postgres" -o im-db ./init-db
	```

  如果你希望使用 `go install` 安装到 `$GOPATH/bin/` 中：
	```bash
	go install -tags mysql ./server
	go install -tags mysql ./init-db
	```

  请注意构建选项中必须包含必要的 **`-tags mysql`**、**`-tags postgres`**、**`-tags mongodb`** 或 **`-tags rethinkdb`**。

  你也可以选择为服务器定义 `main.buildstamp`，例如带有时间戳的构建选项：
	```bash
	go build -tags mysql -ldflags "-X main.buildstamp=`date -u '+%Y%m%dT%H:%M:%SZ'`" -o im-server ./server
	```
  `buildstamp` 的值将被服务器发送给客户端。

5. 打开 `im.conf`（位于 `./server/im.conf`）。检查 `store_config` 节中的数据库连接参数是否适合你的数据库。例如 MySQL 配置：
```json
	"store_config": {
		"use_adapter": "mysql",
		"adapters": {
			"mysql": {
				"User": "root",
				"Passwd": "你的数据库密码",
				"Net": "tcp",
				"Addr": "localhost",
				"Port": "3306",
				"DBName": "im",
				"Collation": "utf8mb4_0900_ai_ci",
				"ParseTime": true
			}
		}
	}
```

6. 确保在 `im.conf` 中 `store_config.use_adapter` 指定了正确的适配器名称（如 `"mysql"`、`"postgres"`、`"mongodb"` 或 `"rethinkdb"`）。

7. 编译好二进制文件后，按照 _运行单机服务器_ 章节的说明进行操作。


## 运行单机服务器

1. 确保你的数据库正在运行：
 - **MySQL**:
	```bash
	mysql.server start
	```
 - **PostgreSQL**:
	```bash
	pg_ctl start
	```
 - **MongoDB**:
    MongoDB 应当作为单节点副本集（Single Node Replica Set）运行：
	```bash
	mongod
	```
 - **RethinkDB**:
	```bash
	rethinkdb --bind all --daemon
	```

2. 运行数据库初始化工具（建表与导入基础数据）：
	```bash
	./im-db -config=./server/im.conf -data=./init-db/data.json
	```
	若需要重置表结构并重建数据库，可传入 `-reset=true` 参数：
	```bash
	./im-db -config=./server/im.conf -data=./init-db/data.json -reset=true
	```
	数据库初始化工具每次安装只需要运行一次。更多选项请参阅 [说明文档](init-db/README.md)。

3. 确保静态资源目录存在（避免因缺少 static 目录导致服务无法启动）：
	```bash
	mkdir -p server/static
	```
	如需指定外部前端静态资源，可使用 `-static_data` 参数。

4. 运行服务器：
	```bash
	./im-server -config=./server/im.conf
	```

5. 在浏览器中访问 [http://localhost:6060/](http://localhost:6060/) 来测试你的安装。静态文件将在 Web 根路径 `/` 处提供服务。你可以通过修改配置文件中的 `static_mount` 行来更改此设置。

**重要提示！** 如果你在 Apache 或 Nginx 等其他 Web 服务器旁边运行 IM，请切记你需要从 IM 服务的 URL 来启动 Web 应用，否则它将无法工作。


## 运行集群模式

- 按照上一节所述，安装并运行数据库、运行数据库初始化工具并确认模板和配置。MySQL 和 RethinkDB 都支持集群模式。为了增加容错能力，你可以考虑使用集群模式。

- 集群模式要求至少有两个节点，建议至少三个节点。

- 以下部分为集群的配置示例：

```json
	"cluster_config": {
		// 当前节点的名称
		"self": "",
		// 所有集群节点的列表（包含当前节点）
		"nodes": [
			{"name": "one", "addr":"localhost:12001"},
			{"name": "two", "addr":"localhost:12002"},
			{"name": "three", "addr":"localhost:12003"}
		],
		// 故障转移功能配置，请勿随意更改
		"failover": {
			"enabled": true,
			"heartbeat": 100,
			"vote_after": 8,
			"node_fail_after": 16
		}
	}
```
* `self` 是当前节点的名称。通常更方便的做法是在命令行中使用 `cluster_self` 选项指定当前节点名称。命令行参数的值会覆盖配置文件中的值。如果配置文件和命令行中均未提供该值，则禁用集群功能。
* `nodes` 定义了各个集群节点。示例中定义了运行在本地主机指定通信端口上的三个节点 `one`、`two` 和 `three`。集群通信地址不需要暴露给外界。
* `failover` 是一个故障转移配置，它可以将主题（Topics）从故障集群节点迁移出来，保持其可访问性：
  * `enabled` 开启故障转移模式；故障转移模式要求集群中至少有三个节点。
  * `heartbeat` 主节点向从节点发送心跳包的时间间隔（毫秒），用于确保从节点可达。
  * `vote_after` 重新选举新主节点之前允许失败的心跳包数量。
  * `node_fail_after` 认为某个从节点离线前允许其丢失的心跳包数量。

如果你在同一台主机上测试集群，还必须重写 `listen` 和 `grpc_listen` 端口。以下是在同一台主机上使用相同配置文件启动两个集群节点的示例：
```bash
./im-server -config=./server/im.conf -listen=:6060 -grpc_listen=:16060 -cluster_self=one &
./im-server -config=./server/im.conf -listen=:6061 -grpc_listen=:16061 -cluster_self=two &
```
你可以参考使用 [run-cluster.sh](./server/run-cluster.sh) 脚本。

### 启用推送通知

请遵循 [配置指南](./docs/faq.md#问开启离线-push-消息推送有哪些方案)。


### 启用视频通话

视频通话使用 [WebRTC](https://en.wikipedia.org/wiki/WebRTC) 技术。WebRTC 是一种点对点（P2P）协议：一旦通话建立，客户端应用程序之间会直接传输数据。直接数据传输效率高，但当双方无法直接通过公网互通时会产生问题。WebRTC 通过实现 [TURN(S)](https://en.wikipedia.org/wiki/Traversal_Using_Relays_around_NAT) 和 [STUN](https://en.wikipedia.org/wiki/STUN) 协议的 [ICE](https://en.wikipedia.org/wiki/Interactive_Connectivity_Establishment) 服务器作为备用方案来解决该问题。

IM 开箱即用情况下不提供 ICE 服务器。你必须自行安装配置（或购买）你自己的服务器，否则视频和语音通话功能将不可用。

一旦你从服务提供商处获取了 ICE TURN/STUN 配置，将其添加到 `im.conf` 的 `"webrtc"` -> `"ice_servers"`（或 `"ice_servers_file"`）节中。同时将 `"webrtc"` -> `"enabled"` 修改为 `true`。`im.conf` 中提供了一个示例配置仅供参考，**它无法工作**，因为它使用的是虚拟地址而非真实的服务器地址。


### 关于后台运行服务器的注意事项

在 Go 语言内部并没有完美优雅的方式将进程守护化（daemonize）。必须使用外部工具（例如 shell `&` 运算符、`systemd`、`launchd`、`SMF`、`daemon tools`、`runit` 等）在后台运行该进程。

针对 [nohup](https://en.wikipedia.org/wiki/Nohup) 用户的特别说明：在使用 `nohup` 调用后必须立即执行 `exit` 以干净地关闭前台会话：

```bash
nohup ./im-server -config=./server/im.conf &
exit
```

否则，如果在 SSH 会话终止前 Shell 连接中断（表现为 `Connection to XXX.XXX.XXX.XXX port 22: Broken pipe`），服务器可能会接收到 `SIGHUP` 信号。在这种情况下服务器会关闭，因为服务器拦截了 `SIGHUP` 信号并将其解释为关机请求。
