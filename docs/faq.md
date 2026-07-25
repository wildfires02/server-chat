# 常见问题解答 (FAQ)

### 问：在 Docker 中运行时，从哪里可以找到服务端日志？
**答**：日志位于容器内部的 `/var/log/im.log`。使用以下命令进入正在运行的容器：
```bash
docker exec -it <运行中的容器名称> /bin/bash
```
然后执行命令查看日志（例如最新 50 行）：
```bash
tail -50 /var/log/im.log
```

如果容器已停止运行，可以将日志拷贝到宿主机当前目录下：
```bash
docker cp <容器名称>:/var/log/im.log ./im.log
```

或者，也可以在 `docker run` 命令中映射宿主机目录 `-v /path/to/logs:/var/log`，将日志直接持久化保存至宿主机。

---

### 问：开启离线 Push 消息推送有哪些方案？
**答**：可使用 **[IM Push Gateway (TNPG)](../server/push/tnpg/)** 或 **[Google FCM](../server/push/fcm/)**：
- **IM Push Gateway (TNPG)**：由 IM 网关代表发送推送，配置极其简便，无需开发者自行申请和维护证书。
- **Google FCM**：基于 Google FCM 原生推送，需要开发者自行打包和发布移动端 App。

---

### 问：如何添加新用户？
**答**：创建用户账号有三种途径：
1. 用户在前端应用（Web、Android、iOS）中自助注册。
2. 使用命令行工具 [cli](../cli/) 发送 `acc` 指令或运行 `useradd` 宏批处理添加。
3. 如果已有外部数据库，可配置 [REST 认证服务](../server/auth/rest/)，在用户首次登录时自动为外部账号初始化创建 IM 账号。

---

### 问：如何限制私有部署注册（私有化部署）？
**答**：如果希望只允许特定人群注册，最简便的方法是在配置文件中限制邮件后缀：在 `im.conf` 的 `"acc_validation" -> "email" -> "domains"` 中填入允许的域名列表（例如 `"domains": ["my-company.com"]`）。如果需要与复杂的外部用户系统深度集成，建议配置使用 [REST 认证适配器](../server/auth/rest/)。

---

### 问：如何创建 `root` 超级管理员账号？
**答**：使用命令行工具 [init-db](../init-db/) 可以直接赋予某个已有账号 `root` 权限：
```bash
./im-db -make_root=usrAbcDef123
```
其中 `usrAbcDef123` 为目标用户的用户 ID。

也可以在数据库中执行 SQL 语句（以 MySQL/PostgreSQL 为例）：
```sql
USE im;
UPDATE auth SET authlvl=30 WHERE uname='basic:target-user-login';
```

---

### 问：网络并发连接数达到约 1000 时出现连接异常，是系统 Bug 吗？
**答**：这不是 Bug。Linux 操作系统为了保证内核安全，默认对单个进程能够打开的最大文件描述符数量（File Descriptors，包含网络 Socket 连接）做出了限制（默认通常为 1024）。在生产环境中高并发部署时，请在操作系统层面调高 `ulimit -n` 的上限值（例如调整为 65535 或更高）。

---

### 问：群组 Topic (Group Topic) 与频道 (Channel) 有何区别？
**答**：频道是群组 Topic 的一种特殊扩展形态。
- **普通群组 (Group Topic)**：订阅成员数量有限（默认限制 128 人），每位成员可以被单独管理（邀请、移出、禁言、提升为管理员/群主）。
- **频道 (Channel)**：允许无上限数量的 `订阅读者 (Readers)`。读者只有只读权限，不能发送消息，加入/退出频道不会产生在线状态 Notification，消息发送者的真实身份 `From` 也会被隐藏。

---

### 问：PostgreSQL 数据库初始化时提示 'missing database' 错误如何解决？
**答**：PostgreSQL 连接时必须指定一个已存在的数据库。首次启动 IM 时，若配置的数据库（如 `im`）尚不存在，连接会尝试退而连接当前连接用户名同名的默认数据库。若将连接用户修改为非 `postgres`（例如 `imadmin`），且该数据库不存在则会报错。解决办法是在 PostgreSQL 中手动预先创建该数据库：
```sql
CREATE DATABASE im;
```
