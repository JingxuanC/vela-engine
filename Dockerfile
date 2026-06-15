# Stage 1: Build
FROM golang:1.26-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /build/api-server ./cmd/server

# Stage 2: Development
FROM golang:1.26-alpine AS dev
WORKDIR /app
RUN go install github.com/air-verse/air@latest
COPY go.mod go.sum ./
RUN go mod download
CMD ["air"]

# Stage 3: Production
FROM alpine:3.21 AS prod
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /build/api-server /usr/local/bin/api-server
EXPOSE 8000
CMD ["api-server"]
