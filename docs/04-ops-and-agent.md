# 批次 4 实录：运维演示面板 + AI OPS Agent

> 培训班第二周作业（周六/周日）：SNS/SQS+死信队列、性能SDK→日志→ECS清洗→可视化、
> Synthetics 巡检、API 灰度上线、AI OPS Agent。全部挂在复活的 ECS 底座上，
> 收敛到一个 `/ops` 面板给老师直观演示。

## 架构：一套服务，四条运维能力，两个 agent 门面

```
ALB(公网) → ECS 上的 takemefree-go
  ├─ /ops           运维演示面板（队列深度 / 巡检 / 性能 / 日志，3 秒刷新 + 演示按钮）
  ├─ /ops/chat      AI 运维对话（ReAct 循环，MiMo 当大脑，SSE 推过程）
  ├─ /mcp           MCP 端点（同一份工具核心，供 Claude Code 等外部宿主调用）
  ├─ 性能 SDK 中间件 → CloudWatch 专用日志组（每 5 秒批量直写）
  └─ worker goroutine 消费 SQS（云上与 server 同进程）
一次性 ECS 任务：-logclean 聚合日志 → S3；-migrate 初始化库
Synthetics：每 5 分钟探测 ALB → 面板 Synthetics 卡片
灰度：独立小栈，Lambda 别名加权路由（10% → 100%）
```

设计主线：**一个工具核心，多个门面**。CloudWatch/SQS/Synthetics/性能 这五个查询
既是面板卡片的数据源，也是 agent 的工具集，也是 MCP 的 tools——写一次，三处复用。

## 作业逐条对应

| 作业 | 落点 |
|---|---|
| 周六1.1 Synthetics 巡检 | `infra/observability.yaml` 的 Canary，探 /healthz 与活动列表 |
| 周六1.2 SNS/SQS+DLQ | `internal/events/`，`infra/messaging.yaml`（队列+DLQ+非空告警） |
| 周六1.3 API 灰度 | `infra/canary-demo.yaml`，Lambda 别名加权路由 |
| 周日 性能SDK→日志→ECS清洗→可视化 | `internal/perf/`（SDK+清洗）+ `/ops` 面板 |
| 作业2 AI OPS Agent（AWS必做+MCP选做） | `internal/agent/`，聊天页 + MCP 端点 |

## 关键设计决策

**1. Agent 大脑可插拔。** ReAct 循环用协议无关的 JSON 约定（模型每轮输出一个
`{tool,args}` 或 `{final}`），不依赖各家 function-calling 实现差异。切换模型只动
三个环境变量 `LLM_BASE_URL/LLM_API_KEY/LLM_MODEL`。本次用 MiMo——因为 Bedrock 对
国内个人账号有 allowlist 门槛（实测撞到"unsupported countries"墙）。这是真实合规绕行，
比硬啃 Bedrock 更能体现工程判断。

**2. 密钥零明文。** LLM API key 与 MCP 令牌走 SSM SecureString，ECS 任务定义用
`Secrets` 字段在启动期注入，不进模板、不进任务定义明文、不进 git。执行角色获授
`ssm:GetParameters`（注入发生在启动期，用的是执行角色不是任务角色）。

**3. 灰度用 Lambda 别名而非 ALB 双服务。** alias(prod) 初始 100% 指向 v1，
`update-alias` 挂 routing-config 权重掺入 v2（实测 100 发 ≈ 90:10），确认后切 100%。
API Gateway 只认 alias，切流对调用方零感知，回滚是把 alias 指回 v1（一秒）。

## 踩坑记录（每个都真实撞出来）

1. **Go 1.22 mux：`/mcp`（无方法）与 `GET /` 冲突**，注册即 panic、容器起不来、
   滚动发布卡死。无方法模式被当子树通配，改 `POST /mcp` 解决。
2. **PutLogEvents 要求批内时间戳升序**，零星乱序请求导致整批 400 拒绝。
   flush 前 `sort.Slice` 按时间排。
3. **性能中间件包装 ResponseWriter 吞掉 Flusher**，SSE handler 的 `http.Flusher`
   断言失败 → 聊天流 500 "streaming unsupported"。给 recorder 补 `Flush()` 透传。
4. **Lambda::Permission 引用 `:prod` 别名靠字符串拼接，CFN 看不出依赖**，
   权限先于别名创建 → 404 整栈回滚。显式 `DependsOn: ProdAlias`。
5. **Synthetics 步骤名不许带斜杠**，`executeHttpStep('GET /healthz')` 校验失败。
   改用 `GET healthz` 之类的合规名。
6. **MiMo 多轮工具调用后偶发返回空 content**，导致 final 为空。属模型侧抖动，
   非代码 bug；同一问题重试即拿到完整回答。（生产上应对空回复做一轮重试）

## MCP 接入（外部 agent 调用云上服务器）

```bash
TOKEN=$(grep MCP_TOKEN .env.aws | cut -d= -f2)
claude mcp add --transport http takemefree-ops \
  http://<ALB>/mcp --header "Authorization: Bearer $TOKEN"
```

接入后在 Claude Code 里提问，即由外部 agent 宿主调用部署在 AWS 上的自研 MCP 工具。

## 验收记录（2026-07-20）

- `/ops` 面板四卡全绿：DLQ 深度实时、Synthetics PASSED、性能 p50/p95、日志样本流
- 点"毒消息"按钮 → worker 拒 3 次 → SQS 移入 DLQ → 告警 ALARM（约 30s 全程）
- ECS `-logclean` 任务退出码 0，聚合产物落 `s3://…/aggregates/latest.json`
- 灰度：100 发实测 90 v1 / 10 v2，切 100% 后 20 发全 v2
- `/ops/chat`：agent 自主连查 4 工具，诊断出"整体健康、DLQ 有 10 条毒消息触发告警"
- `/mcp`：无 token 401、tools/list 返回 5 工具、tools/call 查到真实队列深度

## 成本与清理

底座（ALB+RDS+Fargate）约 $1.5/天，其余无服务器件近乎免费。演示完一键拆：
messaging / observability / canary-demo / go-base 四栈 delete-stack，ECR 镜像删 tag。
