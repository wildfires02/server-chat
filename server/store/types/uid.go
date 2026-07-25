package types

import (
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"slices"
	"sort"
	"strings"
)

// Uid 是数据库特定的记录 ID，适合作为主键使用。
type Uid uint64

// ZeroUid 是一个表示未初始化 Uid 的常量。
const ZeroUid Uid = 0

// 各种 Uid 表示形式的长度。
const (
	uidBase64Unpadded = 11
	p2pBase64Unpadded = 22
)

// IsZero 检查 Uid 是否未初始化。
func (uid Uid) IsZero() bool {
	return uid == ZeroUid
}

// Compare 在 uid 等于 u2 时返回 0，u2 大于 uid 时返回 1，u2 小于 uid 时返回 -1。
func (uid Uid) Compare(u2 Uid) int {
	if uid < u2 {
		return -1
	} else if uid > u2 {
		return 1
	}
	return 0
}

// MarshalBinary 将 Uid 转换为字节切片。
func (uid Uid) MarshalBinary() ([]byte, error) {
	dst := make([]byte, 8)
	binary.LittleEndian.PutUint64(dst, uint64(uid))
	return dst, nil
}

// UnmarshalBinary 从字节切片中读取 Uid。
func (uid *Uid) UnmarshalBinary(b []byte) error {
	if len(b) < 8 {
		return errors.New("Uid.UnmarshalBinary: invalid length")
	}
	*uid = Uid(binary.LittleEndian.Uint64(b))
	return nil
}

// UnmarshalText 从以字节切片表示的字符串中读取 Uid。
func (uid *Uid) UnmarshalText(src []byte) error {
	if len(src) != uidBase64Unpadded {
		return errors.New("Uid.UnmarshalText: invalid length")
	}
	dec := make([]byte, base64.URLEncoding.WithPadding(base64.NoPadding).DecodedLen(uidBase64Unpadded))
	count, err := base64.URLEncoding.WithPadding(base64.NoPadding).Decode(dec, src)
	if count < 8 {
		if err != nil {
			return errors.New("Uid.UnmarshalText: failed to decode " + err.Error())
		}
		return errors.New("Uid.UnmarshalText: failed to decode")
	}
	*uid = Uid(binary.LittleEndian.Uint64(dec))
	return nil
}

// MarshalText 将 Uid 转换为以字节切片表示的字符串。
func (uid *Uid) MarshalText() ([]byte, error) {
	if *uid == ZeroUid {
		return []byte{}, nil
	}
	src := make([]byte, 8)
	dst := make([]byte, base64.URLEncoding.WithPadding(base64.NoPadding).EncodedLen(8))
	binary.LittleEndian.PutUint64(src, uint64(*uid))
	base64.URLEncoding.WithPadding(base64.NoPadding).Encode(dst, src)
	return dst, nil
}

// MarshalJSON 将 Uid 转换为双引号包裹的（"ajjj"）字符串。
func (uid *Uid) MarshalJSON() ([]byte, error) {
	dst, _ := uid.MarshalText()
	return append(append([]byte{'"'}, dst...), '"'), nil
}

// UnmarshalJSON 从双引号包裹的字符串中读取 Uid。
func (uid *Uid) UnmarshalJSON(b []byte) error {
	size := len(b)
	if size != (uidBase64Unpadded + 2) {
		return errors.New("Uid.UnmarshalJSON: invalid length")
	} else if b[0] != '"' || b[size-1] != '"' {
		return errors.New("Uid.UnmarshalJSON: unrecognized")
	}
	return uid.UnmarshalText(b[1 : size-1])
}

// String 将 Uid 转换为 base64 字符串。
func (uid Uid) String() string {
	buf, _ := uid.MarshalText()
	return string(buf)
}

// String32 将 Uid 转换为小写 base32 字符串（适用于 Windows 文件名）。
func (uid Uid) String32() string {
	data, _ := uid.MarshalBinary()
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(data))
}

// ParseUid 解析不带任何前缀的字符串。
func ParseUid(s string) Uid {
	var uid Uid
	uid.UnmarshalText([]byte(s))
	return uid
}

// ParseUid32 将 base32 编码的字符串解析为 Uid。
func ParseUid32(s string) Uid {
	var uid Uid
	if data, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s); err == nil {
		uid.UnmarshalBinary(data)
	}
	return uid
}

// UserId 将 Uid 转换为以 'usr' 为前缀的字符串，如 usrXXXXX。
func (uid Uid) UserId() string {
	return uid.PrefixId("usr")
}

// FndName 为给定 Uid 生成 'fnd' Topic 名称。
func (uid Uid) FndName() string {
	return uid.PrefixId("fnd")
}

// SlfName 为给定 Uid 生成 'slf' Topic 名称。
func (uid Uid) SlfName() string {
	return uid.PrefixId("slf")
}

// PrefixId 将 Uid 转换为带指定前缀的字符串。
func (uid Uid) PrefixId(prefix string) string {
	if uid.IsZero() {
		return ""
	}
	return prefix + uid.String()
}

