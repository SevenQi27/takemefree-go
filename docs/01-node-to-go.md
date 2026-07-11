# 批次 1 实录：Node(Lambda+Hono+Drizzle) → Go 迁移

> 需求基线：specs/1.home-discovery（TakeMeFree 首页免费活动发现，F-001~F-007），不采用课堂作业的"个人介绍页"变体。
> 本文是"从结果看过程"的对照笔记：每个决策写清楚为什么，以及私有化部署经验的对应物。

## 迁移对照表

| Node 版 (project_test_vps) | Go 版 (本仓库) | 差异要点 |
|---|---|---|
| `db/client.ts`（Drizzle + pg Pool） | `internal/db/db.go`（pgxpool） | 连接池启动时 Ping，把配置错误暴露在启动期 |
| `db/queries.ts` getActivities | `internal/db/activities.go` | ORM 链式调用 → 手写 SQL + `pgx.CollectRows` 扫描进 struct |
| `db/schema.ts`（drizzle 定义） | `schema.sql` | ORM schema → 纯 DDL；索引三条原样平移 |
| `lib/constants.ts` 白名单归一化 | `internal/handler/api.go` | 逻辑逐行对应：非法 city 回退 shanghai，非法 category 不过滤 |
| Next.js 首页（Server Component + 客户端筛选） | `html/template` + `go:embed`，SSR 直出 | 城市/分类切换从客户端路由降级为带参链接——无 JS 依赖，语义相同 |
| `lib/format.ts` formatActivityTime | `home.go` 同名函数 | Intl.DateTimeFormat → time.Format；`import _ "time/tzdata"` 内嵌时区（distroless 镜像里没有系统 zoneinfo） |
| `components/ActivityCard.tsx` | home.html 模板 article 块 | 标题/时间/地点/标签/预约徽章逐项对应 |
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

## 验收记录（2026-07-11，对照 requirements.md 的 AC）

- AC-001 首页卡片含标题/时间/地点/标签/预约徽章 ✓
- AC-002 切换北京只显示北京活动 ✓
- AC-003 分类过滤 + "全部"清除 ✓
- AC-004 过期/draft 种子不出现（grep 计数 0）✓
- AC-005 375px 视口单列布局、首屏含筛选与列表、无横滚（浏览器实测截图）✓
- AC-006 北京+福利 → 空状态文案 ✓
- 附加：`GET /api/activities` JSON 接口保留；`city=tokyo` 回退 shanghai；时间格式化三形态（同天补时刻/跨天日期区间/长期开放）与 format.ts 一致

## CRUD 迁移（src/lambda/app.ts + activity-service.ts → crud.go）

端点与响应形状与 Node 版完全一致：

| 端点 | 语义 |
|---|---|
| `GET /api/activities` | 管理视角全量列表（含 hidden/draft），updated_at desc |
| `GET/PUT/DELETE /api/activities/{id}` | 单条查/整行覆盖更新/删，404 = `{"error":"Activity not found."}` |
| `POST /api/activities` | 创建，201；缺 title → 400 |

迁移要点：

- **宽容输入解析逐函数对应**：tags 接受数组或逗号串、坐标接受字符串或数字、reservationRequired 接受 bool 或 "true"/"on"——Go 里用 `any` 字段 + 类型断言实现 TS 的联合类型语义
- **写入走 RETURNING**：创建/更新/删除都返回完整行，和 Drizzle `.returning()` 对齐
- **顺手修正一处**：非法 uuid 文本（SQLSTATE 22P02）按 404 处理；Node 版这里会抛到 onError 变 500
- Node 版每个操作前的 `ensureSchema()` 没有迁移——Go 版建表由 schema.sql 负责（本地 initdb / 上云跑迁移），运行时不做 DDL

CRUD 冒烟验收（2026-07-11）：创建（宽容解析全命中）→ 查单条 → 更新+发布 → 缺 title 400 → 删除 → 删除后 404 → 非法 uuid 404 → 列表含 draft ✓

## 已知的种子数据小毛刺（非代码 bug）

首张卡片 chips 出现两次"展览"和一个"免预约"标签：原种子的 tags 数组本身含 category 同名词和预约状态词，Node 版渲染同样重复。保留以维持与原版行为一致。
