// Package ringhash_test 实现即时通信服务端的协议、路由和业务逻辑。
package ringhash_test

import (
	"fmt"
	"hash/crc32"
	"hash/fnv"
	"testing"

	"chat/server/ringhash"
)

// TestHashing 验证 Hashing 相关行为。
func TestHashing(t *testing.T) {
	ring := ringhash.New(3, crc32.ChecksumIEEE)
	ring.Add("A", "B", "C")

	tests := []struct {
		name     string
		key      string
		expected string
	}{
		{name: "Key A mapping", key: "A", expected: "B"},
		{name: "Key B mapping", key: "B", expected: "C"},
		{name: "Key C mapping", key: "C", expected: "C"},
		{name: "Key D mapping", key: "D", expected: "A"},
		{name: "Key E mapping", key: "E", expected: "B"},
		{name: "Key F mapping", key: "F", expected: "C"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ring.Get(tt.key)
			if got != tt.expected {
				t.Errorf("Key '%s': expected '%s', got '%s'", tt.key, tt.expected, got)
			}
		})
	}

	ring.Add("X")

	testsAfterAdd := []struct {
		name     string
		key      string
		expected string
	}{
		{name: "Key A mapping after Add X", key: "A", expected: "X"},
		{name: "Key B mapping after Add X", key: "B", expected: "C"},
		{name: "Key C mapping after Add X", key: "C", expected: "C"},
		{name: "Key D mapping after Add X", key: "D", expected: "A"},
		{name: "Key E mapping after Add X", key: "E", expected: "X"},
		{name: "Key F mapping after Add X", key: "F", expected: "C"},
	}

	for _, tt := range testsAfterAdd {
		t.Run(tt.name, func(t *testing.T) {
			got := ring.Get(tt.key)
			if got != tt.expected {
				t.Errorf("Key '%s': expected '%s', got '%s'", tt.key, tt.expected, got)
			}
		})
	}
}

// TestConsistency 验证 Consistency 相关行为。
func TestConsistency(t *testing.T) {
	t.Run("Basic consistency across node insertion order", func(t *testing.T) {
		ring1 := ringhash.New(3, nil)
		ring2 := ringhash.New(3, nil)

		ring1.Add("owl", "crow", "sparrow")
		ring2.Add("sparrow", "owl", "crow")

		if ring1.Get("duck") != ring2.Get("duck") {
			t.Error("'duck' 在两种情况下都应映射到 'sparrow'")
		}
	})

	t.Run("Collision consistency check", func(t *testing.T) {
		ring1 := ringhash.New(1, crc32.ChecksumIEEE)
		ring2 := ringhash.New(1, crc32.ChecksumIEEE)

		ring1.Add("VXGD", "BGABAA", "VXGG", "BGABAB", "VXGF", "BGABAC")
		ring2.Add("BGABAA", "VXGD", "BGABAB", "VXGG", "BGABAC", "VXGF")

		str := []string{
			"datsam", "kGmVht", "dSPmEr", "RloWQr", "WFkAkG", "gLBNPX", "twEwll", "RnRdaf",
			"ruEMuJ", "ZvXJsJ", "xjQzKD", "CKfSFg", "BMKMvM", "PSzYdC", "CsxqTR", "IbzdXz",
			"xdnZGj", "VdHcVp", "iVgIvH", "bZsTIX", "CyRBUO", "ylgEGS", "vOTwJD", "JZbyFU",
			"Hayrly", "jQQkOV", "NEVjlJ", "SkJfie", "HrdJuL", "ASwkXH", "UwJOmo", "nfbrxA",
		}

		for _, key := range str {
			if ring1.Get(key) != ring2.Get(key) {
				t.Errorf("'%s' 在两种情况下应映射到同一个桶", key)
			}
		}
	})
}

