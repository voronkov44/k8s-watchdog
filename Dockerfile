# 1) build
FROM golang:1.22 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /watchdog ./...

# 2) runtime
FROM gcr.io/distroless/base-debian12
WORKDIR /
COPY --from=builder /watchdog /watchdog
USER nonroot:nonroot
ENTRYPOINT ["/watchdog"]
