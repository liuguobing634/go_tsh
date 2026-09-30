# 刻意不写 `# syntax=docker/dockerfile:1`：
# 那条指令会让 BuildKit 去 docker.io 拉一个前端镜像，
# 在离线或受限网络下直接构建失败，而本文件并没有用到需要它的特性。

# ---------------------------------------------------------------- 构建阶段
FROM golang:1.27-alpine AS build

WORKDIR /src

# 本项目零第三方依赖，没有 go.sum。只复制 go.mod 足以让这一层被缓存，
# 源码变动不会导致重新下载依赖。
COPY go.mod ./
RUN go mod download

COPY . .

ARG VERSION=dev

# CGO_ENABLED=0 产出完全静态的二进制，运行阶段才能用 scratch。
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/tshd ./cmd/tshd

# ---------------------------------------------------------------- 运行阶段
#
# scratch 里没有 shell、没有 libc、没有时区库。
# 本服务完全静态链接、不读任何配置文件，因此 scratch 足够；
# 代价是不能 docker exec 进去排查，也没有 HEALTHCHECK 可写
# （scratch 里没有可执行的探针），请由编排层通过 HTTP 探 /healthz。
FROM scratch

COPY --from=build /out/tshd /tshd

# 以 nobody 运行。scratch 里没有 /etc/passwd，所以只能用数字 uid:gid。
USER 65534:65534

EXPOSE 8080

ENTRYPOINT ["/tshd"]
CMD ["-addr", ":8080"]
