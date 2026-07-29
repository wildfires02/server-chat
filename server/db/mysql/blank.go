//go:build !mysql
// +build !mysql

// 此文件用于条件编译。当未定义 'mysql' 构建标签时使用。
// 否则编译 adapter.go。

// Package mysql 提供数据库持久化、迁移或测试支持。
package mysql
