# Tinode 压力测试说明文档

本目录下的文件用于对 Tinode 服务端执行基础压力测试与性能基准评估。若需要测试自定义部署环境下的系统性能与瓶颈，可参考本指南。

---

## 1. Tsung 压测框架

`tsung.xml` 为 [Tsung](http://tsung.erlang-projects.org/) 分布式压力测试工具的配置文件。`tinode.beam` 是测试过程中所需的 Erlang 二进制编译文件，用于快速生成经过 Base64 编码的账号密码对。`tinode.erl` 是 `tinode.beam` 的源码文件（可通过 `erlc tinode.erl` 重新编译为 `tinode.beam`）。

[安装 Tsung](http://tsung.erlang-projects.org/user_manual/installation.html) 后，执行以下命令开始压测：

```bash
tsung -f ./tsung.xml start
```

---

## 2. Gatling 压测框架

针对 [Gatling](https://gatling.io/) 的压测场景脚本位于 `loadtest.scala`（及 `tinode.scala`）中。

在 [安装 Gatling](https://gatling.io/docs/current/installation/) 后，运行命令：

```bash
gatling.sh -sf . -rsf . -rd "na" -s tinode.Loadtest
```

### 可用压测场景列表：

* **`tinode.Loadtest`**：模拟用户连接服务端后，拉取订阅列表并向这些主题依次发送若干消息。
* **`tinode.MeLoadtest`**：测试服务端对个人主题 `me` 长连接并发数量的吞吐极限。
* **`tinode.SingleTopicLoadtest`**：模拟海量用户同时连接到指定主题（通常为群组 Topic）并高频发送广播消息。

### 参数配置说明

可以通过环境变量 `JAVA_OPTS` 向脚本传递参数：

| 参数名称             | 默认值        | 详细说明                                                                                                          |
| :------------------- | :------------ | :---------------------------------------------------------------------------------------------------------------- |
| `num_sessions`     | `10000`     | 建立连接的总并发 Session 数                                                                                       |
| `ramp`             | `300`       | 压力提升的梯度时间（秒），在此时间内平滑增加连接数至`num_sessions`                                              |
| `publish_count`    | `10`        | 账号在所订阅主题中发送消息的总数量                                                                                |
| `publish_interval` | `100`       | 账号发送后续消息的最大等待间隔时间（毫秒）                                                                        |
| `accounts`         | `users.csv` | （适用于`Loadtest` 和 `SingleTopicLoadtest`）用于压测的用户账号 CSV 文件路径（格式：`用户名,密码[,Token]`） |
| `topic`            |               | （仅适用于`SingleTopicLoadtest`）压测的目标主题名称                                                             |
| `username`         |               | （仅适用于`MeLoadtest`）订阅 `me` 主题的测试用户名                                                            |
| `password`         |               | （仅适用于`MeLoadtest`）测试用户密码                                                                            |

---

### 运行范例

#### 范例 1：基础并发负载测试

```shell
JAVA_OPTS="-Daccounts=users.csv -Dnum_sessions=100 -Dramp=10" gatling.sh -sf . -rsf . -rd "na" -s tinode.Loadtest
```

在 10 秒内平滑增加负载，提升至 `users.csv` 中列出的 100 个 Session 并发。

#### 范例 2：个人主题 `me` 极限长连接测试

```shell
JAVA_OPTS="-Dusername=user1 -Dpassword=user1123 -Dnum_sessions=10000 -Dramp=600" gatling.sh -sf . -rsf . -rd "na" -s tinode.MeLoadtest
```

在 600 秒内平滑建立 10,000 个长连接 Session 并连接到账号 `user1` 的 `me` 个人主题。

#### 范例 3：热点大群消息广播测试

```shell
JAVA_OPTS="-Dtopic=grpYOrcDwORhPg -Daccounts=users.csv -Dnum_sessions=10000 -Dramp=1000 -Dpublish_count=2 -Dpublish_interval=300" gatling.sh -sf . -rsf . -rd "na" -s tinode.SingleTopicLoadtest
```

在 1000 秒内建立 10,000 个用户长连接并接入指定群组 `grpYOrcDwORhPg`，每个用户在最大 300 秒间隔内各发送 2 条消息。

---

## 3. 官方实测基准数据参考

在标准 AWS `t3.xlarge` 节点（4 vCPU，16GB 内存，5Gbps 网络）+ `MySQL` 存储后端下测试单节点 Tinode（包含 50,000 个合成账号）：

在性能指标开始下降之前，服务器表现如下：

* **单机并发长连接**：服务器可稳定维持 **50,000 个并发连接 Session**。
* **热点大群吞吐能力**：单个群组 Topic 可稳定支持 **1,500 个并发活跃 Session 同时发言与接收广播**。