// ParseUserId parses 用户 ID of the form "usrXXXXXX".
func ParseUserId(s string) Uid {
	var uid Uid
	if strings.HasPrefix(s, "usr") {
		(&uid).UnmarshalText([]byte(s)[3:])
	}
	return uid
}

// GrpToChn 将群组 Topic 名称转换为对应的频道名称。
// 如果是非群组频道 Topic，名称原样返回。
// 如果两者都不是，返回空字符串。
func GrpToChn(grp string) string {
	if strings.HasPrefix(grp, "grp") {
		return strings.Replace(grp, "grp", "chn", 1)
	}
	// 如果已经是频道则原样返回。
	if strings.HasPrefix(grp, "chn") {
		return grp
	}
	return ""
}

// IsChannel 检查给定 Topic 名称是否为频道引用。
// "nch" 不应被视为频道引用，因为它只能在创建群组 Topic 时由 Topic 所有者使用。
func IsChannel(name string) bool {
	return strings.HasPrefix(name, "chn")
}

// ChnToGrp 从频道名称获取群组 Topic 名称。
// 如果是非频道群组 Topic，名称原样返回。
// 如果两者都不是，返回空字符串。
func ChnToGrp(chn string) string {
	if strings.HasPrefix(chn, "chn") {
		return strings.Replace(chn, "chn", "grp", 1)
	}
	// 如果已经是群组则原样返回。
	if strings.HasPrefix(chn, "grp") {
		return chn
	}
	return ""
}

// UidSlice 是按升序排列的 Uid 切片。
type UidSlice []Uid

func (us UidSlice) find(uid Uid) (int, bool) {
	l := len(us)
	if l == 0 || us[0] > uid {
		return 0, false
	}
	if uid > us[l-1] {
		return l, false
	}
	idx := sort.Search(l, func(i int) bool {
		return uid <= us[i]
	})
	return idx, idx < l && us[idx] == uid
}

// Add 将 uid 添加到 UidSlice 并保持排序，重复值会被忽略。
func (us *UidSlice) Add(uid Uid) bool {
	idx, found := us.find(uid)
	if found {
		return false
	}
	// 插入操作，不创建临时切片。
	*us = append(*us, ZeroUid)
	copy((*us)[idx+1:], (*us)[idx:])
	(*us)[idx] = uid
	return true
}

// Rem 从 UidSlice 中移除 uid。
func (us *UidSlice) Rem(uid Uid) bool {
	idx, found := us.find(uid)
	if !found {
		return false
	}
	if idx == len(*us)-1 {
		*us = (*us)[:idx]
	} else {
		*us = slices.Delete((*us), idx, idx+1)
	}
	return true
}

// Contains 检查 UidSlice 是否包含给定的 UID。
func (us UidSlice) Contains(uid Uid) bool {
	_, contains := us.find(uid)
	return contains
}

// P2PName 接收两个 Uid 并生成 P2P Topic 名称。
func (uid Uid) P2PName(u2 Uid) string {
	if !uid.IsZero() && !u2.IsZero() {
		b1, _ := uid.MarshalBinary()
		b2, _ := u2.MarshalBinary()

		if uid < u2 {
			b1 = append(b1, b2...)
		} else if uid > u2 {
			b1 = append(b2, b1...)
		} else {
			// 明确禁止与自己建立 P2P
			return ""
		}

		return "p2p" + base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b1)
	}

	return ""
}

// ParseP2P 从 p2p Topic 名称中提取 uid。
func ParseP2P(p2p string) (uid1, uid2 Uid, err error) {
	if strings.HasPrefix(p2p, "p2p") {
		src := []byte(p2p)[3:]
		if len(src) != p2pBase64Unpadded {
			err = errors.New("ParseP2P: invalid length")
			return
		}
		dec := make([]byte, base64.URLEncoding.WithPadding(base64.NoPadding).DecodedLen(p2pBase64Unpadded))
		var count int
		count, err = base64.URLEncoding.WithPadding(base64.NoPadding).Decode(dec, src)
		if count < 16 {
			if err != nil {
				err = errors.New("ParseP2P: failed to decode " + err.Error())
			} else {
				err = errors.New("ParseP2P: invalid decoded length")
			}
			return
		}
		uid1 = Uid(binary.LittleEndian.Uint64(dec))
		uid2 = Uid(binary.LittleEndian.Uint64(dec[8:]))
	} else {
		err = errors.New("ParseP2P: missing or invalid prefix")
	}
	return
}

// P2PNameForUser takes a 用户 ID and a full name of a P2P Topic and generates the name of the
// P2P Topic as it should be seen by the given 用户.
func P2PNameForUser(uid Uid, p2p string) (string, error) {
	uid1, uid2, err := ParseP2P(p2p)
	if err != nil {
		return "", err
	}
	if uid.Compare(uid1) == 0 {
		return uid2.UserId(), nil
	}
	return uid1.UserId(), nil
}
