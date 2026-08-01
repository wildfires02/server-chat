// Package ringhash 实现一致性环哈希：
//https://en.wikipedia.org/wiki/Consistent_hashing
package ringhash

import (
	"encoding/ascii85"
	"hash/crc32"
	"hash/fnv"
	"sort"
	"strconv"

	"chat/server/logs"
)

// Hash 是本包使用的哈希函数签名。
type Hash func(data []byte) uint32

// elem 保存elem的数据和运行状态。
type elem struct {
	// key 保存键。
	key string
	// hash 指示是否启用或满足哈希。
	hash uint32
}

// sortable 表示sortable领域值。
type sortable []elem

// Len 返回集合中的元素数量。
func (k sortable) Len() int { return len(k) }

// Swap 交换排序集合中两个位置的元素。
func (k sortable) Swap(i, j int) { k[i], k[j] = k[j], k[i] }

// Less 报告排序位置 i 的元素是否应位于位置 j 之前。
func (k sortable) Less(i, j int) bool {
	// 弱哈希函数可能导致冲突。
	if k[i].hash < k[j].hash {
		return true
	}
	if k[i].hash == k[j].hash {
		return k[i].key < k[j].key
	}
	return false
}

// Ring 是环哈希的定义。
type Ring struct {
	keys []elem // 排序后的键列表。

	// signature 保存signature。
	signature string
	// replicas 保存replicas。
	replicas int
	// hashfunc 指示是否启用或满足hashfunc。
	hashfunc Hash
}

// New 初始化一个空的环哈希，给定副本数量和哈希函数。
// 如果哈希函数为 nil，则使用 crc32.NewIEEE()。
func New(replicas int, fn Hash) *Ring {
	return &Ring{
		replicas: replicas,
		hashfunc: fn,
	}
}

// Len 返回环中键的数量。
func (ring *Ring) Len() int {
	return len(ring.keys)
}

// Add 将键添加到环中。
func (ring *Ring) Add(keys ...string) {
	for _, key := range keys {
		for i := range ring.replicas {
			ring.keys = append(ring.keys, elem{
				hash: ring.hashString(strconv.Itoa(i) + key),
				key:  key})
		}
	}
	sort.Sort(sortable(ring.keys))

	// 计算签名
	hash := fnv.New128a()
	b := make([]byte, 4)
	for _, key := range ring.keys {
		b[0] = byte(key.hash)
		b[1] = byte(key.hash >> 8)
		b[2] = byte(key.hash >> 16)
		b[3] = byte(key.hash >> 24)
		hash.Write(b)
		hash.Write([]byte(key.key))
	}

	b = []byte{}
	b = hash.Sum(b)
	dst := make([]byte, ascii85.MaxEncodedLen(len(b)))
	ascii85.Encode(dst, b)
	ring.signature = string(dst)
}

// Get 返回环中与提供的键最近的项。
func (ring *Ring) Get(key string) string {

	if ring.Len() == 0 {
		return ""
	}

	hash := ring.hashString(key)

	// 二分查找合适的副本。
	idx := sort.Search(len(ring.keys), func(i int) bool {
		el := ring.keys[i]
		return (el.hash > hash) || (el.hash == hash && el.key >= key)
	})

	// 表示已循环回到第一个副本。
	if idx == len(ring.keys) {
		idx = 0
	}

	return ring.keys[idx].key
}

// hashString 对默认 CRC32 路径使用可内联的无状态函数，避免每次
// Topic 路由创建 hash.Hash 或因动态函数调用产生字节切片堆分配。
func (ring *Ring) hashString(value string) uint32 {
	if ring.hashfunc == nil {
		return checksumIEEEString(value)
	}
	return ring.hashfunc([]byte(value))
}

// checksumIEEEString 直接遍历只读字符串计算 IEEE CRC32，避免把 Topic
// 名称转换成逃逸到堆上的 []byte。算法与 crc32.ChecksumIEEE 等价。
func checksumIEEEString(value string) uint32 {
	checksum := ^uint32(0)
	for index := range len(value) {
		checksum = crc32.IEEETable[byte(checksum)^value[index]] ^ (checksum >> 8)
	}
	return ^checksum
}

// Signature 返回环的哈希签名。两个相同的环哈希
// 将具有相同的签名。两个具有不同键数量、副本数
// 或哈希函数的环哈希将具有不同的签名。
func (ring *Ring) Signature() string {
	return ring.signature
}

// dump 完成dump所需的内部处理。
func (ring *Ring) dump() {
	for _, e := range ring.keys {
		logs.Info.Printf("key: '%s', hash=%d", e.key, e.hash)
	}
}
