# 多阶段构建：编译环境 ~800MB，最终镜像只装一个静态二进制（distroless ~2MB 底座）。
# CGO_ENABLED=0 产出纯静态二进制，不依赖 glibc，才能跑在 distroless/static 上。
FROM golang:1.25-alpine AS build
WORKDIR /src
# 先拷 go.mod/go.sum 单独下载依赖，利用层缓存：代码改动不触发重新拉包
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/server"]
