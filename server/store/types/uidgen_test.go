package types

import (
	"encoding/base64"
	"testing"
	"time"
)

// TestUidGeneratorInit 测试 UidGenerator 的初始化逻辑与重入幂等性
func TestUidGeneratorInit(t *testing.T) {
	key := []byte("testkey1testkey2")

	tests := []struct {
		name     string
		workerID uint
		key      []byte
		wantErr  bool
	}{
		{name: "Valid initialization worker 1", workerID: 1, key: key, wantErr: false},
		{name: "Valid initialization worker 2", workerID: 2, key: key, wantErr: false},
		{name: "Re-initialization worker 3", workerID: 3, key: key, wantErr: false},
	}

	ug := &UidGenerator{}
	var oldSeq any
	var oldCipher any

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ug.Init(tt.workerID, tt.key)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Init() error = %v, wantErr %v", err, tt.wantErr)
			}
			if i == 0 {
				if ug.seq == nil {
					t.Error("Snowflake 发生器应当完成初始化")
				}
				if ug.cipher == nil {
					t.Error("XTEA 密码器应当完成初始化")
				}
				oldSeq = ug.seq
				oldCipher = ug.cipher
			} else {
				if ug.seq != oldSeq {
					t.Error("Snowflake 发生器不应重新初始化")
				}
				if ug.cipher != oldCipher {
					t.Error("XTEA 密码器不应重新初始化")
				}
			}
		})
	}
}

