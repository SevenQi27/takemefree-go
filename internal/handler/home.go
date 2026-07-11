package handler

import (
	"embed"
	"html/template"
	"log"
	"net/http"
	"time"
	_ "time/tzdata" // 内嵌时区数据：distroless/static 等最小镜像里不依赖系统 zoneinfo

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SevenQi27/takemefree-go/internal/db"
)

// 模板用 go:embed 编进二进制：单一制品，容器里不需要再拷模板目录。
//
//go:embed templates/home.html
var homeFS embed.FS

var homeTmpl = template.Must(template.ParseFS(homeFS, "templates/home.html"))

// 展示用中文标签：对照 Node 版 lib/constants.ts 的 cityLabel/categoryLabel。
var (
	cityLabels     = map[string]string{"shanghai": "上海", "beijing": "北京", "shenzhen": "深圳"}
	categoryLabels = map[string]string{"event": "活动", "exhibition": "展览", "public-space": "公共空间", "welfare": "福利", "online": "线上资源"}
	cityOrder      = []string{"shanghai", "beijing", "shenzhen"}
	categoryOrder  = []string{"event", "exhibition", "public-space", "welfare", "online"}
)

// 统一 Asia/Shanghai 时区，避免服务端时区漂移（对照 lib/format.ts 的决策）。
var cst = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		panic(err)
	}
	return loc
}()

// formatActivityTime 迁移自 lib/format.ts：
// 无开始时间=「长期开放」；同天补结束时刻；跨天只显示日期区间。
func formatActivityTime(start, end *time.Time) string {
	if start == nil {
		return "长期开放"
	}
	s := start.In(cst)
	startStr := s.Format("1月2日 15:04")
	if end == nil {
		return startStr
	}
	e := end.In(cst)
	if s.Format("1月2日") == e.Format("1月2日") {
		return startStr + " – " + e.Format("15:04")
	}
	return s.Format("1月2日") + " – " + e.Format("1月2日")
}

// homeView 是模板的数据模型：当前筛选状态 + 供 tab/chip 渲染的选项 + 卡片。
type homeView struct {
	Cities     []option
	Categories []option
	City       string
	CityLabel  string
	Category   string
	Cards      []card
}

type option struct {
	Value    string
	Label    string
	Selected bool
}

type card struct {
	Title         string
	TimeText      string
	Location      string
	CategoryLabel string
	FreeType      string
	Tags          []string
	NeedsReserve  bool
}

// Home GET /?city=&category= —— 首页免费活动发现（specs/1.home-discovery）。
// 服务端直出：SSR 由 html/template 完成，无客户端二次拉取（对应非功能需求"性能"）。
func Home(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		city := normalizeCity(r.URL.Query().Get("city"))
		category := normalizeCategory(r.URL.Query().Get("category"))

		acts, err := db.GetActivities(r.Context(), pool, city, category)
		if err != nil {
			log.Printf("首页查询失败: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		view := homeView{City: city, CityLabel: cityLabels[city], Category: category}
		for _, c := range cityOrder {
			view.Cities = append(view.Cities, option{c, cityLabels[c], c == city})
		}
		// "全部"入口 = 清除分类过滤（AC-003 的"可清除回到全部"）。
		view.Categories = append(view.Categories, option{"", "全部", category == ""})
		for _, c := range categoryOrder {
			view.Categories = append(view.Categories, option{c, categoryLabels[c], c == category})
		}
		for _, a := range acts {
			loc := cityLabels[a.City]
			if a.District != nil {
				loc += " · " + *a.District
			}
			if a.Address != nil {
				loc += " · " + *a.Address
			}
			freeType := ""
			if a.FreeType != nil {
				freeType = *a.FreeType
			}
			view.Cards = append(view.Cards, card{
				Title:         a.Title,
				TimeText:      formatActivityTime(a.StartTime, a.EndTime),
				Location:      loc,
				CategoryLabel: categoryLabels[a.Category],
				FreeType:      freeType,
				Tags:          a.Tags,
				NeedsReserve:  a.ReservationRequired,
			})
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := homeTmpl.Execute(w, view); err != nil {
			log.Printf("渲染首页模板失败: %v", err)
		}
	}
}
