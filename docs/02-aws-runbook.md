# 批次 2 执行手册：ECR + ECS(Fargate) + ALB + CloudMap（你亲手跑）

> 分工：模板/代码已备好（infra/ecs-stack.yaml + /api/via-lambda 端点），以下命令全部由你执行。
> 每步都写了"这一步在私有化里对应什么"，这就是知识转移的抓手。
> 预计总耗时 ~40 分钟（其中 RDS 创建等待 ~10 分钟）。成本：全套跑一天 < $1，做完就拆。

## 0. 变量准备（每次开终端先跑这段）

```bash
cd /Users/d/D/project/takemefree-go
export AWS_REGION=us-east-1
export ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
export REPO=$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com/takemefree-go
```

## 1. ECR：建仓库、推镜像（对应私有化：Harbor 建项目 + docker push）

```bash
# 建仓库（只需一次）
aws ecr create-repository --repository-name takemefree-go

# 登录：ECR 的"用户名密码"是 IAM 换来的 12 小时临时令牌——注意和 Harbor 固定账号的区别
aws ecr get-login-password | docker login --username AWS --password-stdin $ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com

# 构建并推送（M 系芯片原生 arm64，任务定义里已声明 ARM64 运行时）
docker build -t $REPO:v1 .
docker push $REPO:v1
```

## 2. 建栈（对应私有化：买机器+装环境+配 Nginx+建库，一个命令全包）

```bash
export MY_IP=$(curl -s ifconfig.me)
# 密码自己定，≥16位；后面 psql 还要用，先存变量
export DB_PASS='换成你自己的至少16位密码'

aws cloudformation deploy \
  --template-file infra/ecs-stack.yaml \
  --stack-name takemefree-go-ecs \
  --capabilities CAPABILITY_IAM \
  --parameter-overrides ImageUri=$REPO:v1 DBPassword=$DB_PASS MyIpCidr=$MY_IP/32
```

等待 ~10 分钟（RDS 最慢）。另开一个终端可以看进度：

```bash
aws cloudformation describe-stack-events --stack-name takemefree-go-ecs \
  --query 'StackEvents[0:8].[ResourceType,ResourceStatus]' --output table
```

## 3. 初始化数据库（对应私有化：ssh 上库服务器跑建表脚本——现在从本机直连）

```bash
export DB_HOST=$(aws cloudformation describe-stacks --stack-name takemefree-go-ecs \
  --query 'Stacks[0].Outputs[?OutputKey==`DbEndpoint`].OutputValue' --output text)

psql "postgresql://appuser:$DB_PASS@$DB_HOST:5432/takemefree" -f schema.sql -f seed.sql
```

> 连不上先怀疑两件事：① 你的公网 IP 变了（重跑 curl ifconfig.me 对比 MY_IP）② 公司/家里网络封了 5432 出方向。

## 4. 验收（作业 2 的交付截图就在这步）

```bash
export ALB_URL=$(aws cloudformation describe-stacks --stack-name takemefree-go-ecs \
  --query 'Stacks[0].Outputs[?OutputKey==`AlbUrl`].OutputValue' --output text)

curl $ALB_URL/healthz                 # → ok
open $ALB_URL                         # 浏览器看首页卡片流（数据来自 RDS）
curl $ALB_URL/api/activities | head   # CRUD 列表
curl $ALB_URL/api/via-lambda          # ★题眼：CloudMap 查号台 → 调 Lambda，
                                      #   响应里有 discoveredVia + lambdaResult
```

控制台截图点位：ECS 服务的任务列表（RUNNING）、CloudMap 两个命名空间
（takemefree.local 里 ECS 自动注册的 web 实例 / takemefree-registry 里手工注册的 hello-lambda）、ALB 目标组 healthy。

## 5. 排错三板斧（任务起不来时按顺序查）

```bash
# ① 服务事件：拉不起任务的原因九成写在这（镜像拉不到/健康检查失败/子网没公网IP）
aws ecs describe-services --cluster takemefree-go-cluster --services takemefree-go-svc \
  --query 'services[0].events[0:5].message'

# ② 任务停止原因
aws ecs list-tasks --cluster takemefree-go-cluster --desired-status STOPPED --query 'taskArns[0]' --output text \
  | xargs -I{} aws ecs describe-tasks --cluster takemefree-go-cluster --tasks {} \
    --query 'tasks[0].{stopped:stoppedReason,container:containers[0].reason}'

# ③ 应用日志
aws logs tail /ecs/takemefree-go --since 10m
```

## 6. 拆除（当天做完必须执行——ALB+RDS+Fargate 都是按小时计费）

```bash
aws cloudformation delete-stack --stack-name takemefree-go-ecs
aws cloudformation wait stack-delete-complete --stack-name takemefree-go-ecs && echo 已拆干净
# ECR 仓库和镜像保留（存储费约等于零），下次重建栈 15 分钟恢复全部环境
```

## 本批的知识转移清单（做完对照着回想）

| 你刚才做的 | 私有化对应物 | 关键差异 |
|---|---|---|
| ecr get-login-password | docker login Harbor | 凭证是 IAM 签发的 12h 临时令牌，没有固定密码 |
| cloudformation deploy | 手工买机器装环境 | 基础设施变成一份可 diff、可回滚、可一键拆除的代码 |
| task definition 的两个角色 | 部署账号 vs 运行账号 | execution role 给 ECS 底座（拉镜像写日志），task role 给你的代码（查 CloudMap 调 Lambda）——权限绑身份不绑机器 |
| ALB 健康检查摘除实例 | Nginx upstream 检查 | 摘除后 ECS 会自动补新任务（desired count），自愈闭环 |
| CloudMap 两种命名空间 | 内网 DNS / Consul | DNS 型给 ECS 自动注册，HTTP 型可注册任意资源（Lambda ARN 当属性存），DiscoverInstances 就是查号 API |
| delete-stack | 关机退机器 | 拆除是常规操作而非事故——环境可再生，才敢随手拆 |