// TestUidGeneratorInitWithInvalidKey 测试使用无效密钥初始化的情况
func TestUidGeneratorInitWithInvalidKey(t *testing.T) {
	tests := []struct {
		name    string
		key     []byte
		wantErr bool
	}{
		{name: "Short key", key: []byte("short"), wantErr: true},
		{name: "Nil key", key: nil, wantErr: true},
		{name: "15-byte key", key: []byte("testkey1testkey"), wantErr: true},
		{name: "17-byte key", key: []byte("testkey1testkey22"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ug := &UidGenerator{}
			err := ug.Init(1, tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("Init() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestUidGeneratorGet 测试生成唯一 Uid 的功能
func TestUidGeneratorGet(t *testing.T) {
	t.Run("Initialized generator", func(t *testing.T) {
		ug := &UidGenerator{}
		key := []byte("testkey1testkey2")
		if err := ug.Init(1, key); err != nil {
			t.Fatalf("初始化生成器失败: %v", err)
		}

		uid1 := ug.Get()
		uid2 := ug.Get()
		if uid1 == ZeroUid || uid2 == ZeroUid {
			t.Error("生成的 UID 不应为 ZeroUid")
		}
		if uid1 == uid2 {
			t.Error("生成的 UID 应当唯一")
		}

		uids := make(map[Uid]bool)
		for i := 0; i < 1000; i++ {
			uid := ug.Get()
			if uid == ZeroUid {
				t.Errorf("第 %d 个 UID 不应为零", i)
			}
			if uids[uid] {
				t.Errorf("生成了重复的 UID: %v", uid)
			}
			uids[uid] = true
		}
	})

	t.Run("Uninitialized generator", func(t *testing.T) {
		ug := &UidGenerator{}
		if uid := ug.Get(); uid != ZeroUid {
			t.Error("未初始化的生成器预期返回 ZeroUid")
		}
	})
}

// TestUidGeneratorGetStr 测试生成 Base64 格式字符串 UID
func TestUidGeneratorGetStr(t *testing.T) {
	t.Run("Initialized generator", func(t *testing.T) {
		ug := &UidGenerator{}
		key := []byte("testkey1testkey2")
		if err := ug.Init(1, key); err != nil {
			t.Fatalf("初始化生成器失败: %v", err)
		}

		uidStr1 := ug.GetStr()
		uidStr2 := ug.GetStr()

		if uidStr1 == "" || uidStr2 == "" {
			t.Error("生成的 UID 字符串不应为空")
		}
		if uidStr1 == uidStr2 {
			t.Error("生成的 UID 字符串应当唯一")
		}

		decoded, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(uidStr1)
		if err != nil {
			t.Errorf("生成的 UID 字符串应为有效的 Base64: %v", err)
		}
		if len(decoded) != 8 {
			t.Errorf("解码后的 UID 应为 8 字节，实际为 %d 字节", len(decoded))
		}
	})

	t.Run("Uninitialized generator", func(t *testing.T) {
		ug := &UidGenerator{}
		if uidStr := ug.GetStr(); uidStr != "" {
			t.Error("未初始化的生成器预期返回空字符串")
		}
	})
}

// TestUidGeneratorDecodeUid 测试解密还原 Uid 为原始 int64 数字
func TestUidGeneratorDecodeUid(t *testing.T) {
	ug := &UidGenerator{}
	key := []byte("testkey1testkey2")
	if err := ug.Init(1, key); err != nil {
		t.Fatalf("初始化生成器失败: %v", err)
	}

	uid1 := ug.Get()
	uid2 := ug.Get()

	tests := []struct {
		name string
		uid  Uid
	}{
		{name: "Decode UID 1", uid: uid1},
		{name: "Decode UID 2", uid: uid2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoded := ug.DecodeUid(tt.uid)
			if decoded < 0 {
				t.Errorf("解密后的 UID 应当为非负数，实际为 %d", decoded)
			}
		})
	}

	if ug.DecodeUid(uid1) == ug.DecodeUid(uid2) {
		t.Error("不同的 UID 应解密得到不同的原始数值")
	}
}

// TestUidGeneratorEncodeInt64 测试将正整数 int64 加密为 Uid
func TestUidGeneratorEncodeInt64(t *testing.T) {
	ug := &UidGenerator{}
	key := []byte("testkey1testkey2")
	if err := ug.Init(1, key); err != nil {
		t.Fatalf("初始化生成器失败: %v", err)
	}

	tests := []struct {
		name  string
		value int64
	}{
		{name: "Zero value", value: 0},
		{name: "Small integer", value: 12345},
		{name: "Medium integer", value: 67890},
		{name: "Max int64", value: 9223372036854775807},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid := ug.EncodeInt64(tt.value)
			if uid == ZeroUid {
				t.Errorf("加密 %d 后的 UID 不应为 ZeroUid", tt.value)
			}
		})
	}

	uid1 := ug.EncodeInt64(12345)
	uid2 := ug.EncodeInt64(67890)
	if uid1 == uid2 {
		t.Error("不同数值加密后应为不同的 UID")
	}
}

// TestUidGeneratorEncodeDecodeRoundtrip 测试编码-解码双向转换一致性
func TestUidGeneratorEncodeDecodeRoundtrip(t *testing.T) {
	ug := &UidGenerator{}
	key := []byte("testkey1testkey2")
	if err := ug.Init(1, key); err != nil {
		t.Fatalf("初始化生成器失败: %v", err)
	}

	testValues := []struct {
		name  string
		value int64
	}{
		{name: "Value 0", value: 0},
		{name: "Value 1", value: 1},
		{name: "Value 42", value: 42},
		{name: "Value 12345", value: 12345},
		{name: "Value 1000000", value: 1000000},
		{name: "Value max int64", value: 9223372036854775807},
	}

	for _, tt := range testValues {
		t.Run(tt.name, func(t *testing.T) {
			encoded := ug.EncodeInt64(tt.value)
			decoded := ug.DecodeUid(encoded)
			if decoded != tt.value {
				t.Errorf("数值 %d 双向编解码还原失败: 实际得到 %d", tt.value, decoded)
			}
		})
	}

	t.Run("Generated UID roundtrip", func(t *testing.T) {
		uid := ug.Get()
		decoded := ug.DecodeUid(uid)
		reencoded := ug.EncodeInt64(decoded)
		if reencoded != uid {
			t.Error("生成的 UID 双向编解码还原失败")
		}
	})
}

// TestUidGeneratorConcurrency 测试并发环境下的 UID 生成唯一性
func TestUidGeneratorConcurrency(t *testing.T) {
	ug := &UidGenerator{}
	key := []byte("testkey1testkey2")
	err := ug.Init(1, key)
	if err != nil {
		t.Fatalf("初始化生成器失败: %v", err)
	}

	const numGoroutines = 10
	const uidsPerGoroutine = 100

	uidChan := make(chan Uid, numGoroutines*uidsPerGoroutine)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			for j := 0; j < uidsPerGoroutine; j++ {
				uid := ug.Get()
				uidChan <- uid
			}
		}()
	}

	uids := make(map[Uid]bool)
	for i := 0; i < numGoroutines*uidsPerGoroutine; i++ {
		uid := <-uidChan
		if uid == ZeroUid {
			t.Error("并发生成的 UID 不应为零")
		}
		if uids[uid] {
			t.Errorf("并发测试中生成了重复的 UID: %v", uid)
		}
		uids[uid] = true
	}

	if len(uids) != numGoroutines*uidsPerGoroutine {
		t.Errorf("预期 %d 个唯一 UID，实际得到 %d", numGoroutines*uidsPerGoroutine, len(uids))
	}
}

// TestUidGeneratorPerformance 测试 UID 生成性能吞吐量
func TestUidGeneratorPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过短时间测试模式下的性能测试")
	}

	ug := &UidGenerator{}
	key := []byte("testkey1testkey2")
	err := ug.Init(1, key)
	if err != nil {
		t.Fatalf("初始化生成器失败: %v", err)
	}

	start := time.Now()
	for i := 0; i < 100000; i++ {
		uid := ug.Get()
		if uid == ZeroUid {
			t.Error("生成的 UID 不应为零")
		}
	}
	duration := time.Since(start)
	t.Logf("生成 100,000 个二进制 UID 耗时 %v (约 %.0f UIDs/秒)", duration, 100000/duration.Seconds())

	start = time.Now()
	for i := 0; i < 100000; i++ {
		uidStr := ug.GetStr()
		if uidStr == "" {
			t.Error("生成的 UID 字符串不应为空")
		}
	}
	duration = time.Since(start)
	t.Logf("生成 100,000 个 Base64 UID 字符串耗时 %v (约 %.0f UIDs/秒)", duration, 100000/duration.Seconds())
}

