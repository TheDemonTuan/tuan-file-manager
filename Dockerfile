# Stage 1: Build React Frontend
FROM node:22-alpine AS frontend-builder
WORKDIR /build/web

COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
RUN npm run build

# Stage 2: Build Go Binary
FROM golang:alpine AS backend-builder
WORKDIR /build

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Copy built assets from frontend-builder into web/dist
COPY --from=frontend-builder /build/web/dist ./web/dist

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app/filemgr ./cmd/server

# Stage 3: Minimal Production Non-Root Runtime
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 -S appgroup \
    && adduser -u 10001 -S appuser -G appgroup \
    && mkdir -p /data /tmp \
    && chown -R appuser:appgroup /data /tmp

WORKDIR /app
COPY --from=backend-builder --chown=10001:10001 /app/filemgr /app/filemgr

USER 10001:10001

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=10s \
  CMD ["/app/filemgr", "healthcheck", "--url", "http://127.0.0.1:8080/health/live"]

ENTRYPOINT ["/app/filemgr"]
