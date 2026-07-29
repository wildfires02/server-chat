# 脚本入口

脚本按用途分为四类：

- 构建：`build-all.sh`、`build-exporter.sh`、`docker-build.sh`。
- 发布：`docker-release.sh`、`certify-cluster-72h.sh`。
- 本地运行：`run-cluster.sh`。
- 验证：`validate-delivery.sh`、`test-standalone.sh`、`test-cluster.sh` 及进程级测试。

## 推荐命令

```bash
# 配置、Compose、Kubernetes、Shell 和生产默认值统一检查。
./scripts/validate-delivery.sh

# 构建精确版本镜像；默认不会创建 latest。
./scripts/docker-build.sh --tag v0.29.0 --db postgres

# 发布前由 CI 登录镜像仓库，再推送精确版本。
./scripts/docker-release.sh --tag v0.29.0 --db postgres

# 只有明确需要稳定别名时才推送 minor/latest。
IM_TAG_LATEST=1 ./scripts/docker-build.sh --tag v0.29.0
./scripts/docker-release.sh --tag v0.29.0 --include-aliases
```

所有脚本使用长参数，不再接受 `tag=...`、`db=...` 形式，也不读取包含明文密码
的 `.dockerhub` 文件。镜像仓库登录必须由 CI 的凭据助手或短期令牌完成。
