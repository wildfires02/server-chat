# IM 聊天服务端本地启动与部署指南

本文档提供 IM 聊天服务端的本地开发环境准备、数据库初始化、编译启动及常见问题排查指南。

---

## 1. 环境准备

在本地运行服务前，请确认具备以下环境：

- **Go 开发环境**：Go 1.22 及以上版本（推荐 Go 1.26+）
- **数据库**：MySQL 8.0+（默认配置）、PostgreSQL 12+、MongoDB 或 RethinkDB
  > **小贴士（通过 Docker 快速启动 MySQL）**：
  >
  > ```bash
  > docker run -d --name mysql -p 3306:3306 -e MYSQL_ROOT_PASSWORD=123456 mysql:8.0
  > ```

---

## 2. 配置文件检查

打开项目根目录下的配置文件 [`server/im.conf`](./server/im.conf)，确认 `store_config.adapters.mysql` 节中的数据库连接参数：

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
            "DBName": "im"
        }
    }
}
```

---

## 3. 数据库初始化 (建表与导入基础数据)

首次启动服务前，**必须**使用 `init-db` 工具创建数据库结构并生成默认数据集：

```bash
cd init-db

# 1. 编译数据库初始化工具 im-db (选择对应数据库 tag，如 mysql)
go build -tags mysql -o im-db .

# 2. 执行数据库建表与数据导入 (--reset=true 会重置/重建数据库表及导入示例账号)
./im-db --config=../server/im.conf --data=data.json --reset=true
```

初始化成功后，控制台会输出：

```text
Sample data processing completed.
All done.
```

---

## 4. 编译与启动服务

返回 `server` 目录，准备资源目录并启动 `im-server` 服务：

```bash
cd ../server

# 1. 创建静态资源目录 (避免静态目录缺失导致服务退出)
mkdir -p static

# 2. 编译服务端程序
go build -tags mysql -o im-server .

# 3. 启动服务
./im-server --config=./im.conf
```

### 成功启动标识：

控制台输出如下日志表示服务已就绪：

```text
INFO server/hdl_grpc.go:181  gRPC/1.82.0 server is registered at [:16060]
INFO server/http.go:81      Listening for client HTTP connections on [:6060]
```

---

## 5. 常见问题排查 (FAQ)

### Q1: `Static content directory is not found .../server/static`

- **原因**：服务端默认会挂载 `server/static` 目录。若该目录不存在则会抛错。
- **解决**：
  在 `server` 目录下执行 `mkdir -p static`，或在启动时添加参数 `--static_data="-"` 显式禁用静态挂载。

### Q2: `bind: address already in use` 端口冲突

- **原因**：默认端口 `:6060` (HTTP) 或 `:16060` (gRPC) 已被本地其他进程或 Docker 容器占用。
- **解决**：
  - **方法 1（推荐）**：关闭占用的容器或进程。
  - **方法 2**：命令行指定新端口启动：
    ```bash
    ./im-server --config=./im.conf --listen=:6070 --grpc_listen=:16070
    ```

---

## 6. 服务接口与连接测试

服务启动后，可以通过以下接口校验运行状态：

1. **查看 Runtime 统计信息**：

   ```bash
   curl http://localhost:6060/debug/vars
   ```
2. **使用 CLI 客户端测试连接与登录**（注：CLI 通过 gRPC 端口 `:16060` 连接）：

   ```bash
   cd ../cli
   go run . -host=localhost:16060 -login-basic=alice:alice123
   ```

---

## 7. 多平台部署打包脚本

项目内置了自动化多平台打包脚本 `build-all.sh`，可批量打包发布二进制产物：

```bash
cd ..
./build-all.sh tag=v0.25.0
```

打包输出产物将自动生成于 `releases/` 目录中。
