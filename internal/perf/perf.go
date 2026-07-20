// Package perf 作业(周日)：性能SDK。以 http 中间件形态采集每个请求的
// 方法/路径/状态码/耗时，两条去向：
//  1. 内存环形窗口 —— /ops 面板实时读（最近5分钟的 p50/p95/错误率）
//  2. CloudWatch Logs 专用日志组 —— 每5秒批量 PutLogEvents 直写（不是stdout转发），
//     供 ECS 清洗任务离线聚合。PERF_LOG_GROUP 未设置时此去向禁用，本地零依赖。
package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	cwlogs "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

// Sample 一次请求的性能样本（也是写进日志的 JSON 行结构）
type Sample struct {
	Ts     time.Time `json:"ts"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Status int       `json:"status"`
	DurMs  float64   `json:"durMs"`
}

type SDK struct {
	mu      sync.Mutex
	window  []Sample // 内存环形窗口（最近 windowKeep 内）
	pending []Sample // 待批量上传 CloudWatch 的缓冲

	client    *cwlogs.Client
	logGroup  string
	logStream string
}

const windowKeep = 5 * time.Minute

// New 初始化 SDK；PERF_LOG_GROUP 未设置时只保留内存窗口
func New(ctx context.Context) *SDK {
	s := &SDK{}
	group := os.Getenv("PERF_LOG_GROUP")
	if group == "" {
		log.Println("perf: PERF_LOG_GROUP 未设置，仅内存统计，不写 CloudWatch")
		return s
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Printf("perf: 加载 AWS 配置失败，降级为仅内存统计: %v", err)
		return s
	}
	s.client = cwlogs.NewFromConfig(cfg)
	s.logGroup = group
	s.logStream = fmt.Sprintf("perf-%s-%d", hostname(), time.Now().Unix())
	_, err = s.client.CreateLogStream(ctx, &cwlogs.CreateLogStreamInput{
		LogGroupName:  aws.String(s.logGroup),
		LogStreamName: aws.String(s.logStream),
	})
	if err != nil {
		log.Printf("perf: 创建日志流失败，降级为仅内存统计: %v", err)
		s.client = nil
		return s
	}
	go s.flushLoop(ctx)
	log.Printf("perf: SDK 已启用，日志流 %s/%s", s.logGroup, s.logStream)
	return s
}

// Middleware 包住整个 mux，对所有路由生效
func (s *SDK) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.record(Sample{
			Ts:     start.UTC(),
			Method: r.Method,
			Path:   r.URL.Path,
			Status: rec.status,
			DurMs:  float64(time.Since(start).Microseconds()) / 1000,
		})
	})
}

func (s *SDK) record(sm Sample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.window = append(s.window, sm)
	// 修剪窗口：只留最近 windowKeep
	cutoff := time.Now().Add(-windowKeep)
	i := 0
	for ; i < len(s.window) && s.window[i].Ts.Before(cutoff); i++ {
	}
	s.window = s.window[i:]
	if s.client != nil {
		s.pending = append(s.pending, sm)
	}
}

// flushLoop 每5秒把缓冲批量写入 CloudWatch Logs
func (s *SDK) flushLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.flush(ctx)
		}
	}
}

func (s *SDK) flush(ctx context.Context) {
	s.mu.Lock()
	batch := s.pending
	s.pending = nil
	s.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	// PutLogEvents 要求批内事件按时间戳升序，否则整批 400 拒绝
	sort.Slice(batch, func(i, j int) bool { return batch[i].Ts.Before(batch[j].Ts) })
	logEvents := make([]types.InputLogEvent, 0, len(batch))
	for _, sm := range batch {
		line, _ := json.Marshal(sm)
		logEvents = append(logEvents, types.InputLogEvent{
			Timestamp: aws.Int64(sm.Ts.UnixMilli()),
			Message:   aws.String(string(line)),
		})
	}
	_, err := s.client.PutLogEvents(ctx, &cwlogs.PutLogEventsInput{
		LogGroupName:  aws.String(s.logGroup),
		LogStreamName: aws.String(s.logStream),
		LogEvents:     logEvents,
	})
	if err != nil {
		log.Printf("perf: 上传 %d 条样本失败: %v", len(batch), err)
	}
}

// Stats /ops 面板要的实时统计：按路径聚合最近5分钟的 p50/p95/错误率
type Stats struct {
	WindowSec int         `json:"windowSec"`
	Total     int         `json:"total"`
	ByPath    []PathStats `json:"byPath"`
}

type PathStats struct {
	Path   string  `json:"path"`
	Count  int     `json:"count"`
	P50Ms  float64 `json:"p50Ms"`
	P95Ms  float64 `json:"p95Ms"`
	ErrPct float64 `json:"errPct"`
}

func (s *SDK) Snapshot() Stats {
	s.mu.Lock()
	window := make([]Sample, len(s.window))
	copy(window, s.window)
	s.mu.Unlock()

	group := map[string][]Sample{}
	for _, sm := range window {
		group[sm.Method+" "+sm.Path] = append(group[sm.Method+" "+sm.Path], sm)
	}
	stats := Stats{WindowSec: int(windowKeep.Seconds()), Total: len(window)}
	for path, samples := range group {
		durs := make([]float64, 0, len(samples))
		errs := 0
		for _, sm := range samples {
			durs = append(durs, sm.DurMs)
			if sm.Status >= 500 {
				errs++
			}
		}
		sort.Float64s(durs)
		stats.ByPath = append(stats.ByPath, PathStats{
			Path:   path,
			Count:  len(samples),
			P50Ms:  percentile(durs, 0.50),
			P95Ms:  percentile(durs, 0.95),
			ErrPct: float64(errs) * 100 / float64(len(samples)),
		})
	}
	sort.Slice(stats.ByPath, func(i, j int) bool { return stats.ByPath[i].Count > stats.ByPath[j].Count })
	return stats
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(q * float64(len(sorted)-1))
	return sorted[idx]
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush 透传底层 Flusher——否则包一层后 SSE handler 的 http.Flusher 断言失败，
// 聊天流直接 "streaming unsupported"（agent 聊天页踩过）
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
