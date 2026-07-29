#!/usr/bin/env bash

# scripts/lib/common.sh 保存发布与验证脚本共享的无副作用辅助函数。

# im_die 输出统一错误并退出。
im_die() {
  echo "错误：$*" >&2
  exit 1
}

# im_require_command 检查外部命令是否存在。
im_require_command() {
  local command_name=$1
  command -v "${command_name}" >/dev/null 2>&1 ||
    im_die "缺少必需命令：${command_name}"
}

# im_repo_root 返回当前脚本所属仓库根目录。
im_repo_root() {
  local script_dir=$1
  (
    cd "${script_dir}/.." >/dev/null 2>&1
    pwd
  )
}

# im_normalize_version 校验 semver，并移除可选的 v 前缀。
im_normalize_version() {
  local raw_version=${1#v}
  if [[ ! "${raw_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
    im_die "版本必须是 semver，例如 v0.29.0 或 v0.29.0-rc.1"
  fi
  printf '%s\n' "${raw_version}"
}

# im_is_stable_version 判断版本是否可发布稳定别名。
im_is_stable_version() {
  [[ "$1" != *-* && "$1" != *+* ]]
}

# im_validate_database 校验构建支持的数据库标签。
im_validate_database() {
  case "$1" in
    mysql|postgres|mongodb|rethinkdb|alldbs) ;;
    *) im_die "数据库必须是 mysql、postgres、mongodb、rethinkdb 或 alldbs" ;;
  esac
}
