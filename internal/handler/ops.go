package handler

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	cwlogs "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/aws-sdk-go-v2/service/synthetics"

	"github.com/SevenQi27/takemefree-go/internal/events"
	"github.com/SevenQi27/takemefree-go/internal/perf"
)

//go:embed templates/ops.html
var opsFS embed.FS

var opsTmpl = template.Must(template.ParseFS(opsFS, "templates/ops.html"))

// Ops 作业演示面板：消息队列 / 巡检 / 性能 / 日志 四块可视化 + 演示按钮。
// 各依赖按需降级：对应环境变量缺失时该卡片显示"未启用"，不影响其他卡片。
type Ops struct {
	pub          *events.Publisher
	sdk          *perf.SDK
	sqsClient    *sqs.Client
	synthClient  *synthetics.Client
	s3Client     *s3.Client
	logsClient   *cwlogs.Client
	queueURL     string
	dlqURL       string
	statsBucket  string
	perfLogGroup string
}

func NewOps(ctx context.Context, pub *events.Publisher, sdk *perf.SDK) *Ops {
	o := &Ops{
		pub:          pub,
		sdk:          sdk,
		queueURL:     os.Getenv("QUEUE_URL"),
		dlqURL:       os.Getenv("DLQ_URL"),
		statsBucket:  os.Getenv("STATS_BUCKET"),
		perfLogGroup: os.Getenv("PERF_LOG_GROUP"),
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Printf("ops: 加载 AWS 配置失败，面板云端卡片全部降级: %v", err)
		return o
	}
	o.sqsClient = sqs.NewFromConfig(cfg)
	o.synthClient = synthetics.NewFromConfig(cfg)
	o.s3Client = s3.NewFromConfig(cfg)
	o.logsClient = cwlogs.NewFromConfig(cfg)
	return o
}

// Page GET /ops —— 面板骨架，数据由页面 JS 轮询下面的 API 填充
func (o *Ops) Page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := opsTmpl.Execute(w, nil); err != nil {
		log.Printf("ops page: %v", err)
	}
}

// Queues GET /api/ops/queues —— 主队列与 DLQ 的实时深度
func (o *Ops) Queues(w http.ResponseWriter, r *http.Request) {
	type qstat struct {
		Name       string `json:"name"`
		Visible    string `json:"visible"`
		InFlight   string `json:"inFlight"`
		Configured bool   `json:"configured"`
	}
	out := []qstat{}
	for _, q := range []struct{ name, url string }{
		{"主队列 takemefree-activity-worker", o.queueURL},
		{"死信队列 takemefree-activity-dlq", o.dlqURL},
	} {
		st := qstat{Name: q.name}
		if q.url != "" && o.sqsClient != nil {
			attrs, err := o.sqsClient.GetQueueAttributes(r.Context(), &sqs.GetQueueAttributesInput{
				QueueUrl: aws.String(q.url),
				AttributeNames: []sqstypes.QueueAttributeName{
					sqstypes.QueueAttributeNameApproximateNumberOfMessages,
					sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
				},
			})
			if err == nil {
				st.Configured = true
				st.Visible = attrs.Attributes["ApproximateNumberOfMessages"]
				st.InFlight = attrs.Attributes["ApproximateNumberOfMessagesNotVisible"]
			}
		}
		out = append(out, st)
	}
	writeJSON(w, http.StatusOK, map[string]any{"queues": out})
}

// Publish POST /api/ops/publish?poison=1 —— 演示按钮：发一条正常/毒消息
func (o *Ops) Publish(w http.ResponseWriter, r *http.Request) {
	title := "demo-message"
	if r.URL.Query().Get("poison") == "1" {
		title = "poison-demo-message" // worker 对含 poison 的载荷模拟处理失败 → 3次重试 → DLQ
	}
	o.pub.Publish(r.Context(), "ops.demo", map[string]any{
		"title":  title,
		"sentAt": time.Now().UTC().Format(time.RFC3339),
	})
	writeJSON(w, http.StatusOK, map[string]any{"sent": title})
}

// Canary GET /api/ops/canary —— Synthetics 巡检任务的最近一次结果
func (o *Ops) Canary(w http.ResponseWriter, r *http.Request) {
	if o.synthClient == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	res, err := o.synthClient.DescribeCanariesLastRun(r.Context(), &synthetics.DescribeCanariesLastRunInput{})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "error": err.Error()})
		return
	}
	type run struct {
		Name   string `json:"name"`
		State  string `json:"state"`
		RanAt  string `json:"ranAt"`
		Reason string `json:"reason,omitempty"`
	}
	runs := []run{}
	for _, c := range res.CanariesLastRun {
		item := run{Name: aws.ToString(c.CanaryName)}
		if c.LastRun != nil && c.LastRun.Status != nil {
			item.State = string(c.LastRun.Status.State)
			item.Reason = aws.ToString(c.LastRun.Status.StateReason)
			if c.LastRun.Timeline != nil && c.LastRun.Timeline.Completed != nil {
				item.RanAt = c.LastRun.Timeline.Completed.UTC().Format(time.RFC3339)
			}
		}
		runs = append(runs, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "runs": runs})
}

// Perf GET /api/ops/perf —— 实时统计（内存窗口）+ 最近一次 ECS 清洗产物（S3）
func (o *Ops) Perf(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"live": o.sdk.Snapshot()}
	if o.s3Client != nil && o.statsBucket != "" {
		obj, err := o.s3Client.GetObject(r.Context(), &s3.GetObjectInput{
			Bucket: aws.String(o.statsBucket),
			Key:    aws.String("aggregates/latest.json"),
		})
		if err == nil {
			defer obj.Body.Close()
			var cleaned any
			if json.NewDecoder(obj.Body).Decode(&cleaned) == nil {
				resp["cleaned"] = cleaned
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// Logs GET /api/ops/logs —— 性能日志组里最近的原始样本（日志观测卡片）
func (o *Ops) Logs(w http.ResponseWriter, r *http.Request) {
	if o.logsClient == nil || o.perfLogGroup == "" {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	res, err := o.logsClient.FilterLogEvents(r.Context(), &cwlogs.FilterLogEventsInput{
		LogGroupName: aws.String(o.perfLogGroup),
		StartTime:    aws.Int64(time.Now().Add(-10 * time.Minute).UnixMilli()),
		Limit:        aws.Int32(50),
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "error": err.Error()})
		return
	}
	lines := []string{}
	for _, e := range res.Events {
		lines = append(lines, aws.ToString(e.Message))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(lines))) // JSON 行以 ts 开头，倒序≈最新在前
	if len(lines) > 20 {
		lines = lines[:20]
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "lines": lines})
}
