package db

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// schema.sql 内嵌进二进制：迁移不依赖文件系统，distroless 镜像里也能跑。
//
//go:embed schema.sql
var schemaSQL string

// Migrate 执行幂等建表脚本（全部 IF NOT EXISTS，可重复执行）。
// 为什么单独建连接：脚本是多条语句，pgx 默认的扩展协议一次只允许一条，
// 这里切到 simple protocol 整段执行。
// 为什么存在这个函数：CI(CodeBuild) 在公网，RDS 只对服务安全组开门——
// 迁移由 CI 发起一次性 ECS 任务(命令覆盖为 -migrate)在 VPC 内执行。
func Migrate(ctx context.Context, dsn string) error {
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL 未设置")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("解析连接串: %w", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("连接数据库: %w", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("执行迁移脚本: %w", err)
	}
	return nil
}
