# YAML 配置说明

项目自有配置统一使用 YAML：

- `im.yaml`：功能完整的开发单机配置，不包含集群参数。
- `im.standalone.yaml`：不包含任何集群节点地址的独立开发单机配置。
- `im.cluster-dev.yaml`：本机三节点开发集群配置，依赖本机 etcd 与 MySQL。
- `im.cluster.yaml`：三节点生产集群模板，只保存非敏感拓扑和安全默认值。
- `init-db.yaml`：数据库初始化工具的最小配置。
- `ice-servers.example.yaml`：使用 `ice_servers` 对象根节点的独立 WebRTC ICE 服务器示例。
- `server/db/*/tests/test.yaml`：各数据库集成测试配置。
- `deployments/docker/compose/im.cluster.yaml`：Docker Compose 开发集群配置。

## 环境变量覆盖

服务端和初始化工具支持用环境变量覆盖 YAML 中已有的标量值：

- 变量统一使用 `IM_` 前缀。
- YAML 层级使用双下划线 `__` 分隔。
- 环境变量名必须使用大写。
- 字符串、整数、浮点数和布尔值会按 YAML 原值类型转换。

例如：

```bash
export IM_LISTEN=:8080
export IM_STORE_CONFIG__USE_ADAPTER=postgres
export IM_STORE_CONFIG__ADAPTERS__POSTGRES__PASSWD='strong-password'
export IM_AUTH_CONFIG__TOKEN__KEY='base64-secret'

./bin/im-server --config=./configs/im.yaml
```

数组和对象仍应写在 YAML 文件中；环境变量覆盖主要用于密码、密钥、地址、端口、开关和超时等标量。

容器不再使用 `envsubst` 生成第二份配置；镜像直接读取这些 YAML，并通过同一套
`IM_` 环境变量规则覆盖。数据库初始化或升级由显式的一次性任务执行，业务容器
启动时不会自动修改 Schema。

## 部署模式

服务端配置必须显式提供：

```yaml
runtime:
  environment: development
  deployment_mode: standalone
```

- `environment` 支持 `development`、`test`、`staging` 和 `production`。
- `deployment_mode` 支持 `standalone` 和 `cluster`。
- `staging`、`production` 只能使用集群模式。
- 生产集群必须配置 Cluster ID、至少 3 个奇数节点、广播地址和 etcd 控制面。
- etcd 控制面使用 Lease 注册节点，并以 members 键的修改 Revision 和持久化成员变更 marker 形成专用 Cluster View epoch；无关 etcd 写入不会推动 IM 任期。
- 启用控制面时，PostgreSQL/MySQL 可直接使用数据库 fencing；MongoDB 必须配置 Replica Set；RethinkDB 会被启动门禁拒绝。
- 可以使用 `im-server --validate_config --config=...` 仅执行配置门禁检查。

## 格式约束

配置加载器只接受 `.yaml` 和 `.yml` 文件，且所有文件的根节点必须是对象，以便统一由 Viper 解析。`.conf`、`.json` 和 `.jsonc` 不再作为配置格式支持。
