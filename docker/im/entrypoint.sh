#!/bin/bash

# ==============================================================================
# IM 聊天服务器 Docker 容器启动入口脚本
# 功能：解析环境变量、生成 working.config 配置文件、等待数据库就绪、执行 DB 初始化及启动服务
# ==============================================================================

# 如果指定了外部配置文件路径 EXT_CONFIG，直接使用外部配置文件
if [ ! -z "$EXT_CONFIG" ] ; then
	CONFIG="$EXT_CONFIG"

	# 如果配置了 FCM 发送者 ID，启用推送通知功能
	if [ ! -z "$FCM_SENDER_ID" ] ; then
		FCM_PUSH_ENABLED=true
	fi

else
	# 使用模版动态生成 working.config 配置文件
	CONFIG=working.config

	# 清理上一次生成的临时配置文件
	rm -f working.config

	# alldbs 不是具体的适配器名称，如果为 alldbs 则置空，由用户指定 STORE_USE_ADAPTER
	if [ "$TARGET_DB" = "alldbs" ] ; then
		TARGET_DB=
	fi

	# 若定义了 SMTP_SERVER，自动开启邮件验证功能
	if [ ! -z "$SMTP_SERVER" ] ; then
		EMAIL_VERIFICATION_REQUIRED='"auth"'
	fi

	# 若定义了 TLS 域名，自动开启 HTTPS/TLS
	if [ ! -z "$TLS_DOMAIN_NAME" ] ; then
		TLS_ENABLED=true
	fi

	# 若提供了 FCM 凭据文件，开启 FCM 推送通知
	if [ ! -z "$FCM_CRED_FILE" ] ; then
		FCM_PUSH_ENABLED=true
	fi

	# 若配置了 TNPG 认证 Token，开启网关推送
	if [ ! -z "$TNPG_AUTH_TOKEN" ] ; then
		TNPG_PUSH_ENABLED=true
	fi

	# 若配置了 WebRTC ICE 服务器文件，开启视频通话功能
	if [ ! -z "$ICE_SERVERS_FILE" ] ; then
		WEBRTC_ENABLED=true
	fi

	# 逐行读取 config.template 并用当前环境变量替换其中的变量占位符 ($VAR)
	while IFS='' read -r line || [[ -n $line ]] ; do
		while [[ "$line" =~ (\$[A-Z_][A-Z_0-9]*) ]] ; do
			LHS=${BASH_REMATCH[1]}
			RHS="$(eval echo "\"$LHS\"")"
			line=${line//$LHS/"$RHS"}
		done
		echo "$line" >> working.config
	done < config.template
fi

# 若定义了外部静态文件目录 EXT_STATIC_DIR 则使用，否则默认为 ./static
if [ ! -z "$EXT_STATIC_DIR" ] ; then
	STATIC_DIR=$EXT_STATIC_DIR
else
	STATIC_DIR="./static"
fi

# 如果指定了数据库升级 (UPGRADE_DB=true)，则不加载默认示例数据
if [ "$UPGRADE_DB" = "true" ] ; then
	SAMPLE_DATA=
fi

# 如果开启了推送通知，生成供 Web 客户端调用的 firebase-init.js 文件
if [ ! -z "$FCM_PUSH_ENABLED" ] || [ ! -z "$TNPG_PUSH_ENABLED" ] ; then
	# 将 Firebase 客户端初始化参数写入 $STATIC_DIR/firebase-init.js
	cat > $STATIC_DIR/firebase-init.js <<- EOM
const FIREBASE_INIT = {
  apiKey: "$FCM_API_KEY",
  appId: "$FCM_APP_ID",
  messagingSenderId: "$FCM_SENDER_ID",
  projectId: "$FCM_PROJECT_ID",
  messagingVapidKey: "$FCM_VAPID_KEY",
  measurementId: "$FCM_MEASUREMENT_ID"
};
EOM
else
	# 未开启推送时创建空的 firebase-init.js
	echo "" > $STATIC_DIR/firebase-init.js
fi

# 如果指定了 iOS Universal Links App ID，生成相关关联配置文件
if [ ! -z "$IOS_UNIV_LINKS_APP_ID" ] ; then
	cat > $STATIC_DIR/apple-app-site-association <<- EOM
{
  "applinks": {
    "apps": [],
    "details": [
      {
        "appID": "$IOS_UNIV_LINKS_APP_ID",
        "paths": [ "*" ]
      }
    ]
  }
}
EOM
fi

# 若配置了 WAIT_FOR 环境变量 (格式 HOST:PORT)，等待数据库端口可达
if [ ! -z "$WAIT_FOR" ] ; then
	IFS=':' read -ra DB <<< "$WAIT_FOR"
	if [ ${#DB[@]} -ne 2 ]; then
		echo "\$WAIT_FOR (${WAIT_FOR}) 环境变量格式应为 HOST:PORT"
		exit 1
	fi
	until nc -z -v -w5 ${DB[0]} ${DB[1]}; do echo "正在等待数据库连通 ${WAIT_FOR}..."; sleep 3; done
fi

# 执行数据库初始化工具 (根据 RESET_DB / UPGRADE_DB 状态重置或升级数据库)
init_stdout=./init-db-stdout.txt
./init-db \
	--reset=${RESET_DB} \
	--upgrade=${UPGRADE_DB} \
	--config=${CONFIG} \
	--data=${SAMPLE_DATA} \
	--no_init=${NO_DB_INIT} \
	1>${init_stdout}
if [ $? -ne 0 ]; then
	echo "./init-db 执行失败，容器即将退出。"
	exit 1
fi

# 若初始化包含了示例数据，提取默认机器人账号 Tino 的初始密码
if [ ! -z "$SAMPLE_DATA" ] ; then
	grep "usr;tino;" $init_stdout > /botdata/tino-password
fi

if [ -s /botdata/tino-password ] ; then
	# 调用 credentials.sh 将账号密码转换为登录 Cookie 凭据保存到 /botdata/.tn-cookie
	./credentials.sh /botdata/.tn-cookie < /botdata/tino-password
fi

# 组装 IM 服务器启动命令行参数
args=("--config=${CONFIG}" "--static_data=$STATIC_DIR" "--cluster_self=$CLUSTER_SELF" "--pprof_url=$PPROF_URL")

# 启动 IM 聊天服务器，日志重定向至 /var/log/im.log
if [ -f ./im ]; then
	./im "${args[@]}" 2>> /var/log/im.log
else
	./im-server "${args[@]}" 2>> /var/log/im.log
fi