// TestUidGeneratorDifferentWorkerIds 测试不同 Worker ID 生成结果的独立性
func TestUidGeneratorDifferentWorkerIds(t *testing.T) {
	key := []byte("testkey1testkey2")

	ug1 := &UidGenerator{}
	ug2 := &UidGenerator{}

	err1 := ug1.Init(1, key)
	err2 := ug2.Init(2, key)

	if err1 != nil || err2 != nil {
		t.Fatalf("初始化生成器失败: %v, %v", err1, err2)
	}

	uids1 := make([]Uid, 100)
	uids2 := make([]Uid, 100)

	for i := 0; i < 100; i++ {
		uids1[i] = ug1.Get()
		uids2[i] = ug2.Get()
	}

	allUids := make(map[Uid]bool)
	for _, uid := range uids1 {
		if allUids[uid] {
			t.Error("不同生成器之间发现重复的 UID")
		}
		allUids[uid] = true
	}
	for _, uid := range uids2 {
		if allUids[uid] {
			t.Error("不同生成器之间发现重复的 UID")
		}
		allUids[uid] = true
	}
}

func TestUidGeneratorInitErrorConditions(t *testing.T) {
	tests := []struct {
		name     string
		workerID uint
	}{
		{name: "worker ID 1024", workerID: 1024},
		{name: "max uint32 worker ID", workerID: 4294967295},
	}

	key := []byte("testkey1testkey2")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ug := &UidGenerator{}
			err := ug.Init(tt.workerID, key)
			if err == nil {
				t.Errorf("对于 Worker ID %d 预期报错，实际成功", tt.workerID)
			}
		})
	}
}

func TestUidGeneratorInitKeyValidation(t *testing.T) {
	testCases := []struct {
		name string
		key  []byte
	}{
		{"nil key", nil},
		{"empty key", []byte{}},
		{"too short key", []byte("short")},
		{"15 byte key", []byte("testkey1testkey")},
		{"17 byte key", []byte("testkey1testkey22")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ug := &UidGenerator{}
			err := ug.Init(1, tc.key)
			if err == nil {
				t.Errorf("对于 %s 预期报错，但未报错", tc.name)
			}
		})
	}
}

func TestUidGeneratorInitValidKeys(t *testing.T) {
	validKeys := []struct {
		name string
		key  []byte
	}{
		{name: "16-byte key ASCII 1", key: []byte("testkey1testkey2")},
		{name: "16-byte key ASCII 2", key: []byte("1234567890123456")},
		{name: "16-byte key ASCII 3", key: []byte("abcdefghijklmnop")},
		{name: "16-byte key bytes", key: []byte("\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f")},
	}

	for i, tc := range validKeys {
		t.Run(tc.name, func(t *testing.T) {
			ug := &UidGenerator{}
			err := ug.Init(uint(i), tc.key)
			if err != nil {
				t.Errorf("有效密钥 %s 应当正常工作: %v", tc.name, err)
			}
		})
	}
}

func TestUidGeneratorInitPartialFailure(t *testing.T) {
	ug := &UidGenerator{}

	validKey := []byte("testkey1testkey2")
	err := ug.Init(1, validKey)
	if err != nil {
		t.Fatalf("初始设置应当成功: %v", err)
	}

	oldSeq := ug.seq
	err = ug.Init(1, []byte("short"))

	if ug.seq != oldSeq {
		t.Error("在部分失败时不应重新初始化 Snowflake")
	}

	if err != nil {
		t.Logf("符合预期: 部分初始化校验失败: %v", err)
	}
}

func TestUidGeneratorInitMultipleWorkers(t *testing.T) {
	key := []byte("testkey1testkey2")
	generators := make([]*UidGenerator, 10)

	for i := 0; i < 10; i++ {
		generators[i] = &UidGenerator{}
		err := generators[i].Init(uint(i), key)
		if err != nil {
			t.Errorf("Worker %d 初始化失败: %v", i, err)
		}
	}

	uids := make(map[Uid]int)
	for i, gen := range generators {
		for j := 0; j < 10; j++ {
			uid := gen.Get()
			if uid == ZeroUid {
				t.Errorf("Worker %d 生成了 ZeroUid", i)
				continue
			}
			if existingWorker, exists := uids[uid]; exists {
				t.Errorf("Worker %d 和 Worker %d 生成了重复的 UID %v", i, existingWorker, uid)
			}
			uids[uid] = i
		}
	}
}

