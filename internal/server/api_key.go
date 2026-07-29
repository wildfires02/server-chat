/******************************************************************************
 *
 *  描述：
 *
 *  认证
 *
 *****************************************************************************/

// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"encoding/base64"
	"strings"

	"chat/server/logs"
)

// 签名的 AppID。组合方式：
//
//	[1:算法版本][4:appid][2:密钥序号][1:是否Root][16:签名] = 24 字节
//
// 可转换为无填充的 base64。所有整数均为小端序
// 密钥各部分字节长度定义
const (
	// 此 API 方案的版本号
	apikeyVersion = 1
	// 已废弃，未来将移除
	apikeyAppID = 4
	// 密钥的序列号
	apikeySequence = 2
	// 指示密钥是否授予 root 权限
	apikeyWho = 1
	// 密钥的密码学 (HMAC) 签名
	apikeySignature = 16
	// 密钥长度（字节）
	apikeyLength = apikeyVersion + apikeyAppID + apikeySequence + apikeyWho + apikeySignature
)

// 客户端签名验证
//
//	key: 客户端密钥
//
// 返回应用 id、密钥类型
func checkAPIKey(apikey string) (isValid, isRoot bool) {
	// 同时支持标准 base64 (+, /) 和 URL 安全 base64 (-, _)
	apikey = strings.NewReplacer("+", "-", "/", "_").Replace(apikey)
	if declen := base64.URLEncoding.DecodedLen(len(apikey)); declen != apikeyLength {
		return
	}

	data, err := base64.URLEncoding.DecodeString(apikey)
	if err != nil {
		logs.Warn.Println("failed to decode.base64 appid ", err)
		return
	}
	if data[0] != 1 {
		logs.Warn.Println("unknown appid signature algorithm ", data[0])
		return
	}

	hasher := hmac.New(md5.New, globals.apiKeySalt)
	hasher.Write(data[:apikeyVersion+apikeyAppID+apikeySequence+apikeyWho])
	check := hasher.Sum(nil)
	if !bytes.Equal(data[apikeyVersion+apikeyAppID+apikeySequence+apikeyWho:], check) {
		logs.Warn.Println("invalid apikey signature")
		return
	}

	isRoot = (data[apikeyVersion+apikeyAppID+apikeySequence] == 1)

	isValid = true

	return
}
