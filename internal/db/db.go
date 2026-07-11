// Package db 持有连接池与 activities 查询。
// 对照 Node 版 db/client.ts + db/queries.ts：Drizzle 的连接池换成 pgxpool，
// ORM 拼接换成显式 SQL——迁移的核心差异就在这两处。
package db

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool 从 DATABASE_URL 建连接池；启动时 Ping 一次，把配置错误暴露在启动期而不是首个请求。
func NewPool(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL 未设置（本地开发见 .env.example，容器里由 task definition 注入）")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("创建连接池: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("数据库连通性检查失败: %w", err)
	}
	return pool, nil
}
