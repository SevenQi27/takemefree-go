// takemefree-go：培训班作业——Node(Lambda+Hono+Drizzle) 迁移到 Go 的最小服务。
// 一个二进制同时 serve API 与个人介绍页，为容器化（作业2）铺路。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SevenQi27/takemefree-go/internal/agent"
	"github.com/SevenQi27/takemefree-go/internal/db"
	"github.com/SevenQi27/takemefree-go/internal/events"
	"github.com/SevenQi27/takemefree-go/internal/handler"
	"github.com/SevenQi27/takemefree-go/internal/perf"
)

func main() {
	ctx := context.Background()

	// -migrate 子命令：CI 里由一次性 ECS 任务调用，跑完即退出（退出码=迁移成败）
	if len(os.Args) > 1 && os.Args[1] == "-migrate" {
		if err := db.Migrate(ctx, os.Getenv("DATABASE_URL")); err != nil {
			log.Fatalf("迁移失败: %v", err)
		}
		log.Println("迁移完成")
		return
	}

	// -logclean 子命令：性能日志清洗聚合 → S3，以一次性 ECS 任务运行（作业周日）
	if len(os.Args) > 1 && os.Args[1] == "-logclean" {
		if err := perf.RunClean(ctx); err != nil {
			log.Fatalf("清洗失败: %v", err)
		}
		return
	}

	// -worker 子命令：SQS 消费者进程，不连数据库（作业周六1.2：SNS/SQS+DLQ）
	if len(os.Args) > 1 && os.Args[1] == "-worker" {
		workerCtx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		if err := events.RunWorker(workerCtx); err != nil {
			log.Fatalf("worker 退出: %v", err)
		}
		return
	}

	pool, err := db.NewPool(ctx)
	if err != nil {
		log.Fatalf("启动失败: %v", err)
	}
	defer pool.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handler.Home(pool))
	mux.HandleFunc("GET /healthz", handler.Healthz)
	// CRUD：端点与 Node 版 Lambda(src/lambda/app.ts) 一致
	mux.HandleFunc("GET /api/activities", handler.ListActivities(pool))
	pub := events.NewPublisherFromEnv(ctx)
	mux.HandleFunc("POST /api/activities", handler.CreateActivityHandler(pool, pub))
	mux.HandleFunc("GET /api/activities/{id}", handler.GetActivityByID(pool))
	mux.HandleFunc("PUT /api/activities/{id}", handler.UpdateActivityHandler(pool, pub))
	mux.HandleFunc("DELETE /api/activities/{id}", handler.DeleteActivityHandler(pool, pub))
	// 作业2：CloudMap 发现 → 调 Lambda（仅 ECS 部署形态生效）
	mux.HandleFunc("GET /api/via-lambda", handler.ViaLambda)

	// 运维演示面板（周六/周日作业的可视化出口）
	sdk := perf.New(ctx)
	ops := handler.NewOps(ctx, pub, sdk)
	mux.HandleFunc("GET /ops", ops.Page)
	mux.HandleFunc("GET /api/ops/queues", ops.Queues)
	mux.HandleFunc("POST /api/ops/publish", ops.Publish)
	mux.HandleFunc("GET /api/ops/canary", ops.Canary)
	mux.HandleFunc("GET /api/ops/perf", ops.Perf)
	mux.HandleFunc("GET /api/ops/logs", ops.Logs)

	// 作业2：AI OPS Agent——一个工具核心两个门面（页面聊天框 + MCP 端点）
	toolbox, err := agent.NewToolbox(ctx, sdk)
	if err != nil {
		log.Printf("agent: 工具箱初始化失败，AI 运维功能禁用: %v", err)
	} else {
		opsAgent := agent.New(toolbox)
		mux.HandleFunc("GET /ops/chat", handler.ChatPage)
		mux.HandleFunc("POST /api/ops/chat", opsAgent.ChatHandler)
		mux.HandleFunc("POST /mcp", opsAgent.MCPHandler)
		log.Printf("agent: 已挂载 /ops/chat 与 /mcp（大脑启用=%v）", opsAgent.Enabled())
	}

	// 云上形态：QUEUE_URL 存在时 worker 以 goroutine 伴生（单容器省一份 Fargate 钱）；
	// 本地开发仍可用 -worker 子命令独立跑，便于观察两个进程各自的日志
	if os.Getenv("QUEUE_URL") != "" {
		go func() {
			if err := events.RunWorker(ctx); err != nil {
				log.Printf("伴生 worker 退出: %v", err)
			}
		}()
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{Addr: ":" + port, Handler: sdk.Middleware(mux)}

	// 优雅退出：Fargate 滚动部署时先收 SIGTERM，给在途请求一个排空窗口。
	go func() {
		log.Printf("listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
