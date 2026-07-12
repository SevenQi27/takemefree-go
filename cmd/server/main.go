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

	"github.com/SevenQi27/takemefree-go/internal/db"
	"github.com/SevenQi27/takemefree-go/internal/handler"
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
	mux.HandleFunc("POST /api/activities", handler.CreateActivityHandler(pool))
	mux.HandleFunc("GET /api/activities/{id}", handler.GetActivityByID(pool))
	mux.HandleFunc("PUT /api/activities/{id}", handler.UpdateActivityHandler(pool))
	mux.HandleFunc("DELETE /api/activities/{id}", handler.DeleteActivityHandler(pool))
	// 作业2：CloudMap 发现 → 调 Lambda（仅 ECS 部署形态生效）
	mux.HandleFunc("GET /api/via-lambda", handler.ViaLambda)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{Addr: ":" + port, Handler: mux}

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
