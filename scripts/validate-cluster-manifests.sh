#!/usr/bin/env bash

# 离线渲染并校验生产集群部署物的基本结构。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
KUSTOMIZE_DIR="${REPO_ROOT}/deployments/kubernetes/base"
OUTPUT_FILE="$(mktemp "${TMPDIR:-/tmp}/im-kubernetes.XXXXXX.yaml")"

# 无论校验成功或失败都清理包含渲染配置的临时文件。
cleanup() {
  rm -f "${OUTPUT_FILE}"
}
trap cleanup EXIT

if ! command -v kubectl >/dev/null 2>&1; then
  echo "错误：需要 kubectl 执行 Kustomize 渲染。" >&2
  exit 1
fi

kubectl kustomize "${KUSTOMIZE_DIR}" >"${OUTPUT_FILE}"

# 关键资源缺失时立即失败，防止误交付不具备高可用门禁的清单。
for resource in \
  "kind: StatefulSet" \
  "kind: PodDisruptionBudget" \
  "kind: NetworkPolicy" \
  "readinessProbe:" \
  "livenessProbe:" \
  "preStop:" \
  "automountServiceAccountToken: false" \
  "readOnlyRootFilesystem: true" \
  "runAsNonRoot: true"; do
  if ! grep -q "${resource}" "${OUTPUT_FILE}"; then
    echo "错误：渲染结果缺少 ${resource}" >&2
    exit 1
  fi
done

if grep -Eq 'image:[[:space:]]+[^[:space:]]+:latest' "${OUTPUT_FILE}"; then
  echo "错误：生产清单禁止使用 latest 镜像。" >&2
  exit 1
fi

# 安装 kubeconform 的 CI 环境会继续执行 Kubernetes OpenAPI Schema 校验。
if command -v kubeconform >/dev/null 2>&1; then
  kubeconform -strict -summary "${OUTPUT_FILE}"
fi

echo "集群部署清单离线校验通过：${KUSTOMIZE_DIR}"
