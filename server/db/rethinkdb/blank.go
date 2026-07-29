//go:build !rethinkdb
// +build !rethinkdb

// 此文件用于条件编译。当未定义 'rethinkdb' 构建标签时使用。
// 否则编译 adapter.go。

// Package rethinkdb 提供数据库持久化、迁移或测试支持。
package rethinkdb
