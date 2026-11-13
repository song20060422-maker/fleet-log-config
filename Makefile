.PHONY: build run test race vet fmt coverage smoke clean

# 构建服务二进制
build:
	go build -o fleet ./.

# 本地运行（默认 :8000）
run:
	go run .

# 单元测试
test:
	go test ./... -count=1

# 竞态检测（需要 CGO，例如 Linux CI 环境）
race:
	CGO_ENABLED=1 go test ./... -count=1 -race

# 静态检查
vet:
	go vet ./...

# 代码格式检查
fmt:
	gofmt -l .

# 分包覆盖率门禁（默认 >=85%）
coverage:
	bash hack/coverage.sh 85

# 冒烟验证：编译 + 单测 + vet
smoke: build test vet

clean:
	rm -f fleet fleet.exe
	rm -rf data
