//go:build !mysql && (postgres || mongodb || rethinkdb)
// +build !mysql
// +build postgres mongodb rethinkdb

// 此文件用于条件编译。显式选择其它数据库且未选择 MySQL 时使用；
// 未提供任何数据库构建标签时，MySQL 是默认适配器。

// Package mysql 提供数据库持久化、迁移或测试支持。
package mysql
