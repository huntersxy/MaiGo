# syntax=docker/dockerfile:1

# 原版 Dashboard 以 PyPI 包发布；构建期取静态 dist，运行镜像不需要 Python
FROM --platform=$BUILDPLATFORM python:3.13-alpine AS dashboard
RUN pip install --no-cache-dir --no-deps --target /out maibot-dashboard==1.9.13

# 插件 Runner 复用 MaiBot 原版 Python 实现，只装 runner 真正需要的依赖
# （maibot wheel 提供 src.plugin_runtime.runner，--no-deps 避免拉入 faiss/playwright 等）
# 必须与运行镜像同发行版同 Python 版本：pydantic_core/msgpack 是编译扩展
FROM --platform=$TARGETPLATFORM alpine:3.23 AS plugin-runtime
RUN apk add --no-cache python3 py3-pip && \
    python3 -m venv /runner && \
    /runner/bin/pip install --no-cache-dir --no-input --no-deps "maibot==1.3.19" && \
    /runner/bin/pip install --no-cache-dir --no-input \
        pydantic tomlkit structlog packaging msgpack maibot-plugin-sdk

# go-webp 依赖 libwebp，cgo 必须按目标架构构建
FROM --platform=$TARGETPLATFORM golang:1.26.5-alpine AS build
RUN apk add --no-cache build-base libwebp-dev libwebp-static
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
# 整份静态链接，产物不依赖任何系统动态库；sharpyuv 的 gamma 转换需要 libm
ENV CGO_LDFLAGS="-static -lwebp -lsharpyuv -lm"
RUN CGO_ENABLED=1 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X maibot-go/internal/common/version.Build=${VERSION}" -o /out/maibot-go .

FROM alpine:3.23
ENV TZ=Asia/Shanghai
# 数据库是 SQLite（对齐 MaiBot src/common/database/database.py:33），
# 库文件在 /app/data/MaiBot.db，随 data 卷一起备份，无需 postgresql 客户端
# git 供插件市场下载/更新插件使用
RUN apk add --no-cache ca-certificates su-exec tzdata wget python3 git && \
    addgroup -S maibot && adduser -S -G maibot maibot
WORKDIR /app
COPY --from=build /out/maibot-go /app/maibot-go
COPY --from=dashboard /out/maibot_dashboard/dist /app/dashboard/dist
# Python Runner 独立 venv；MAIBOT_PLUGIN_SPAWN=1 时直接用它拉起原版 runner_main
COPY --from=plugin-runtime /runner /app/plugin-runtime
# 内置插件清单随镜像分发：host 启动期读取 plugins/built_in/plugin_management/_manifest.json
COPY plugins/built_in/plugin_management/_manifest.json /app/plugins/built_in/plugin_management/_manifest.json
COPY --chmod=755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
# config/data/logs/plugins 与 media/stickers 一样预建并改属主，供命名卷首次挂载时继承
# temp 是 Runner 的非持久运行时目录（Python build_plugin_paths 写 temp/plugins）
RUN mkdir -p /app/config /app/data /app/logs /app/plugins /app/stickers /app/media /app/temp && chown -R maibot:maibot /app
EXPOSE 7468
ENV MAIBOT_DASHBOARD_DIR=/app/dashboard/dist
ENV MAIBOT_PLUGIN_PYTHON=/app/plugin-runtime/bin/python
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/app/maibot-go"]
