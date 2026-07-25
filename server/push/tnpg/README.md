# TNPG：离线推送网关 (Push Gateway) 适配器

本模块为 **Push Gateway (TNPG)** 推送网关适配器，用于服务端向离线移动端发送离线消息推送。

## 1. 概述与原理

TNPG 旨在简化私有化独立部署环境下的移动端离线推送配置。

如果在不开启 TNPG 的情况下部署 IM 服务端，直接向 App 发送推送通常需要：
1. 申请并配置自己的 [Google FCM 证书与密钥](../fcm/) 以及 Apple APNs 证书；
2. 重新编译 iOS 与 Android 原生客户端 App；
3. 使用自己的开发者账号打包发布到 App Store 和 Google Play 应用商店。

这通常较为繁琐且耗时。**Push Gateway** 解决了这一难题：IM 服务端直接将离线推送消息转发给配置的 Push Gateway 网关，网关使用统一证书代表服务端向 App 交付推送消息。网关底层支持 [Google FCM](https://firebase.google.com/docs/cloud-messaging/) 协议及相同的移动平台。

使用 Push Gateway 的最大优势是**配置极其简单**：无须重新编译客户端，只需在服务端配置文件中开启网关地址及认证 Token 即可。

---

## 2. 配置说明

### 2.1 获取网关认证 Token

1. 登录自建或托管的 Push Gateway 管理控制台，创建机构/组织（Organization）；
2. 在控制台的 *Push Gateway* 区域获取对应机构的授权 Token。

### 2.2 配置 IM 服务端

更新服务端主配置文件 [`im.conf`](../../im.conf)，在 `"push"` 节点下的 `"tnpg"` 配置项中开启并设置参数：

```json
{
  "enabled": true,
  "server_addr": "https://your-pushgw.domain.com/",
  "org": "myorg",
  "token": "SoMe_LonG.RaNDoM-StRiNg.12345"
}
```

*说明：使用 TNPG 模式时，请确保 `fcm` 节点已禁用（`"enabled": false`）或直接移除。*
