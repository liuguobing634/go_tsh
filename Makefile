# go_tsh 开发任务入口（面向 CI / Unix shell）。
#
# Windows 本地开发建议直接用 scripts/check.ps1：它会自动从 GOROOT 推导 gofmt
# 的绝对路径，因此 go 未加入 PATH 也能跑。
#
# 若 go / gofmt 不在 PATH 上：
#   make check GO="/d/Program Files/Go/bin/go.exe" GOFMT="/d/Program Files/Go/bin/gofmt.exe"

GO      ?= go
GOFMT   ?= gofmt
BIN_DIR ?= bin
BIN     ?= $(BIN_DIR)/tshd
PKG     ?= ./...
VERSION ?= dev
LDFLAGS ?= -s -w -X main.version=$(VERSION)

.PHONY: help
help: ## 显示所有可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: fmt
fmt: ## 就地格式化代码
	$(GOFMT) -w .

.PHONY: fmt-check
fmt-check: ## 校验格式，不修改文件（CI 门禁）
	@out=$$($(GOFMT) -l .); \
	if [ -n "$$out" ]; then \
		echo "以下文件未格式化："; echo "$$out"; exit 1; \
	fi

.PHONY: vet
vet: ## 静态检查
	$(GO) vet $(PKG)

.PHONY: test
test: ## 单元测试
	$(GO) test $(PKG)

.PHONY: race
race: ## 单元测试 + 竞态检测（需要 cgo 与 C 编译器）
	$(GO) test -race $(PKG)

.PHONY: cover
cover: ## 生成并汇总覆盖率
	$(GO) test -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -func=coverage.out | tail -n 1

.PHONY: bench
bench: ## 基准测试
	$(GO) test -run '^$$' -bench . -benchmem $(PKG)

.PHONY: build
build: ## 编译二进制到 bin/
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/tshd

.PHONY: run
run: ## 本地启动服务
	$(GO) run ./cmd/tshd -addr :8080 -log-level debug

.PHONY: tidy
tidy: ## 整理 go.mod
	$(GO) mod tidy

.PHONY: check
check: fmt-check vet race ## 一键门禁：格式 + 静态检查 + 竞态测试
	@echo "check 全部通过"

.PHONY: clean
clean: ## 清理构建产物
	$(GO) clean
	rm -rf $(BIN_DIR) coverage.out