// TestSignature 验证 Signature 相关行为。
func TestSignature(t *testing.T) {
	fnvHashfunc := func(data []byte) uint32 {
		hash := fnv.New32a()
		hash.Write(data)
		return hash.Sum32()
	}

	tests := []struct {
		name        string
		setupRings  func() (ring1, ring2 *ringhash.Ring)
		expectEqual bool
	}{
		{
			name: "Same elements different insertion order",
			setupRings: func() (*ringhash.Ring, *ringhash.Ring) {
				r1 := ringhash.New(4, nil)
				r2 := ringhash.New(4, nil)
				r1.Add("owl", "crow", "sparrow")
				r2.Add("sparrow", "owl", "crow")
				return r1, r2
			},
			expectEqual: true,
		},
		{
			name: "Different replicas count",
			setupRings: func() (*ringhash.Ring, *ringhash.Ring) {
				r1 := ringhash.New(4, nil)
				r2 := ringhash.New(5, nil)
				r1.Add("owl", "crow", "sparrow")
				r2.Add("owl", "crow", "sparrow")
				return r1, r2
			},
			expectEqual: false,
		},
		{
			name: "Different keys",
			setupRings: func() (*ringhash.Ring, *ringhash.Ring) {
				r1 := ringhash.New(4, nil)
				r2 := ringhash.New(4, nil)
				r1.Add("owl", "crow", "sparrow")
				r2.Add("owl", "crow", "sparrow", "crane")
				return r1, r2
			},
			expectEqual: false,
		},
		{
			name: "Different hash functions",
			setupRings: func() (*ringhash.Ring, *ringhash.Ring) {
				r1 := ringhash.New(4, nil)
				r2 := ringhash.New(4, fnvHashfunc)
				r1.Add("owl", "crow", "sparrow")
				r2.Add("owl", "crow", "sparrow")
				return r1, r2
			},
			expectEqual: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r1, r2 := tt.setupRings()
			equal := (r1.Signature() == r2.Signature())
			if equal != tt.expectEqual {
				t.Errorf("Signature comparison: got %v, expected %v", equal, tt.expectEqual)
			}
		})
	}
}

// TestDefaultHashMatchesIEEE 验证默认的字符串零分配路径与标准 IEEE CRC32 完全一致。
func TestDefaultHashMatchesIEEE(t *testing.T) {
	defaultRing := ringhash.New(256, nil)
	standardRing := ringhash.New(256, crc32.ChecksumIEEE)
	nodes := []string{"im-0", "im-1", "im-2", "im-3", "im-4"}
	defaultRing.Add(nodes...)
	standardRing.Add(nodes...)

	if defaultRing.Signature() != standardRing.Signature() {
		t.Fatal("默认字符串 CRC32 与标准 IEEE CRC32 生成了不同 Ring")
	}
	for index := range 10_000 {
		topic := fmt.Sprintf("grp-ieee-%d", index)
		if defaultRing.Get(topic) != standardRing.Get(topic) {
			t.Fatalf("Topic %q 的默认 Owner 与标准 IEEE CRC32 不一致", topic)
		}
	}
}

// BenchmarkGet8 衡量 Get 8 相关操作的性能。
func BenchmarkGet8(b *testing.B) { benchmarkGet(b, 8) }

// BenchmarkGet32 衡量 Get 32 相关操作的性能。
func BenchmarkGet32(b *testing.B) { benchmarkGet(b, 32) }

// BenchmarkGet128 衡量 Get 128 相关操作的性能。
func BenchmarkGet128(b *testing.B) { benchmarkGet(b, 128) }

// BenchmarkGet512 衡量 Get 512 相关操作的性能。
func BenchmarkGet512(b *testing.B) { benchmarkGet(b, 512) }

// benchmarkGet 完成benchmarkGet所需的内部处理。
func benchmarkGet(b *testing.B, keycount int) {
	ring := ringhash.New(53, nil)

	var ids []string
	for i := range keycount {
		ids = append(ids, fmt.Sprintf("id=%d", i))
	}

	ring.Add(ids...)

	b.ResetTimer()

	for i := range b.N {
		ring.Get(ids[i&(keycount-1)])
	}
}
