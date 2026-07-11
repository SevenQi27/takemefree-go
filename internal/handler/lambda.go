package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
)

// 作业2题眼端点：GET /api/via-lambda
// 流程：CloudMap DiscoverInstances 按命名空间+服务名查出实例属性里的 Lambda ARN → SDK Invoke → 透传结果。
// CloudMap 在这里的角色是注册表（查号台）：ECS 任务是自动登记的住户，Lambda 是手工登记的住户。

var (
	sdkOnce   sync.Once
	sdClient  *servicediscovery.Client
	lmbClient *awslambda.Client
	sdkErr    error
)

func initSDK(ctx context.Context) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx) // Fargate 里自动走 task role 的临时凭证
	if err != nil {
		sdkErr = err
		return
	}
	sdClient = servicediscovery.NewFromConfig(cfg)
	lmbClient = awslambda.NewFromConfig(cfg)
}

// ViaLambda 本地开发未配置 DISCOVERY_* 时返回 501 说明，不影响其余功能。
func ViaLambda(w http.ResponseWriter, r *http.Request) {
	namespace := os.Getenv("DISCOVERY_NAMESPACE")
	service := os.Getenv("DISCOVERY_SERVICE")
	if namespace == "" || service == "" {
		errJSON(w, http.StatusNotImplemented, "DISCOVERY_NAMESPACE/DISCOVERY_SERVICE 未配置（此端点仅在 ECS 部署形态下可用）")
		return
	}
	sdkOnce.Do(func() { initSDK(r.Context()) })
	if sdkErr != nil {
		log.Printf("aws sdk init: %v", sdkErr)
		errJSON(w, http.StatusInternalServerError, sdkErr.Error())
		return
	}

	// 1) 查号台：按命名空间+服务名发现实例
	disc, err := sdClient.DiscoverInstances(r.Context(), &servicediscovery.DiscoverInstancesInput{
		NamespaceName: &namespace,
		ServiceName:   &service,
	})
	if err != nil {
		log.Printf("DiscoverInstances: %v", err)
		errJSON(w, http.StatusBadGateway, "CloudMap 发现失败: "+err.Error())
		return
	}
	if len(disc.Instances) == 0 {
		errJSON(w, http.StatusBadGateway, "CloudMap 里没有已注册的实例")
		return
	}
	arn, ok := disc.Instances[0].Attributes["lambdaArn"]
	if !ok {
		errJSON(w, http.StatusBadGateway, "实例属性缺少 lambdaArn")
		return
	}

	// 2) 按发现结果调用 Lambda
	out, err := lmbClient.Invoke(r.Context(), &awslambda.InvokeInput{FunctionName: &arn})
	if err != nil {
		log.Printf("lambda invoke: %v", err)
		errJSON(w, http.StatusBadGateway, "Lambda 调用失败: "+err.Error())
		return
	}

	var payload any
	if err := json.Unmarshal(out.Payload, &payload); err != nil {
		payload = string(out.Payload)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"discoveredVia": map[string]string{"namespace": namespace, "service": service, "instanceId": *disc.Instances[0].InstanceId},
		"lambdaArn":     arn,
		"lambdaResult":  payload,
	})
}
