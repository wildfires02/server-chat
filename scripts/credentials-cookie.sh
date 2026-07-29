#!/bin/bash

# 把初始化工具输出的机器人账号凭据转换为认证 Cookie。
# 该密码由 im-db 写入到标准输出 (stdout)，本脚本将其转换为聊天机器人的认证 Cookie。

# 脚本接收形如 'usr;tino;usrImlot_X9vAc;cOuTvzVa' 的字符串 (忽略类型;登录名;用户ID;密码)
# 并将其格式化为 JSON 结构的聊天机器人认证 Cookie，例如：
# '{"schema": "basic", "secret": "username:password", "user": "user_id"}'。

COOKIE_FILE=$@

while read line; do
  IFS=';' read -r -a parts <<< "$line"
  if [ ${#parts[@]} -eq 0 ] ; then
    continue
  fi

  # 如果指定了 Cookie 文件名，则写入到该文件
  # 否则写入到标准输出 (stdout)
  if [ "$COOKIE_FILE" ]; then
    exec 3>"$COOKIE_FILE"
  else
    exec 3>&1
  fi

  echo "{\"schema\": \"basic\", \"secret\": \"${parts[1]}:${parts[3]}\", \"user\": \"${parts[2]}\"}" 1>&3
  break
done < /dev/stdin
