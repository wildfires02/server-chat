# 数据库初始化工具

`init-db` 用于创建、升级或检查服务端数据库，也可以导入开发示例数据和管理
超级管理员账号。

## 1. 构建

构建标签必须与目标数据库一致：

```bash
# 四选一
go build -tags mysql -o bin/init-db ./cmd/init-db
go build -tags postgres -o bin/init-db ./cmd/init-db
go build -tags mongodb -o bin/init-db ./cmd/init-db
go build -tags rethinkdb -o bin/init-db ./cmd/init-db
```

## 2. 常用命令

首次初始化：

```bash
./bin/init-db \
  --config=./configs/im.standalone.yaml \
  --data=./cmd/init-db/data.json
```

升级已有数据库：

```bash
./bin/init-db \
  --config=./configs/im.standalone.yaml \
  --upgrade=true
```

重建开发数据库：

```bash
./bin/init-db \
  --config=./configs/im.standalone.yaml \
  --data=./cmd/init-db/data.json \
  --reset=true
```

`--reset=true` 会删除并重建目标数据库，禁止对生产库使用。升级和迁移前必须
完成可恢复备份。

## 3. 参数

| 参数 | 说明 |
| --- | --- |
| `--config` | YAML 配置文件，默认 `configs/init-db.yaml` |
| `--data` | 要导入的示例数据文件；留空或使用 `-` 表示不导入 |
| `--reset` | 删除并重建数据库 |
| `--upgrade` | 保留数据并升级数据库结构 |
| `--no_init` | 只检查数据库是否存在，不自动创建 |
| `--add_root=用户名[:密码]` | 创建基础认证用户并授予超级管理员权限 |
| `--make_root=用户标识` | 将已有基础认证用户提升为超级管理员 |

`--reset`、`--upgrade` 和普通初始化代表不同操作模式，不应在同一次执行中混用。

## 4. 配置

工具读取 `store_config` 和 `p2p_delete_enabled`。本地 MySQL 可直接使用
[`configs/im.standalone.yaml`](../../configs/im.standalone.yaml)。
[`configs/init-db.yaml`](../../configs/init-db.yaml) 是跨数据库最小模板，使用前
必须设置 `store_config.use_adapter` 和对应连接参数。密码和地址应通过 `IM_`
环境变量注入，覆盖规则见[配置说明](../../configs/README.md)。

示例数据文件 [`data.json`](data.json) 包含开发账号、群组和点对点会话，只适合
本地开发或隔离测试环境。

## 5. 数据库结构

- [MySQL](../../server/db/mysql/schema.sql)
- [PostgreSQL](../../server/db/postgres/schema.sql)
- [MongoDB](../../server/db/mongodb/schema.md)
- [RethinkDB](../../server/db/rethinkdb/schema.md)

完整安装流程见[安装与构建](../../INSTALL.md)。
