// Package main 实现 API Key 生成与校验命令。
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
)

// 生成 API Key
// 二进制字节结构 (小端序 Little-Endian):
//
//	[1:算法版本][4:AppID(已废弃)][2:Key序列号][1:isRoot标志][16:HMAC-MD5签名] = 24 字节
//
// 结果无缝转换为 URLEncoding / Standard Base64 字符串（无需 Padding 填充）。
func main() {
	version := flag.Int("sequence", 1, "API Key 递增序列号")
	isRoot := flag.Int("isroot", 0, "是否为超级管理员 Root API Key?")
	apikey := flag.String("validate", "", "待校验的 API Key 字符串")
	hmacSalt := flag.String("salt", "", "HMAC 随机盐值 (32 字节随机数经 Base64 编码)")

	flag.Parse()

	if *apikey != "" {
		if *hmacSalt == "" {
			log.Println("错误：进行 API Key 校验时必须提供 --salt 参数")
			os.Exit(1)
		}
		os.Exit(validate(*apikey, *hmacSalt))
	} else {
		os.Exit(generate(*version, *isRoot, *hmacSalt))
	}
}

const (
	// APIKEY_VERSION 算法版本号
	APIKEY_VERSION = 1
	// APIKEY_APPID 应用 ID 字节长度 (已废弃)
	APIKEY_APPID = 4
	// APIKEY_SEQUENCE 序列号字节长度
	APIKEY_SEQUENCE = 2
	// APIKEY_WHO Root 用户标志位字节长度
	APIKEY_WHO = 1
	// APIKEY_SIGNATURE HMAC-MD5 密文签名字节长度
	APIKEY_SIGNATURE = 16
	// APIKEY_LENGTH Key 总字节长度 (24 字节)
	APIKEY_LENGTH = APIKEY_VERSION + APIKEY_APPID + APIKEY_SEQUENCE + APIKEY_WHO + APIKEY_SIGNATURE
)

// generate 完成generate所需的内部处理。
func generate(sequence, isRoot int, hmacSaltB64 string) int {
	var data [APIKEY_LENGTH]byte
	var hmacSalt []byte

	if hmacSaltB64 == "" {
		hmacSalt = make([]byte, 32)
		_, err := rand.Read(hmacSalt)
		if err != nil {
			log.Println("错误：生成 HMAC 随机盐值失败:", err)
			return 1
		}
	} else {
		var err error
		hmacSalt, err = base64.URLEncoding.DecodeString(hmacSaltB64)
		if err != nil {
			// 尝试使用标准 Base64 解码
			hmacSalt, err = base64.StdEncoding.DecodeString(hmacSaltB64)
		}
		if err != nil {
			log.Println("错误：解码 HMAC 盐值失败:", err)
			return 1
		}
	}
	// 确保盐值为标准 Base64 编码格式：im.yaml 配置文件需要 Standard Base64 格式
	hmacSaltB64 = base64.StdEncoding.EncodeToString(hmacSalt)

	// 打包字段: [1:算法版本][4:AppID][2:Key序列号][1:isRoot标志]
	data[0] = 1 // 默认算法版本
	binary.LittleEndian.PutUint32(data[APIKEY_VERSION:], uint32(0))
	binary.LittleEndian.PutUint16(data[APIKEY_VERSION+APIKEY_APPID:], uint16(sequence))
	data[APIKEY_VERSION+APIKEY_APPID+APIKEY_SEQUENCE] = uint8(isRoot)

	hasher := hmac.New(md5.New, hmacSalt)
	hasher.Write(data[:APIKEY_VERSION+APIKEY_APPID+APIKEY_SEQUENCE+APIKEY_WHO])
	signature := hasher.Sum(nil)

	copy(data[APIKEY_VERSION+APIKEY_APPID+APIKEY_SEQUENCE+APIKEY_WHO:], signature)

	var strIsRoot string
	if isRoot == 1 {
		strIsRoot = "ROOT"
	} else {
		strIsRoot = "ordinary"
	}

	fmt.Printf("API key v%d seq%d [%s]: %s\nHMAC salt: %s\n", 1, sequence, strIsRoot,
		base64.URLEncoding.EncodeToString(data[:]), hmacSaltB64)

	return 0
}

// validate 校验validate的输入和约束。
func validate(apikey string, hmacSaltB64 string) int {
	var version uint8
	var deprecated uint32
	var sequence uint16
	var isRoot uint8

	var strIsRoot string

	hmacSalt, err := base64.URLEncoding.DecodeString(hmacSaltB64)
	if err != nil {
		// 尝试使用标准 Base64 解码
		hmacSalt, err = base64.StdEncoding.DecodeString(hmacSaltB64)
	}
	if err != nil {
		log.Println("错误：解码 HMAC 盐值失败:", err)
		return 1
	}

	if declen := base64.URLEncoding.DecodedLen(len(apikey)); declen != APIKEY_LENGTH {
		log.Printf("错误：无效的 Key 长度 %d，期望长度为 %d", declen, APIKEY_LENGTH)
		return 1
	}

	data, err := base64.URLEncoding.DecodeString(apikey)
	if err != nil {
		log.Println("错误：解码 base64-URL API Key 失败:", err)
		return 1
	}

	buf := bytes.NewReader(data)
	binary.Read(buf, binary.LittleEndian, &version)

	if version != 1 {
		log.Println("错误：未知的签名算法版本:", version)
		return 1
	}

	hasher := hmac.New(md5.New, hmacSalt)
	hasher.Write(data[:APIKEY_VERSION+APIKEY_APPID+APIKEY_SEQUENCE+APIKEY_WHO])

	if signature := hasher.Sum(nil); !bytes.Equal(data[APIKEY_VERSION+APIKEY_APPID+APIKEY_SEQUENCE+APIKEY_WHO:], signature) {
		log.Println("错误：API Key HMAC 签名无效", data, signature)
		return 1
	}
	// 解包剩余字段: [1:算法版本][4:deprecated][2:Key序列号][1:isRoot]
	binary.Read(buf, binary.LittleEndian, &deprecated)
	binary.Read(buf, binary.LittleEndian, &sequence)
	binary.Read(buf, binary.LittleEndian, &isRoot)

	if isRoot == 1 {
		strIsRoot = "ROOT"
	} else {
		strIsRoot = "ordinary"
	}

	fmt.Printf("校验成功: v%d seq%d [%s]\n", version, sequence, strIsRoot)

	return 0
}
