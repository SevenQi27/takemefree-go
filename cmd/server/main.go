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

	pool, err := db.NewPool(ctx)
	if err != nil {
		log.Fatalf("启动失败: %v", err)
	}
	defer pool.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handler.Home(pool))
	mux.HandleFunc("GET /healthz", handler.Healthz)
	mux.HandleFunc("GET /api/activities", handler.Activities(pool))

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
