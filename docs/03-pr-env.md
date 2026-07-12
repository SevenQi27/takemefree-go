# 批次 3 实录：PR 独立预览环境（OIDC + CodeBuild + 请求头路由）

> 作业3：基于 PR 分支的独立开发环境，CodeBuild + IAM 三角色（Cloudflare 腿裁掉：
> 本服务前后端是单二进制，无独立前端可发 Pages；环境入口用 ALB 请求头路由替代）。

## 架构决策：共享底座 + 增量栈

每个 PR 不重建全套 VPC/RDS（慢且贵），而是共享底座栈，只增量部署三样东西：
自己的任务定义 + 目标组 + **ALB 监听规则**（请求头 `X-PR-Env: pr-N` 命中才转发，
不带头仍是主环境）。课堂 Node 版用 API Gateway 按请求头分发，这是同一思想的 ALB 版。

```
PR opened/updated
  → GitHub Actions: 分支名校验(必须含 Jira ID，不合规直接红)
  → OIDC 换临时凭证(工作流里零长期密钥)
  → CodeBuild(ARM): git clone → docker build/push :pr-N → 部署 takemefree-go-pr-N 栈
  → 一次性 ECS 任务在 VPC 内跑 -migrate(库不对公网 CI 开门)
  → PR 评论环境地址与测试命令
PR closed
  → 删栈 + 删镜像 tag + 评论已回收
```

## 三个 IAM 角色（进门/干活/运行）

| 角色 | 谁用 | 权限边界 |
|---|---|---|
| ① takemefree-go-gh-deploy | GitHub Actions 经 OIDC 扮演 | 只信任本仓库；只能触发这一个 CodeBuild、删 `takemefree-go-pr-*` 栈及其内资源、删 pr-N 镜像 tag |
| ② takemefree-go-codebuild | CodeBuild 构建期 | 推镜像限本仓库、CFN 写限 pr-* 栈、SSM 读限 /takemefree-go/*、PassRole 限两个 ECS 角色 |
| ③ ECS execution/task role | 任务运行期 | 批次 2 已建，PR 栈直接引用 |

## 踩坑记录（每个都是真实撞出来的）

1. **CodeBuild 的 cwd 跨命令延续**：BUILD 阶段 `cd src` 构建完，POST_BUILD 再 `cd src`
   = 找 `src/src` → 不存在。非 export 变量跨命令也不保证存活。
   修法：cd 一律用 `$CODEBUILD_SRC_DIR` 绝对路径；变量与用它的命令合并进同一个 `|` 块。
2. **GitHub OIDC Provider 一个账号只能有一个**：新建报 409 AlreadyExists（旧项目建过）。
   修法：模板改为参数引用现有 Provider ARN。
3. **CFN 删栈用的是"调用者"的凭证**：回收角色只给 `cloudformation:DeleteStack` 不够——
   栈内 ECS 服务/ALB 规则的删除动作也发生在调用者身上 → DELETE_FAILED。
   修法：给回收方补栈内资源的删除权限；生产上更优解是给栈绑 CFN service role，
   让删除动作走栈自己的角色而不是调用者。
4. **（批次2遗留）ECS Service 忘了挂 TaskDefinition 字段**：CFN 校验不拦，创建时才报
   "TaskDefinition can not be blank"，整栈回滚。模板的字段完整性只有部署才是真校验。

## 迁移为什么走一次性 ECS 任务

课堂决策"迁移脚本必须在 CI 自动执行"，但 RDS 安全组只对服务 SG 开门，公网上的
CodeBuild 够不着。解法：schema.sql `go:embed` 进二进制 + `-migrate` 子命令，
CI 用 `ecs run-task`（命令覆盖为 `-migrate`）把迁移放进 VPC 内执行，等退出码判成败。
脚本全部 IF NOT EXISTS，幂等可重跑。

## 验收记录（2026-07-12）

- 分支 `feature/JIRA-1-pr-env-demo` 开 PR → 流水线绿 → PR 评论自动出现环境地址
- `curl -H "X-PR-Env: pr-1" $ALB/healthz` → ok；同集群出现独立服务 `takemefree-go-pr-1-svc`
- 不带请求头 → 仍路由主环境
- PR 关闭 → 栈与镜像 tag 自动回收（权限修复后由 PR #2 完整复验）
- 不合规分支名会在 validate 步被拒（校验逻辑见 workflow）
