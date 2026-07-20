package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	cwlogs "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// RunClean -logclean 子命令：作业(周日)的"用ECS清洗日志"环节。
// 从性能日志组拉取最近一小时的原始样本 → 按接口聚合(次数/p50/p95/错误率) →
// 聚合结果写 S3（latest.json 供 /ops 面板读取 + 按时间归档一份）。
// 以一次性 Fargate 任务运行（与 -migrate 同款模式），跑完即退，退出码=成败。
func RunClean(ctx context.Context) error {
	group := os.Getenv("PERF_LOG_GROUP")
	bucket := os.Getenv("STATS_BUCKET")
	if group == "" || bucket == "" {
		return fmt.Errorf("PERF_LOG_GROUP / STATS_BUCKET 未设置")
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("加载 AWS 配置: %w", err)
	}
	logsClient := cwlogs.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)

	// 1. 拉原始样本（翻页取完最近一小时）
	since := time.Now().Add(-1 * time.Hour)
	var samples []Sample
	var next *string
	for {
		out, err := logsClient.FilterLogEvents(ctx, &cwlogs.FilterLogEventsInput{
			LogGroupName: aws.String(group),
			StartTime:    aws.Int64(since.UnixMilli()),
			NextToken:    next,
		})
		if err != nil {
			return fmt.Errorf("读日志: %w", err)
		}
		for _, e := range out.Events {
			var sm Sample
			if json.Unmarshal([]byte(aws.ToString(e.Message)), &sm) == nil {
				samples = append(samples, sm)
			}
		}
		if out.NextToken == nil {
			break
		}
		next = out.NextToken
	}
	log.Printf("clean: 拉取 %d 条样本(最近1小时)", len(samples))

	// 2. 聚合（复用 /ops 实时统计的同一套口径）
	group2 := map[string][]float64{}
	errs := map[string]int{}
	for _, sm := range samples {
		key := sm.Method + " " + sm.Path
		group2[key] = append(group2[key], sm.DurMs)
		if sm.Status >= 500 {
			errs[key]++
		}
	}
	agg := struct {
		GeneratedAt time.Time   `json:"generatedAt"`
		WindowHours int         `json:"windowHours"`
		Total       int         `json:"total"`
		ByPath      []PathStats `json:"byPath"`
	}{GeneratedAt: time.Now().UTC(), WindowHours: 1, Total: len(samples)}
	for path, durs := range group2 {
		sort.Float64s(durs)
		agg.ByPath = append(agg.ByPath, PathStats{
			Path:   path,
			Count:  len(durs),
			P50Ms:  percentile(durs, 0.50),
			P95Ms:  percentile(durs, 0.95),
			ErrPct: float64(errs[path]) * 100 / float64(len(durs)),
		})
	}
	sort.Slice(agg.ByPath, func(i, j int) bool { return agg.ByPath[i].Count > agg.ByPath[j].Count })

	// 3. 写 S3：latest.json 给面板，时间戳副本留档
	body, _ := json.MarshalIndent(agg, "", "  ")
	for _, key := range []string{
		"aggregates/latest.json",
		"aggregates/" + agg.GeneratedAt.Format("2006-01-02T15-04-05") + ".json",
	} {
		_, err := s3Client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(bucket),
			Key:         aws.String(key),
			Body:        bytes.NewReader(body),
			ContentType: aws.String("application/json"),
		})
		if err != nil {
			return fmt.Errorf("写 s3://%s/%s: %w", bucket, key, err)
		}
	}
	log.Printf("clean: 聚合完成，%d 个接口，已写 s3://%s/aggregates/latest.json", len(agg.ByPath), bucket)
	return nil
}
