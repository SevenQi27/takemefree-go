// Package events 作业3(周六)：SNS/SQS + 死信队列。
// 发布侧：业务写操作成功后向 SNS 发领域事件，SNS 扇出到 SQS，worker 异步消费。
// 设计取舍：这里是 fire-and-forget——事件发布失败只记日志、不让业务请求失败；
// 一致性要求高的场景（比如 mini-custody 的入账事件）要换 outbox 模式：事件先落库、
// 由独立进程投递，业务与事件在同一个数据库事务里。
package events

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
)

// Envelope 事件信封：type 供订阅方路由，payload 是领域对象快照
type Envelope struct {
	Type       string          `json:"type"`
	OccurredAt time.Time       `json:"occurredAt"`
	Payload    json.RawMessage `json:"payload"`
}

type Publisher struct {
	client   *sns.Client
	topicArn string
}

// NewPublisherFromEnv EVENTS_TOPIC_ARN 未设置时返回禁用态（本地开发零依赖，与 via-lambda 同款约定）
func NewPublisherFromEnv(ctx context.Context) *Publisher {
	topicArn := os.Getenv("EVENTS_TOPIC_ARN")
	if topicArn == "" {
		log.Println("events: EVENTS_TOPIC_ARN 未设置，事件发布已禁用")
		return &Publisher{}
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Printf("events: 加载 AWS 配置失败，事件发布已禁用: %v", err)
		return &Publisher{}
	}
	return &Publisher{client: sns.NewFromConfig(cfg), topicArn: topicArn}
}

// Publish 发布领域事件。失败不向上冒泡——业务成功不该被旁路的事件失败拖垮（取舍见包注释）。
func (p *Publisher) Publish(ctx context.Context, eventType string, payload any) {
	if p.client == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		log.Printf("events: 序列化失败 type=%s: %v", eventType, err)
		return
	}
	body, _ := json.Marshal(Envelope{Type: eventType, OccurredAt: time.Now().UTC(), Payload: raw})
	_, err = p.client.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(p.topicArn),
		Message:  aws.String(string(body)),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"eventType": {DataType: aws.String("String"), StringValue: aws.String(eventType)},
		},
	})
	if err != nil {
		log.Printf("events: 发布失败 type=%s: %v", eventType, err)
		return
	}
	log.Printf("events: 已发布 type=%s", eventType)
}
