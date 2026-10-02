package tsh

import "errors"

// 表管理相关的哨兵错误。
//
// 它们都会被 HTTP 层映射成合适的状态码：表不存在 → 404，
// 表已存在 → 409，其余是 400。让调用方能做程序化判断，
// 而不是去解析错误文案。
var (
	// ErrTableNotFound 表示表不存在。
	//
	// 刻意**不**自动建表：拼错表名会静默产生一张空表，
	// 在导入管道里极难发现——数据写进去了，只是写到了别处。
	ErrTableNotFound = errors.New("tsh: 表不存在")

	// ErrTableExists 表示表已存在。
	//
	// CreateTable 遇到已存在的表返回它而不是静默成功：
	// 「以为新建了一张表，其实在往旧表里写」是很严重的误解。
	ErrTableExists = errors.New("tsh: 表已存在")

	// ErrTooManyTables 表示表数量已达上限。
	ErrTooManyTables = errors.New("tsh: 表数量已达上限")

	// ErrCannotDropDefault 表示试图删除默认表。
	ErrCannotDropDefault = errors.New("tsh: 默认表不能删除")

	// ErrEngineClosed 表示引擎已关闭，不再接受建表。
	ErrEngineClosed = errors.New("tsh: 引擎已关闭")
)
