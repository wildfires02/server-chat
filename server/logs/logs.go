// Package logs 提供基于 Uber Zap 的高级日志库，包含控制台输出、结构化日志以及日志切割归档功能。
package logs

import (
	"fmt"
	"io"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// ZapLogger 封装 zap.SugaredLogger，提供与 log.Logger 兼容的方法接口 (Println, Printf, Fatal, Fatalf, Fatalln, Panicln 等)。
type ZapLogger struct {
	// sugar 保存sugar。
	sugar *zap.SugaredLogger
	// level 保存level。
	level zapcore.Level
}

// newZapLogger 创建并初始化ZapLogger。
func newZapLogger(sugar *zap.SugaredLogger, level zapcore.Level) *ZapLogger {
	return &ZapLogger{
		sugar: sugar.WithOptions(zap.AddCallerSkip(1)),
		level: level,
	}
}

// Println 输出指定日志级别的换行格式化日志。
func (l *ZapLogger) Println(args ...any) {
	msg := strings.TrimSuffix(fmt.Sprintln(args...), "\n")
	l.sugar.Log(l.level, msg)
}

// Printf 输出指定日志级别的格式化日志。
func (l *ZapLogger) Printf(template string, args ...any) {
	l.sugar.Logf(l.level, template, args...)
}

// Print 输出指定日志级别的无换行格式化日志。
func (l *ZapLogger) Print(args ...any) {
	msg := fmt.Sprint(args...)
	l.sugar.Log(l.level, msg)
}

// Fatal 输出 Fatal 级别日志并退出程序 (os.Exit(1))。
func (l *ZapLogger) Fatal(args ...any) {
	msg := fmt.Sprint(args...)
	l.sugar.Fatal(msg)
}

// Fatalf 输出 Fatal 级别的格式化日志并退出程序。
func (l *ZapLogger) Fatalf(template string, args ...any) {
	l.sugar.Fatalf(template, args...)
}

// Fatalln 输出 Fatal 级别的换行格式化日志并退出程序。
func (l *ZapLogger) Fatalln(args ...any) {
	msg := strings.TrimSuffix(fmt.Sprintln(args...), "\n")
	l.sugar.Fatal(msg)
}

// Panic 输出 Panic 级别日志并触发 panic。
func (l *ZapLogger) Panic(args ...any) {
	msg := fmt.Sprint(args...)
	l.sugar.Panic(msg)
}

// Panicf 输出 Panic 级别的格式化日志并触发 panic。
func (l *ZapLogger) Panicf(template string, args ...any) {
	l.sugar.Panicf(template, args...)
}

// Panicln 输出 Panic 级别的换行格式化日志并触发 panic。
func (l *ZapLogger) Panicln(args ...any) {
	msg := strings.TrimSuffix(fmt.Sprintln(args...), "\n")
	l.sugar.Panic(msg)
}

var (
	// Logger 为全局 Zap 结构化日志实例。
	Logger *zap.Logger
	// Sugar 为全局 Zap 格式化日志实例 (SugaredLogger)。
	Sugar *zap.SugaredLogger

	// Info 为 Zap 原生 Info 级别日志句柄（兼容 Println/Printf/Fatal 等调用）。
	Info *ZapLogger
	// Warn 为 Zap 原生 Warn 级别日志句柄（兼容 Println/Printf/Fatal 等调用）。
	Warn *ZapLogger
	// Err 为 Zap 原生 Error 级别日志句柄（兼容 Println/Printf/Fatal 等调用）。
	Err *ZapLogger
)

// RotateConfig 定义日志文件归档切割的相关配置项。
type RotateConfig struct {
	Filename   string `json:"filename"`    // 日志输出文件路径，例如 "./logs/im.log"
	MaxSize    int    `json:"max_size"`    // 单个日志文件最大容量（单位: MB），默认 100MB
	MaxBackups int    `json:"max_backups"` // 旧日志文件最大保留份数，默认 30 份
	MaxAge     int    `json:"max_age"`     // 旧日志文件最大保留天数（单位: 天），默认 7 天
	Compress   bool   `json:"compress"`    // 是否对历史日志归档文件进行 gzip 压缩，默认 true
	LocalTime  bool   `json:"local_time"`  // 归档文件名是否使用本地时间戳，默认 true
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	// 初始化默认兜底日志对象，避免包级别变量为 nil
	Init(os.Stderr, "stdFlags")
}

// L 获取全局 Zap Logger 实例
func L() *zap.Logger {
	return Logger
}

// S 获取全局 Zap SugaredLogger 实例
func S() *zap.SugaredLogger {
	return Sugar
}

// Sync 刷新并同步日志缓冲区中的数据
func Sync() error {
	if Logger != nil {
		return Logger.Sync()
	}
	return nil
}

// Init 初始化日志组件（仅输出到控制台或指定 Writer）
func Init(output io.Writer, logFlags string) {
	InitWithRotate(output, logFlags, nil)
}

// InitWithRotate 初始化日志组件，支持控制台输出与日志切割归档配置
func InitWithRotate(output io.Writer, logFlags string, rotate *RotateConfig) {
	if output == nil && (rotate == nil || rotate.Filename == "") {
		output = os.Stderr
	}

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalColorLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	if strings.Contains(logFlags, "nocolor") {
		encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	}

	var syncers []zapcore.WriteSyncer

	if output != nil {
		syncers = append(syncers, zapcore.AddSync(output))
	}

	if rotate != nil && rotate.Filename != "" {
		maxSize := rotate.MaxSize
		if maxSize <= 0 {
			maxSize = 100 // 默认 100 MB
		}
		maxBackups := rotate.MaxBackups
		if maxBackups <= 0 {
			maxBackups = 30 // 默认保留 30 个历史文件
		}
		maxAge := rotate.MaxAge
		if maxAge <= 0 {
			maxAge = 7 // 默认保留 7 天
		}

		lj := &lumberjack.Logger{
			Filename:   rotate.Filename,
			MaxSize:    maxSize,
			MaxBackups: maxBackups,
			MaxAge:     maxAge,
			Compress:   rotate.Compress,
			LocalTime:  rotate.LocalTime,
		}
		syncers = append(syncers, zapcore.AddSync(lj))
	}

	syncer := zapcore.NewMultiWriteSyncer(syncers...)
	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderConfig),
		syncer,
		zap.DebugLevel,
	)

	Logger = zap.New(core, zap.AddCaller())
	Sugar = Logger.Sugar()

	Info = newZapLogger(Sugar, zapcore.InfoLevel)
	Warn = newZapLogger(Sugar, zapcore.WarnLevel)
	Err = newZapLogger(Sugar, zapcore.ErrorLevel)
}

// 快捷调用的格式化日志输出函数
func Debugf(template string, args ...any) { Sugar.Debugf(template, args...) }

// Infof 完成Infof所需的内部处理。
func Infof(template string, args ...any) { Sugar.Infof(template, args...) }

// Warnf 完成Warnf所需的内部处理。
func Warnf(template string, args ...any) { Sugar.Warnf(template, args...) }

// Errorf 完成Errorf所需的内部处理。
func Errorf(template string, args ...any) { Sugar.Errorf(template, args...) }

// Fatalf 完成Fatalf所需的内部处理。
func Fatalf(template string, args ...any) { Sugar.Fatalf(template, args...) }
