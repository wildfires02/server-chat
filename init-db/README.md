# 初始化或升级 `im` 数据库工具

此工具用于初始化 `im` 数据库（或从早期版本升级现有数据库），并可选地在数据库中填充初始数据。如需强制重置数据库，请使用命令行选项 `--reset=true`。

## 编译程序：

 - **RethinkDB**
  `go build -tags rethinkdb -o im-db .`

 - **MySQL**
  `go build -tags mysql -o im-db .`

 - **MongoDB**
  `go build -tags mongodb -o im-db .`

 - **PostgreSQL**
  `go build -tags postgres -o im-db .`


## 运行

在命令行中直接运行：

`./im-db [参数]`

命令行参数说明：
 - `--reset`：删除原有数据库并重新创建一个空白数据库；如果数据库不存在则无效果。
 - `--upgrade`：从旧版本升级数据库并保留所有数据；升级前请确保已备份数据库。
 - `--no_init`：检查数据库是否存在，若不存在则不创建。
 - `--data=文件名`：使用指定文件的数据填充 `im` 数据库。参考 [data.json](data.json)。
 - `--config=文件名`：从指定文件加载配置。示例配置文件请见 [im.conf](../server/im.conf)。
 - `--make_root=用户ID`：将现有用户提升为 Root 超级管理员，`用户ID` 格式形如 `usrAbCDef123`。
 - `--add_root=用户名[:密码]`：创建新用户账号并将其设为 Root 超级管理员；如果未提供密码，系统将自动生成强密码。

配置文件参数说明：
 - `uid_key`：经过 Base64 编码的 16 字节 XTEA 加密密钥，用于对对象 ID 进行（弱）加密，避免自增 ID 暴露数量。生产环境建议使用自定义密钥。
 - `store_config.adapters.mysql` 与 `store_config.adapters.rethinkdb` 等为数据库适配器专用配置节：
   - `DBName`：要生成的数据库名称。
   - `addresses`：RethinkDB/MongoDB 的连接主机和端口号，支持数组形式 `["host1", "host2"]`。
   - `replica_set`：MongoDB 副本集 (Replica Set) 名称。

`uid_key` 仅在加载示例数据时使用。它应当与生产服务器密钥匹配，并保持私密。

默认的 `data.json` 文件包含初始示例数据（例如用户账号、群组主题和 P2P 会话）。

## 相关架构链接：

* [RethinkDB Schema 架构](../server/db/rethinkdb/schema.md)
* [MySQL Schema 架构](../server/db/mysql/schema.sql)
* [MongoDB Schema 架构](../server/db/mongodb/schema.md)
* [PostgreSQL Schema 架构](../server/db/postgres/schema.sql)
