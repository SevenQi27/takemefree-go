// Package handler：HTTP 层。白名单归一化逻辑对照 Node 版 lib/constants.ts。
package handler

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SevenQi27/takemefree-go/internal/db"
)

// 城市与分类白名单：lib/constants.ts 的单一事实来源平移过来。
var (
	validCities     = map[string]bool{"shanghai": true, "beijing": true, "shenzhen": true}
	validCategories = map[string]bool{"event": true, "exhibition": true, "public-space": true, "welfare": true, "online": true}
	defaultCity     = "shanghai"
)

// normalizeCity 非法/空值回退默认城市（首页永远展示某个城市）。
func normalizeCity(input string) string {
	if validCities[input] {
		return input
	}
	return defaultCity
}

// normalizeCategory 非法/空值返回 ""（= 不过滤）。
func normalizeCategory(input string) string {
	if validCategories[input] {
		return input
	}
	return ""
}

// Activities GET /api/activities?city=&category=
func Activities(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		city := normalizeCity(r.URL.Query().Get("city"))
		category := normalizeCategory(r.URL.Query().Get("category"))

		cards, err := db.GetActivities(r.Context(), pool, city, category)
		if err != nil {
			log.Printf("查询 activities 失败: %v", err)
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]any{"city": city, "category": category, "items": cards})
	}
}

// Healthz ALB 健康检查端点：只报进程存活，不连库——
// 数据库抖动时希望 ALB 保留实例（问题在下游），而不是把服务整个摘掉。
func Healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
