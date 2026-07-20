// Package agent 作业2：AI OPS Agent。
// 一个工具核心、两个门面：/ops/chat 页面聊天框（本服务自带 ReAct 循环 + OpenAI 兼容模型）
// 与 /mcp 端点（标准 MCP 协议，供 Claude Code 等外部 agent 宿主调用）。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwlogs "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/aws-sdk-go-v2/service/synthetics"

	"github.com/SevenQi27/takemefree-go/internal/perf"
)

// Tool 工具定义：两个门面共用同一份（聊天循环喂给模型的描述 = MCP tools/list 返回的描述）
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Run         func(ctx context.Context, args json.RawMessage) (string, error) `json:"-"`
}

type Toolbox struct {
	cw     *cloudwatch.Client
	logs   *cwlogs.Client
	sqsCli *sqs.Client
	synth  *synthetics.Client
	sdk    *perf.SDK
	Tools  []Tool
}

var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

func NewToolbox(ctx context.Context, sdk *perf.SDK) (*Toolbox, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	tb := &Toolbox{
		cw:     cloudwatch.NewFromConfig(cfg),
		logs:   cwlogs.NewFromConfig(cfg),
		sqsCli: sqs.NewFromConfig(cfg),
		synth:  synthetics.NewFromConfig(cfg),
		sdk:    sdk,
	}
	tb.Tools = []Tool{
		{Name: "get_alarms", Description: "查询 CloudWatch 告警列表及其当前状态(OK/ALARM)、原因", InputSchema: emptySchema, Run: tb.getAlarms},
		{Name: "get_queue_stats", Description: "查询业务消息主队列与死信队列(DLQ)的实时深度；死信队列非零说明有消息连续处理失败", InputSchema: emptySchema, Run: tb.getQueueStats},
		{Name: "get_canary_runs", Description: "查询 Synthetics 巡检任务最近一次探测结果(PASSED/FAILED)与失败原因", InputSchema: emptySchema, Run: tb.getCanaryRuns},
		{Name: "get_perf_stats", Description: "查询各接口最近5分钟的实时性能统计：请求数、p50/p95 延迟、错误率", InputSchema: emptySchema, Run: tb.getPerfStats},
		{Name: "get_recent_perf_logs", Description: "查询性能日志组最近10分钟的原始请求样本(JSON行)，用于定位具体慢请求或错误请求", InputSchema: emptySchema, Run: tb.getRecentPerfLogs},
	}
	return tb, nil
}

func (t *Toolbox) Find(name string) *Tool {
	for i := range t.Tools {
		if t.Tools[i].Name == name {
			return &t.Tools[i]
		}
	}
	return nil
}

func (t *Toolbox) getAlarms(ctx context.Context, _ json.RawMessage) (string, error) {
	out, err := t.cw.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, a := range out.MetricAlarms {
		fmt.Fprintf(&b, "告警[%s] 状态=%s 原因=%s\n", aws.ToString(a.AlarmName), a.StateValue, aws.ToString(a.StateReason))
	}
	if b.Len() == 0 {
		return "无任何告警配置", nil
	}
	return b.String(), nil
}

func (t *Toolbox) getQueueStats(ctx context.Context, _ json.RawMessage) (string, error) {
	var b strings.Builder
	for _, q := range []struct{ label, url string }{
		{"主队列", os.Getenv("QUEUE_URL")},
		{"死信队列", os.Getenv("DLQ_URL")},
	} {
		if q.url == "" {
			continue
		}
		attrs, err := t.sqsCli.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl: aws.String(q.url),
			AttributeNames: []sqstypes.QueueAttributeName{
				sqstypes.QueueAttributeNameApproximateNumberOfMessages,
				sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
			},
		})
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s: 可见=%s 处理中=%s\n", q.label,
			attrs.Attributes["ApproximateNumberOfMessages"],
			attrs.Attributes["ApproximateNumberOfMessagesNotVisible"])
	}
	if b.Len() == 0 {
		return "队列未配置", nil
	}
	return b.String(), nil
}

func (t *Toolbox) getCanaryRuns(ctx context.Context, _ json.RawMessage) (string, error) {
	res, err := t.synth.DescribeCanariesLastRun(ctx, &synthetics.DescribeCanariesLastRunInput{})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range res.CanariesLastRun {
		if c.LastRun == nil || c.LastRun.Status == nil {
			continue
		}
		when := ""
		if c.LastRun.Timeline != nil && c.LastRun.Timeline.Completed != nil {
			when = c.LastRun.Timeline.Completed.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "巡检[%s] 结果=%s 时间=%s %s\n", aws.ToString(c.CanaryName),
			c.LastRun.Status.State, when, aws.ToString(c.LastRun.Status.StateReason))
	}
	if b.Len() == 0 {
		return "无巡检任务", nil
	}
	return b.String(), nil
}

func (t *Toolbox) getPerfStats(_ context.Context, _ json.RawMessage) (string, error) {
	st := t.sdk.Snapshot()
	var b strings.Builder
	fmt.Fprintf(&b, "最近%d秒窗口共%d个请求\n", st.WindowSec, st.Total)
	for _, p := range st.ByPath {
		fmt.Fprintf(&b, "%s: 次数=%d p50=%.1fms p95=%.1fms 错误率=%.1f%%\n", p.Path, p.Count, p.P50Ms, p.P95Ms, p.ErrPct)
	}
	return b.String(), nil
}

func (t *Toolbox) getRecentPerfLogs(ctx context.Context, _ json.RawMessage) (string, error) {
	group := os.Getenv("PERF_LOG_GROUP")
	if group == "" {
		return "性能日志组未配置", nil
	}
	res, err := t.logs.FilterLogEvents(ctx, &cwlogs.FilterLogEventsInput{
		LogGroupName: aws.String(group),
		StartTime:    aws.Int64(time.Now().Add(-10 * time.Minute).UnixMilli()),
		Limit:        aws.Int32(60),
	})
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(res.Events))
	for _, e := range res.Events {
		lines = append(lines, aws.ToString(e.Message))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(lines)))
	if len(lines) > 30 {
		lines = lines[:30]
	}
	if len(lines) == 0 {
		return "最近10分钟无样本", nil
	}
	return strings.Join(lines, "\n"), nil
}
