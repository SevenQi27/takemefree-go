// CRUD 接口：迁移自 src/lambda/app.ts + activity-service.ts。
// 端点、响应形状（{"activity":..}/{"activities":..}/{"deleted":..}/{"error":..}）与 Node 版一致。
// 输入清洗逐函数对应：requiredText/optionalText/optionalNumber/optionalDate/parseTags/parseReservation/parseStatus。
package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SevenQi27/takemefree-go/internal/db"
	"github.com/SevenQi27/takemefree-go/internal/events"
)

var validStatuses = map[string]bool{"published": true, "hidden": true, "draft": true}

// ActivityInput 松散输入：字段类型故意用 any——Node 版接受
// tags 为数组或逗号串、坐标为字符串或数字、reservationRequired 为布尔或 "true"/"on"。
type ActivityInput struct {
	Title               any `json:"title"`
	City                any `json:"city"`
	District            any `json:"district"`
	Address             any `json:"address"`
	Latitude            any `json:"latitude"`
	Longitude           any `json:"longitude"`
	Category            any `json:"category"`
	Tags                any `json:"tags"`
	StartTime           any `json:"startTime"`
	EndTime             any `json:"endTime"`
	CoverImage          any `json:"coverImage"`
	FreeType            any `json:"freeType"`
	ReservationRequired any `json:"reservationRequired"`
	Status              any `json:"status"`
}

func optionalText(v any) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	t := strings.TrimSpace(s)
	if t == "" {
		return nil
	}
	return &t
}

func requiredText(v any, field string) (string, error) {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("%s is required.", field)
	}
	return strings.TrimSpace(s), nil
}

func optionalNumber(v any) *float64 {
	switch n := v.(type) {
	case float64:
		return &n
	case string:
		if strings.TrimSpace(n) == "" {
			return nil
		}
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%g", &f); err != nil {
			return nil
		}
		return &f
	default:
		return nil
	}
}

// optionalDate 对照 TS 的 new Date(value)：接受几种常见文本格式，解析失败按 null 处理。
func optionalDate(v any) *time.Time {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return &t
		}
	}
	return nil
}

func parseTags(v any) []string {
	out := []string{}
	switch tags := v.(type) {
	case []any:
		for _, item := range tags {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		for _, s := range strings.Split(tags, ",") {
			if t := strings.TrimSpace(s); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

func parseReservation(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return v == "true" || v == "on"
}

func parseStatus(v any) string {
	if s, ok := v.(string); ok && validStatuses[s] {
		return s
	}
	return "published"
}

// toActivityValues 对照 activity-service.ts 同名函数：清洗+默认值。
func toActivityValues(in ActivityInput) (db.ActivityValues, error) {
	title, err := requiredText(in.Title, "title")
	if err != nil {
		return db.ActivityValues{}, err
	}
	city, _ := in.City.(string)
	category := ""
	if c, ok := in.Category.(string); ok {
		category = normalizeCategory(c)
	}
	if category == "" {
		category = "event" // Node 版：normalizeCategory 未命中时默认 event
	}
	return db.ActivityValues{
		Title:               title,
		City:                normalizeCity(city),
		District:            optionalText(in.District),
		Address:             optionalText(in.Address),
		Latitude:            optionalNumber(in.Latitude),
		Longitude:           optionalNumber(in.Longitude),
		Category:            category,
		Tags:                parseTags(in.Tags),
		StartTime:           optionalDate(in.StartTime),
		EndTime:             optionalDate(in.EndTime),
		CoverImage:          optionalText(in.CoverImage),
		FreeType:            optionalText(in.FreeType),
		ReservationRequired: parseReservation(in.ReservationRequired),
		Status:              parseStatus(in.Status),
	}, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func errJSON(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ListActivities GET /api/activities —— 管理视角全量列表（含 hidden/draft）。
func ListActivities(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acts, err := db.ListActivitiesForAdmin(r.Context(), pool)
		if err != nil {
			log.Printf("list activities: %v", err)
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"activities": acts})
	}
}

// GetActivityByID GET /api/activities/{id}
func GetActivityByID(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		act, err := db.GetActivity(r.Context(), pool, r.PathValue("id"))
		if err != nil {
			log.Printf("get activity: %v", err)
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		if act == nil {
			errJSON(w, http.StatusNotFound, "Activity not found.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"activity": act})
	}
}

// CreateActivityHandler POST /api/activities
func CreateActivityHandler(pool *pgxpool.Pool, pub *events.Publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in ActivityInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			errJSON(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		values, err := toActivityValues(in)
		if err != nil {
			errJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		act, err := db.CreateActivity(r.Context(), pool, values)
		if err != nil {
			log.Printf("create activity: %v", err)
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		pub.Publish(r.Context(), "activity.created", act)
		writeJSON(w, http.StatusCreated, map[string]any{"activity": act})
	}
}

// UpdateActivityHandler PUT /api/activities/{id} —— 整行覆盖式更新（与 Node 版一致）。
func UpdateActivityHandler(pool *pgxpool.Pool, pub *events.Publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in ActivityInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			errJSON(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		values, err := toActivityValues(in)
		if err != nil {
			errJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		act, err := db.UpdateActivity(r.Context(), pool, r.PathValue("id"), values)
		if err != nil {
			log.Printf("update activity: %v", err)
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		if act == nil {
			errJSON(w, http.StatusNotFound, "Activity not found.")
			return
		}
		pub.Publish(r.Context(), "activity.updated", act)
		writeJSON(w, http.StatusOK, map[string]any{"activity": act})
	}
}

// DeleteActivityHandler DELETE /api/activities/{id}
func DeleteActivityHandler(pool *pgxpool.Pool, pub *events.Publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		act, err := db.DeleteActivity(r.Context(), pool, r.PathValue("id"))
		if err != nil {
			log.Printf("delete activity: %v", err)
			errJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		if act == nil {
			errJSON(w, http.StatusNotFound, "Activity not found.")
			return
		}
		pub.Publish(r.Context(), "activity.deleted", act)
		writeJSON(w, http.StatusOK, map[string]any{"deleted": act})
	}
}
