# 批次 1 实录：Node(Lambda+Hono+Drizzle) → Go 迁移

> 作业 1：搭 Go 环境、Go 连数据库、迁移部分 Node 数据库代码、个人介绍前端。
> 本文是"从结果看过程"的对照笔记：每个决策写清楚为什么，以及私有化部署经验的对应物。

## 迁移对照表

| Node 版 (project_test_vps) | Go 版 (本仓库) | 差异要点 |
|---|---|---|
| `db/client.ts`（Drizzle + pg Pool） | `internal/db/db.go`（pgxpool） | 连接池启动时 Ping，把配置错误暴露在启动期 |
| `db/queries.ts` getActivities | `internal/db/activities.go` | ORM 链式调用 → 手写 SQL + `pgx.CollectRows` 扫描进 struct |
| `db/schema.ts`（drizzle 定义） | `schema.sql` | ORM schema → 纯 DDL；索引三条原样平移 |
| `lib/constants.ts` 白名单归一化 | `internal/handler/api.go` | 逻辑逐行对应：非法 city 回退 shanghai，非法 category 不过滤 |
| Next.js 前端 | `html/template` + `go:embed` | 一个二进制同时出 API 和页面，为容器化铺路 |
| Hono on Lambda | `net/http` 标准库（Go 1.22+ 方法路由） | 无框架；优雅退出监听 SIGTERM（Fargate 滚动部署需要） |

## 关键决策

1. **不用 gin/gorm**：作业量小，标准库 + pgx 能看清 Go 本色；显式 SQL 也符合"查询语义要能逐条对照"的迁移验收标准。
2. **健康检查不连库**：`/healthz` 只报进程存活。数据库抖动时希望 ALB 保留实例（问题在下游），而不是把服务摘掉造成雪崩式 502。
3. **模板 go:embed 编进二进制**：单一制品。Dockerfile 里不需要 COPY 模板目录，distroless 镜像里也没有文件系统布局问题。
4. **查询里 `($2 = '' OR category = $2)`**：用一条 SQL 吃掉"可选过滤"，避免 Go 里拼接 SQL 字符串（拼接是注入的温床）。

## 踩坑记录

- **宿主机 5432 被本机 homebrew PostgreSQL 占用**：容器 `Started` 不代表端口映射可用，连接打到了 homebrew PG 上（特征报错 `role "postgres" does not exist`——homebrew PG 默认角色是系统用户名）。解法：容器映射挪到 5433。
  教训：**排查连接问题先确认"连上的是谁"，`lsof -i :端口` 一眼定位**。
- `docker compose`（插件版）不存在，本机是独立版 `docker-compose`（brew），命令写法不同。

## 私有化 → AWS 对照（本批涉及部分）

| 私有化经验 | 本批对应物 |
|---|---|
| 服务器上装 PG / 起库脚本 | docker-compose + initdb 挂载（schema+seed 自动执行） |
| .env 配置文件 | 本地仍是 env；上云后换 SSM 注入（批次 2） |
| systemd 管进程 | 本地裸跑；上云后 ECS Service 管（批次 2） |

## 验收记录（2026-07-11）

- `GET /healthz` → 200 ok
- `GET /api/activities`（默认上海）→ 4 条，**过期与 draft 的种子被正确排除**
- `GET /api/activities?city=shanghai&category=exhibition` → 1 条
- `GET /api/activities?city=tokyo` → 回退 shanghai
- `GET /` → SevenQi27 个人介绍页渲染正常
