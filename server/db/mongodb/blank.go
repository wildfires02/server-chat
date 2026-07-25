//go:build !mongodb
// +build !mongodb

// 此文件用于条件编译。当未定义 'mongodb' 构建标签时使用。
// 否则编译 adapter.go。
package mongodb
