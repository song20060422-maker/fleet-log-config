# 构建阶段
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o fleet ./.

# 运行阶段（纯 Go sqlite，无需 cgo）
FROM alpine:3.20
WORKDIR /app
COPY --from=builder /app/fleet ./fleet
COPY manifest/config/config.yaml ./manifest/config/config.yaml
ENV GF_GCFG_PATH=./manifest/config
EXPOSE 8000
VOLUME ["/app/data"]
CMD ["./fleet"]
