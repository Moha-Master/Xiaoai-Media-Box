BIN := bin/xiaoai-mb
GO ?= go
LDFLAGS := -s -w
WEB := web
# 前端产物落在 Go 包目录里，由 internal/app/web.go 的 go:embed 内嵌
WEB_DIST := internal/app/web/dist

.PHONY: all build web web-install dev test vet fmt clean

all: build

# 前端：dist 不进 git（构建产物），所以 make build 会先构建前端。
# 只改了 Go 代码、dist 已经存在时，可以单独 `$(GO) build ./...` 跳过 npm。
build: web
	@mkdir -p $(dir $(BIN))
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/xiaoai-mb

web:
	@test -d $(WEB)/node_modules || (cd $(WEB) && npm ci --no-audit --no-fund)
	cd $(WEB) && npm run build
	@# vite 的 emptyOutDir 会把占位文件一起删掉，补回来保证没有 node 也能编译
	@touch $(WEB_DIST)/.gitkeep

web-install:
	cd $(WEB) && npm install --no-audit --no-fund

# 前端开发：另开一个终端跑本服务，vite 会把 /api 代理到 :8088
dev:
	cd $(WEB) && npm run dev

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin $(WEB_DIST)/assets $(WEB_DIST)/index.html
