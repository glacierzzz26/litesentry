package store

import "strings"

// Open 根据 DSN 前缀选择后端：
//
//	sqlite:///path/litesentry.db（默认）
//	postgres://user:pass@host:5432/db
//
// 业务层只依赖 Store 接口，后端可整体替换（Pro 版可切大规模时序存储）。
func Open(dsn string) (Store, error) {
	if strings.HasPrefix(dsn, "postgres://") {
		return NewPostgres(dsn)
	}
	path := strings.TrimPrefix(dsn, "sqlite://")
	return NewSQLite(path)
}
