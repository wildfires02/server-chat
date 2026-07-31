# 文档导航

本页是仓库文档的唯一总入口。文档按使用目标分类；规划资料、操作手册和协议
参考彼此分开，避免把未完成计划误当成当前能力。

## 1. 从这里开始

| 目标 | 文档 |
| --- | --- |
| 了解项目范围 | [项目说明](../README.md) |
| 本地启动服务 | [本地开发启动指南](../STARTUP.md) |
| 从源码或发布包安装 | [安装与构建](../INSTALL.md) |
| 修改配置 | [配置说明](../configs/README.md) |
| 了解代码边界 | [代码架构](code-architecture.md) |
| 查协议字段 | [服务端协议参考](API.md) |
| 复制请求示例 | [接口调用示例](api-examples.md) |

## 2. 部署与运维

| 场景 | 文档 | 定位 |
| --- | --- | --- |
| 开发单机 | [单机模式说明](standalone.md) | 开发、测试、演示 |
| Docker | [Docker 部署](../deployments/docker/README.md) | 本地镜像和容器 |
| Docker Compose | [Compose 部署](../deployments/docker/compose/README.md) | 开发单机和开发集群 |
| Kubernetes | [Kubernetes 部署](../deployments/kubernetes/README.md) | 三至五节点生产模板 |
| 集群操作 | [生产集群操作手册](cluster-operations.md) | 发布、排空、轮换、扩缩容、回滚 |
| 数据库升级 | [数据库版本与迁移流程](database-migrations.md) | 版本历史、升级 SOP、验证和回滚 |
| 监控 | [监控与健康检查](monitoring.md) | 健康端点、运行指标、导出器 |
| 故障排查 | [常见问题](faq.md) | 常见安装和运行问题 |

生产部署必须同时满足目标环境的基础设施验证和
[集群验收计划](planning/cluster.md)，不能只依据进程成功启动。

## 3. 协议与功能参考

| 主题 | 文档 |
| --- | --- |
| 服务端协议 | [API.md](API.md) |
| 常用调用示例 | [api-examples.md](api-examples.md) |
| 富文本消息 | [drafty.md](drafty.md) |
| 全部产品功能需求与服务端设计 | [im-product-requirements.md](im-product-requirements.md) |
| 联系人、文件处理与素材协议参考 | [contacts-files-assets.md](contacts-files-assets.md) |
| 用户和主题描述 | [thecard.md](thecard.md) |
| 音视频通话流程 | [call-establishment.md](call-establishment.md) |
| 群消息逐成员已读 | [message-seen-by.md](message-seen-by.md) |
| 国际化 | [translations.md](translations.md) |
| 双向自动翻译与多供应商配置 | [automatic-translation.md](automatic-translation.md) |
| Protobuf 源文件 | [`api/pbx/*.proto`](../api/pbx/) |

[`im-product-requirements.md`](im-product-requirements.md) 是产品功能需求的唯一入口。
`API.md`、`contacts-files-assets.md` 等页面只说明已实现协议和技术操作，不再维护另一套
功能需求清单。

`API.md` 是字段语义和协议行为的权威说明；`api-examples.md` 只提供常用请求
样例，不能替代完整协议。

## 4. 架构、容量与测试

| 主题 | 文档 |
| --- | --- |
| 代码职责与维护边界 | [code-architecture.md](code-architecture.md) |
| 集群隔离栅栏 | [cluster-owner-fencing.md](cluster-owner-fencing.md) |
| 单机容量基线 | [standalone-capacity-baseline.md](standalone-capacity-baseline.md) |
| 集群容量基线 | [cluster-capacity-baseline.md](cluster-capacity-baseline.md) |
| 集群测试 | [tests/cluster/README.md](../tests/cluster/README.md) |
| 压力测试 | [tests/load/README.md](../tests/load/README.md) |
| 最新集群认证结果 | [test-results/cluster-certification-latest.md](../test-results/cluster-certification-latest.md) |

容量基线只代表文档记录的硬件和测试条件，不是通用生产容量承诺。

## 5. 组件文档

- [命令行客户端](../cmd/im-cli/README.md)
- [数据库初始化工具](../cmd/init-db/README.md)
- [监控导出器](../cmd/exporter/README.md)
- [聊天机器人示例](../cmd/chatbot/README.md)
- [密钥工具](../cmd/keygen/README.md)
- [外部认证示例](../cmd/rest-auth/README.md)
- [推送适配器](../server/push/)
- [数据库结构](../server/db/)

组件目录内的说明只负责该组件，不重复描述完整安装和部署流程。

## 6. 规划与历史资料

规划资料统一放在 [`planning/`](planning/README.md)：

- 性能与部署模式路线图。
- 开发单机版实施记录。
- 生产集群实施与验收计划。
- 产品能力差距分析。

规划文档用于记录目标、阶段和未完成事项，不作为当前配置、协议或操作步骤的
权威来源。

## 7. 文档维护规则

1. 一个主题只保留一份权威文档，其他位置只提供摘要和链接。
2. 根目录只放项目入口、安装和启动文档；专题资料放入 `docs/`。
3. 操作手册必须写明适用环境、前置条件、风险和验证方式。
4. 规划文档必须标记日期与状态，不得用计划描述替代已实现行为。
5. 示例中的密码和密钥必须明确标注为开发值。
6. 使用相对链接；移动文件后必须检查全部 Markdown 链接。
7. 不维护容易过期的手写目录，优先使用短文档和清晰标题层级。
8. 自动生成文件的说明通过源文件和生成器维护，不直接编辑生成产物。

配置行为冲突时，以代码、配置校验器和当前部署模板为准；发现文档与实现不一致
时，应在同一变更中修正文档。