func TestUidGeneratorInitIdempotency(t *testing.T) {
	ug := &UidGenerator{}
	key := []byte("testkey1testkey2")

	err1 := ug.Init(1, key)
	if err1 != nil {
		t.Fatalf("第一次初始化失败: %v", err1)
	}

	seq1 := ug.seq
	cipher1 := ug.cipher

	err2 := ug.Init(1, key)
	if err2 != nil {
		t.Errorf("第二次初始化不应失败: %v", err2)
	}

	if ug.seq != seq1 {
		t.Error("Snowflake 不应重复初始化")
	}
	if ug.cipher != cipher1 {
		t.Error("Cipher 不应重复初始化")
	}

	err3 := ug.Init(2, key)
	if err3 != nil {
		t.Errorf("第三次初始化不应失败: %v", err3)
	}

	if ug.seq != seq1 {
		t.Error("即使 worker ID 改变也不应重新初始化")
	}
	if ug.cipher != cipher1 {
		t.Error("即使 worker ID 改变也不应重新初始化")
	}
}

func TestUidGeneratorInitConcurrent(t *testing.T) {
	const numGoroutines = 20
	key := []byte("testkey1testkey2")

	ug := &UidGenerator{}
	errChan := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(workerID uint) {
			err := ug.Init(workerID, key)
			errChan <- err
		}(uint(i))
	}

	var errors []error
	for i := 0; i < numGoroutines; i++ {
		if err := <-errChan; err != nil {
			errors = append(errors, err)
		}
	}

	if ug.seq == nil {
		t.Error("并发初始化后 Snowflake 应当就绪")
	}
	if ug.cipher == nil {
		t.Error("并发初始化后 Cipher 应当就绪")
	}

	uid := ug.Get()
	if uid == ZeroUid {
		t.Error("并发初始化后应当能够正常生成 UID")
	}

	if len(errors) > 0 {
		t.Logf("部分并发初始化遇到错误（符合预期）: %v", errors)
	}
}

func TestUidGeneratorInitBoundaryWorkerIDs(t *testing.T) {
	key := []byte("testkey1testkey2")

	testCases := []struct {
		name     string
		workerID uint
		expect   bool
	}{
		{"zero worker ID", 0, true},
		{"worker ID 1", 1, true},
		{"worker ID 1023", 1023, true},
		{"worker ID 1024", 1024, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ug := &UidGenerator{}
			err := ug.Init(tc.workerID, key)

			if tc.expect && err != nil {
				t.Errorf("对于 Worker ID %d 预期成功，实际报错: %v", tc.workerID, err)
			} else if !tc.expect && err == nil {
				t.Errorf("对于 Worker ID %d 预期报错，实际成功", tc.workerID)
			}

			if err == nil {
				uid := ug.Get()
				if uid == ZeroUid {
					t.Errorf("Worker ID %d 的生成器应当能生成合法 UID", tc.workerID)
				}
			}
		})
	}
}

func BenchmarkUidGeneratorGet(b *testing.B) {
	ug := &UidGenerator{}
	key := []byte("testkey1testkey2")
	err := ug.Init(1, key)
	if err != nil {
		b.Fatalf("初始化生成器失败: %v", err)
	}

	for b.Loop() {
		_ = ug.Get()
	}
}

func BenchmarkUidGeneratorGetStr(b *testing.B) {
	ug := &UidGenerator{}
	key := []byte("testkey1testkey2")
	err := ug.Init(1, key)
	if err != nil {
		b.Fatalf("初始化生成器失败: %v", err)
	}

	for b.Loop() {
		_ = ug.GetStr()
	}
}

func BenchmarkUidGeneratorDecodeUid(b *testing.B) {
	ug := &UidGenerator{}
	key := []byte("testkey1testkey2")
	err := ug.Init(1, key)
	if err != nil {
		b.Fatalf("初始化生成器失败: %v", err)
	}

	uid := ug.Get()

	for b.Loop() {
		_ = ug.DecodeUid(uid)
	}
}

func BenchmarkUidGeneratorEncodeInt64(b *testing.B) {
	ug := &UidGenerator{}
	key := []byte("testkey1testkey2")
	err := ug.Init(1, key)
	if err != nil {
		b.Fatalf("初始化生成器失败: %v", err)
	}

	val := int64(12345)

	for b.Loop() {
		_ = ug.EncodeInt64(val)
	}
}
