# IM 即时通讯服务器 (IM Instant Messaging Server)

IM 是一套全栈即时通讯 (IM) 开源解决方案。后端采用原生 [Go](http://golang.org) 语言编写（开源协议 [GPL 3.0](http://www.gnu.org/licenses/gpl-3.0.en.html)），客户端涵盖 Web (ReactJS)、Android (Java)、iOS (Swift) 以及基于 [gRPC](https://grpc.io/) 的多语言 SDK（支持 C++、C#、Go、Java、Node、Python、Ruby 等）。

底层网络传输支持 Websocket 上的 JSON 数据交互（亦支持 HTTP 长轮询），或基于 gRPC 的 [protobuf](https://developers.google.com/protocol-buffers/) 高效二进制传输。

---

## 快速部署指南

- **部署安装**：请参阅 [本地源码与二进制安装指南](./INSTALL.md) 或 [Docker 容器化部署指南](./docker/README.md)。
- **Docker Compose**：请参阅 [Docker Compose 单机与集群部署指南](./docker/docker-compose/README.md)。
- **开发与架构文档**：
  - [REST API 接口规范](docs/API.md)
  - [常见问题解答 (FAQ)](docs/faq.md)
  - [服务器监控配置指南](docs/monitoring.md)
  - [配置文件使用说明](server/im.conf)

---

## 项目特性

### 1. 核心功能支持
- **多端全平台覆盖**：支持 Web 网页端、Android 端、iOS 端及 Python 命令行脚本。
- **丰富的消息交互**：
  - 单聊与群组聊天。
  - 音视频点对点通话及语音消息。
  - 支持无限制订阅者数量的广播频道。
  - 多设备协同与消息实时同步。
  - Markdown 风格富文本消息、内联图片、视频及文件附件传输。
  - 支持已发送消息的编辑、撤回、引用、转发及置顶。
- **安全与权限控制**：
  - 基于 ACL 位图的细粒度权限控制管理。
  - 支持第三方自定义认证适配器（REST 认证等）。
  - 支持设置全网域名黑白名单防垃圾注册。

### 2. 高并发与架构设计
- **分片集群与故障转移**：支持多节点分布式分片集群部署与节点容灾。
- **多数据库后端支持**：通过存储适配器完美兼容 MySQL、PostgreSQL、MongoDB 及 RethinkDB。
- **对象存储适配**：支持本地文件系统 (FS) 及 Amazon S3 等云存储扩展。

---

## 配置文件说明

IM 服务端的配置文件位于 [`server/im.conf`](./server/im.conf)。在启动 IM 服务端时，可通过 `-config` 命令行参数指定配置路径，例如：

```bash
# 启动单机 IM 服务端
./im -config=./server/im.conf
```

详细配置说明请直接查阅 [`server/im.conf`](./server/im.conf) 文件内部注释。
