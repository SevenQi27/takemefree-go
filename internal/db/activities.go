package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// Activity 整行模型：CRUD 接口（迁移自 src/lambda/activity-service.ts）返回完整行。
// JSON 字段名与 Drizzle 序列化输出保持一致（camelCase）。
type Activity struct {
	ID                  string     `json:"id"`
	Title               string     `json:"title"`
	City                string     `json:"city"`
	District            *string    `json:"district"`
	Address             *string    `json:"address"`
	Latitude            *float64   `json:"latitude"`
	Longitude           *float64   `json:"longitude"`
	Category            string     `json:"category"`
	Tags                []string   `json:"tags"`
	StartTime           *time.Time `json:"startTime"`
	EndTime             *time.Time `json:"endTime"`
	CoverImage          *string    `json:"coverImage"`
	FreeType            *string    `json:"freeType"`
	ReservationRequired bool       `json:"reservationRequired"`
	Status              string     `json:"status"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

// allColumns 的顺序必须与 Activity 字段顺序一致（RowToStructByPos 按位置扫描）。
const allColumns = `id, title, city, district, address, latitude, longitude, category, tags,
	start_time, end_time, cover_image, free_type, reservation_required, status, created_at, updated_at`

// ActivityValues 已清洗的写入值（handler 层负责从松散输入清洗成这个形状）。
type ActivityValues struct {
	Title               string
	City                string
	District            *string
	Address             *string
	Latitude            *float64
	Longitude           *float64
	Category            string
	Tags                []string
	StartTime           *time.Time
	EndTime             *time.Time
	CoverImage          *string
	FreeType            *string
	ReservationRequired bool
	Status              string
}

// ListActivitiesForAdmin 全量列表（含 hidden/draft），排序对照 Node 版：updated_at desc, created_at desc。
func ListActivitiesForAdmin(ctx context.Context, pool *pgxpool.Pool) ([]Activity, error) {
	rows, err := pool.Query(ctx, `SELECT `+allColumns+` FROM activities ORDER BY updated_at DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	acts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Activity])
	if err != nil {
		return nil, err
	}
	if acts == nil {
		acts = []Activity{}
	}
	return acts, nil
}

// GetActivity 按 id 查单条；找不到（含非法 uuid）返回 (nil, nil)。
func GetActivity(ctx context.Context, pool *pgxpool.Pool, id string) (*Activity, error) {
	rows, err := pool.Query(ctx, `SELECT `+allColumns+` FROM activities WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	act, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[Activity])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
			return nil, nil
		}
		return nil, err
	}
	return &act, nil
}

func CreateActivity(ctx context.Context, pool *pgxpool.Pool, v ActivityValues) (*Activity, error) {
	rows, err := pool.Query(ctx, `
		INSERT INTO activities (title, city, district, address, latitude, longitude, category, tags,
		                        start_time, end_time, cover_image, free_type, reservation_required, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		RETURNING `+allColumns,
		v.Title, v.City, v.District, v.Address, v.Latitude, v.Longitude, v.Category, v.Tags,
		v.StartTime, v.EndTime, v.CoverImage, v.FreeType, v.ReservationRequired, v.Status)
	if err != nil {
		return nil, err
	}
	act, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[Activity])
	if err != nil {
		return nil, err
	}
	return &act, nil
}

// UpdateActivity 整行覆盖式更新（对照 Node 版 set(toActivityValues(input))），updated_at 刷新为 now()。
func UpdateActivity(ctx context.Context, pool *pgxpool.Pool, id string, v ActivityValues) (*Activity, error) {
	rows, err := pool.Query(ctx, `
		UPDATE activities SET title=$2, city=$3, district=$4, address=$5, latitude=$6, longitude=$7,
		       category=$8, tags=$9, start_time=$10, end_time=$11, cover_image=$12, free_type=$13,
		       reservation_required=$14, status=$15, updated_at=now()
		WHERE id = $1
		RETURNING `+allColumns,
		id, v.Title, v.City, v.District, v.Address, v.Latitude, v.Longitude, v.Category, v.Tags,
		v.StartTime, v.EndTime, v.CoverImage, v.FreeType, v.ReservationRequired, v.Status)
	if err != nil {
		return nil, err
	}
	act, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[Activity])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
			return nil, nil
		}
		return nil, err
	}
	return &act, nil
}

func DeleteActivity(ctx context.Context, pool *pgxpool.Pool, id string) (*Activity, error) {
	rows, err := pool.Query(ctx, `DELETE FROM activities WHERE id = $1 RETURNING `+allColumns, id)
	if err != nil {
		return nil, err
	}
	act, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[Activity])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
			return nil, nil
		}
		return nil, err
	}
	return &act, nil
}

// isInvalidUUID：非法 uuid 文本（SQLSTATE 22P02）按"未找到"处理而不是 500——
// 对外语义上"不存在的 id"和"不合法的 id"都是 404（Node 版这里会 500，属于顺手修正）。
func isInvalidUUID(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
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
