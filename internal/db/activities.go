package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ActivityCard 对应 Node 版 queries.ts 的 ActivityCardData：
// 首页卡片所需字段子集，不把整行传出去。
type ActivityCard struct {
	ID                  string     `json:"id"`
	Title               string     `json:"title"`
	City                string     `json:"city"`
	District            *string    `json:"district"`
	Address             *string    `json:"address"`
	Category            string     `json:"category"`
	Tags                []string   `json:"tags"`
	StartTime           *time.Time `json:"startTime"`
	EndTime             *time.Time `json:"endTime"`
	CoverImage          *string    `json:"coverImage"`
	FreeType            *string    `json:"freeType"`
	ReservationRequired bool       `json:"reservationRequired"`
}

// GetActivities 迁移自 queries.ts 的同名函数，查询语义逐条对应：
//   - 仅 status = 'published'
//   - 未过期：end_time 为空 或 end_time > now()
//   - 按城市过滤（city 已由 handler 用白名单归一化）
//   - category 非空则追加过滤
//   - 排序：start_time 升序 NULLS LAST，其次 created_at 倒序
func GetActivities(ctx context.Context, pool *pgxpool.Pool, city, category string) ([]ActivityCard, error) {
	sql := `
		SELECT id, title, city, district, address, category, tags,
		       start_time, end_time, cover_image, free_type, reservation_required
		FROM activities
		WHERE status = 'published'
		  AND city = $1
		  AND (end_time IS NULL OR end_time > now())
		  AND ($2 = '' OR category = $2)
		ORDER BY start_time ASC NULLS LAST, created_at DESC`

	rows, err := pool.Query(ctx, sql, city, category)
	if err != nil {
		return nil, err
	}
	cards, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ActivityCard])
	if err != nil {
		return nil, err
	}
	if cards == nil {
		cards = []ActivityCard{} // JSON 序列化为 [] 而不是 null，与 Node 版行为一致
	}
	return cards, nil
}
