# takemefree-go

A Node-to-Go migration exercise deployed on AWS, built for a cloud-native training course.

The original app (Next.js + Hono on Lambda + Drizzle) is reimplemented as a single Go binary:
SSR home page (free-activity discovery with city/category filters), full CRUD API, and a
CloudMap-discovery-to-Lambda bridge endpoint — then shipped through three stages:

1. **Local** — stdlib `net/http` + pgx, Postgres via docker-compose ([docs/01](docs/01-node-to-go.md))
2. **ECS** — ECR + Fargate(ARM64) + ALB + CloudMap dual namespaces + inline Lambda,
   one self-contained CloudFormation stack ([docs/02](docs/02-aws-runbook.md), [diagram](docs/architecture.svg))
3. **PR preview envs** — GitHub OIDC + CodeBuild + per-PR incremental stack with
   ALB header routing (`X-PR-Env: pr-N`), auto-created on PR open, auto-destroyed on close ([docs/03](docs/03-pr-env.md))

## Run locally

```bash
docker-compose up -d          # Postgres :5433 with schema + seed
cp .env.example .env
go run ./cmd/server           # http://localhost:8080
```

## Layout

```
cmd/server          entrypoint (+ -migrate subcommand for in-VPC CI migration)
internal/db         pgxpool, queries, embedded schema
internal/handler    SSR home, CRUD API, CloudMap→Lambda bridge
infra/              CloudFormation: base stack / per-PR stack / CI foundation
.github/workflows   PR env pipeline (validate branch → OIDC → CodeBuild → comment)
```

_PR preview environment pipeline demo (this line exists only to open a demo PR)._
