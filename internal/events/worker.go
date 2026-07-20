package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// RunWorker -worker 子命令入口：长轮询消费 SQS，演示"处理失败→可见性超时重试→超过
// maxReceiveCount 进死信队列"的完整链路。
//
// 消费语义是 at-least-once：同一条消息可能被重复投递（重试、多实例竞争），
// 所以真实系统的处理逻辑必须幂等——这正是 mini-custody 提现模块要正面解决的问题。
func RunWorker(ctx context.Context) error {
	queueURL := os.Getenv("QUEUE_URL")
	if queueURL == "" {
		return errors.New("QUEUE_URL 未设置")
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("加载 AWS 配置: %w", err)
	}
	client := sqs.NewFromConfig(cfg)
	log.Printf("worker: 开始消费 %s", queueURL)

	for {
		if ctx.Err() != nil {
			return nil
		}
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:              aws.String(queueURL),
			MaxNumberOfMessages:   10,
			WaitTimeSeconds:       20, // 长轮询：省钱(减少空receive计费)也降延迟
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameApproximateReceiveCount,
			},
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("worker: receive 失败: %v", err)
			continue
		}
		for _, msg := range out.Messages {
			receiveCount := msg.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]
			if err := handle(msg, receiveCount); err != nil {
				// 关键动作是"什么都不做"：不删除消息，等可见性超时后 SQS 自动重投；
				// 第 maxReceiveCount 次失败后由 SQS 自动移入死信队列
				log.Printf("worker: 处理失败(第%s次投递)，等待重试或进DLQ: %v", receiveCount, err)
				continue
			}
			_, err := client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(queueURL),
				ReceiptHandle: msg.ReceiptHandle,
			})
			if err != nil {
				log.Printf("worker: 删除消息失败(将被重复消费，靠幂等兜底): %v", err)
			}
		}
	}
}

// handle 模拟下游动作（如同步搜索索引）。标题含 "poison" 的活动模拟处理必失败的毒消息。
func handle(msg types.Message, receiveCount string) error {
	var env Envelope
	if err := json.Unmarshal([]byte(aws.ToString(msg.Body)), &env); err != nil {
		return fmt.Errorf("解析信封: %w", err)
	}
	if strings.Contains(string(env.Payload), "poison") {
		return fmt.Errorf("毒消息：下游处理失败(模拟)")
	}
	log.Printf("worker: 已处理 type=%s (第%s次投递) payload=%.80s", env.Type, receiveCount, env.Payload)
	return nil
}
