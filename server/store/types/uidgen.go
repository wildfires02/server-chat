package types

import (
	"encoding/base64"
	"encoding/binary"
	"errors"

	sf "github.com/bwmarrin/snowflake"
	"golang.org/x/crypto/xtea"
)

// UidGenerator 保存 Snowflake 雪花算法实例及 XTEA 分组加密参数。
// 替代数据库默认的 UUID，生成经过加密打散的 uint64 唯一 ID。
type UidGenerator struct {
	seq    *sf.Node
	cipher *xtea.Cipher
}

var ErrUninitialized = errors.New("UID 生成器未初始化")

// Init 初始化 UID 生成器（指定工作节点 ID workerID 和 XTEA 加密 key）。
func (ug *UidGenerator) Init(workerID uint, key []byte) error {
	var err error

	if ug.seq == nil {
		ug.seq, err = sf.NewNode(int64(workerID))
	}
	if err == nil && ug.cipher == nil {
		ug.cipher, err = xtea.NewCipher(key)
	}

	return err
}

// Get 生成一个唯一且外观随机（经 XTEA 弱加密）的 Uid。
func (ug *UidGenerator) Get() Uid {
	buf, err := getIDBuffer(ug)
	if err != nil {
		return ZeroUid
	}
	return Uid(binary.LittleEndian.Uint64(buf))
}

// GetStr 生成相同的唯一 ID 并直接以 Base64 URL 安全字符串形式返回。
// 比先调用 Get() 再转换为 Base64 性能略高。
func (ug *UidGenerator) GetStr() string {
	buf, err := getIDBuffer(ug)
	if err != nil {
		return ""
	}
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(buf)
}

// getIDBuffer 生成雪花算法 ID 并经 XTEA 加密后返回 8 字节数组。
func getIDBuffer(ug *UidGenerator) ([]byte, error) {
	if ug == nil || ug.seq == nil {
		return nil, ErrUninitialized
	}

	id := uint64(ug.seq.Generate().Int64())

	src := make([]byte, 8)
	dst := make([]byte, 8)
	binary.LittleEndian.PutUint64(src, id)
	ug.cipher.Encrypt(dst, src)

	return dst, nil
}

// DecodeUid 解密经过加密的 Uid，还原为原始非负 int64 数字。
// 用于数据库主键兼容性（例如 MySQL 顺序主键索引优化等场景）。
func (ug *UidGenerator) DecodeUid(uid Uid) int64 {
	if ug == nil || ug.seq == nil {
		return 0
	}
	src := make([]byte, 8)
	dst := make([]byte, 8)
	binary.LittleEndian.PutUint64(src, uint64(uid))
	ug.cipher.Decrypt(dst, src)
	return int64(binary.LittleEndian.Uint64(dst))
}

// EncodeInt64 将正整数 int64 加密为 Uid。
// 用于数据库主键兼容性转换。
func (ug *UidGenerator) EncodeInt64(val int64) Uid {
	if ug == nil || ug.seq == nil {
		return ZeroUid
	}
	src := make([]byte, 8)
	dst := make([]byte, 8)
	binary.LittleEndian.PutUint64(src, uint64(val))
	ug.cipher.Encrypt(dst, src)
	return Uid(binary.LittleEndian.Uint64(dst))
}
