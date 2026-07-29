// Package logs 实现即时通信服务端的协议、路由和业务逻辑。
package logs

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestZapLogs 测试 Zap 基础日志与兼容句柄输出
func TestZapLogs(t *testing.T) {
	buf := new(bytes.Buffer)
	Init(buf, "stdFlags")

	if L() == nil {
		t.Fatal("L() 返回了 nil")
	}
	if S() == nil {
		t.Fatal("S() 返回了 nil")
	}

	tests := []struct {
		name    string
		logFunc func()
	}{
		{
			name:    "Info.Println",
			logFunc: func() { Info.Println("测试标准库 Info 级别日志") },
		},
		{
			name:    "Warn.Println",
			logFunc: func() { Warn.Println("测试标准库 Warn 级别日志") },
		},
		{
			name:    "Err.Println",
			logFunc: func() { Err.Println("测试标准库 Error 级别日志") },
		},
		{
			name:    "Infof",
			logFunc: func() { Infof("测试 %s 级别格式化日志", "Info") },
		},
		{
			name:    "Warnf",
			logFunc: func() { Warnf("测试 %s 级别格式化日志", "Warn") },
		},
		{
			name:    "Errorf",
			logFunc: func() { Errorf("测试 %s 级别格式化日志", "Error") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()
			tt.logFunc()
			_ = Sync()
			if buf.Len() == 0 {
				t.Errorf("%s: 预期日志输出到缓冲区，但实际内容为空", tt.name)
			}
		})
	}
}

// TestLogRotation 测试日志文件归档与自动切割功能
func TestLogRotation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "zap_log_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name      string
		filename  string
		logAction func()
	}{
		{
			name:     "Standard Rotate Logger",
			filename: filepath.Join(tmpDir, "test_rotate.log"),
			logAction: func() {
				Infof("测试日志切割输出 1")
				Warnf("测试日志切割输出 2")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rotateCfg := &RotateConfig{
				Filename:   tt.filename,
				MaxSize:    1,
				MaxBackups: 3,
				MaxAge:     1,
				Compress:   false,
				LocalTime:  true,
			}

			buf := new(bytes.Buffer)
			InitWithRotate(buf, "stdFlags", rotateCfg)

			tt.logAction()
			_ = Sync()

			content, err := os.ReadFile(tt.filename)
			if err != nil {
				t.Fatalf("读取切割日志文件失败: %v", err)
			}

			if len(content) == 0 {
				t.Errorf("预期日志文件 %s 写入内容，但实际为空", tt.filename)
			}
		})
	}
}
