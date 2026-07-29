# systemd 三/五节点生产部署

此目录提供不依赖 Kubernetes 的三/五节点交付模板。基础三台主机使用稳定
DNS 名 `im-0.im.internal`～`im-2.im.internal`；在线扩容主机预留为
`im-3.im.internal`、`im-4.im.internal`。所有主机部署同一不可变版本。

## 主机准备

在每台主机执行等价操作：

```bash
sudo useradd --system --home-dir /var/lib/im --shell /usr/sbin/nologin im
sudo install -o root -g root -m 0755 im-server /usr/local/bin/im-server
sudo install -d -o root -g im -m 0750 /etc/im
sudo install -d -o root -g im -m 0750 /etc/im/nodes
sudo install -o root -g im -m 0640 configs/im.cluster.yaml /etc/im/im.cluster.yaml
sudo install -o root -g root -m 0644 deployments/systemd/im-server@.service /etc/systemd/system/im-server@.service
```

二进制必须由 CI 校验 SHA-256 或签名后再安装。数据库 Schema 由独立迁移流程升级，禁止三台业务主机在启动时并发迁移。

## Secret 和证书

- 将 `im-node.env.example` 的实际 Secret 写入当前节点的
  `/etc/im/nodes/im-N.env`，属主 `root:im`、权限 `0640`。服务账号只能读取，
  不能覆盖由 Secret Agent 落盘的内容。
- etcd 客户端凭据放到 `/etc/im/tls/etcd/{ca.pem,client.pem,client-key.pem}`。
- 集群专用 CA 放到 `/etc/im/tls/cluster/ca.pem`。
- 每个节点的证书放到 `/etc/im/tls/cluster/im-N/{cert.pem,key.pem}`。

节点证书必须同时具备服务端和客户端 EKU，并包含与节点名完全一致的 DNS SAN `im-N`；每个节点使用独立私钥，不能使用通配符证书。

## 网络边界

- 负载均衡器访问 TCP `6060` 和 `16060`，只向 `/readyz` 成功的节点转发新连接。
- 三个 IM 节点之间仅开放 TCP `12000`。
- IM 节点到高可用 etcd 开放 TCP `2379`，到 PostgreSQL 开放 TCP `5432`。
- `/drainz` 和 `/clusterz` 只允许本机回环访问，不能由负载均衡器或公网暴露。

## 校验、启动和滚动停止

以 `im-0` 为例：

```bash
sudo systemctl daemon-reload
sudo systemd-analyze verify /etc/systemd/system/im-server@.service
sudo systemctl enable --now im-server@im-0
curl --fail http://127.0.0.1:6060/livez
curl --fail http://127.0.0.1:6060/readyz
```

启动时 `ExecStartPre` 会在同一套 systemd 环境中执行
`--validate_config`，门禁失败时主进程不会启动。三个节点可按顺序启动；
达到多数派前 `/readyz` 返回失败并拒绝写入。滚动升级一次只处理一个节点：

```bash
sudo systemctl stop im-server@im-0
sudo install -o root -g root -m 0755 im-server /usr/local/bin/im-server
sudo systemctl start im-server@im-0
```

`ExecStop` 会先调用同步 Drain：从 etcd Owner Ring 摘除节点、推进数据库 fence，并等待可靠 Lane 排空。只有该节点重新 Ready 后才能继续下一台。

## 在线扩缩容

扩容前先为 `im-3`、`im-4` 准备专属证书、Secret、二进制和相同候选节点配置，
再启动两个实例。它们会以 Joining 状态注册，`/readyz` 保持 503。

```bash
sudo systemctl enable --now im-server@im-3 im-server@im-4
curl --fail --request POST \
  --header 'Content-Type: application/json' \
  --data '{"members":["im-0","im-1","im-2","im-3","im-4"]}' \
  http://127.0.0.1:6060/clusterz
```

从五节点缩容到三节点时，先分别在 `im-3`、`im-4` 本机调用 `/drainz`，然后
从任一仍 Ready 的活动节点提交三节点集合，最后停止两个已移除实例：

```bash
curl --fail --request POST \
  --header 'Content-Type: application/json' \
  --data '{"members":["im-0","im-1","im-2"]}' \
  http://127.0.0.1:6060/clusterz
sudo systemctl disable --now im-server@im-3 im-server@im-4
```

不能在部分主机使用不同 `nodes` 白名单，也不能跳过 Drain 或直接降低多数派。
