# --- frontend -----------------------------------------------------------
FROM node:22-alpine AS ui
WORKDIR /ui
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm install
COPY frontend/ ./
RUN npm run build

# --- backend ------------------------------------------------------------
FROM golang:1.26-alpine AS api
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kong-flow ./cmd/server

# --- runtime ------------------------------------------------------------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 kongflow
WORKDIR /app
COPY --from=api /out/kong-flow /usr/local/bin/kong-flow
COPY --from=ui /ui/dist /app/web
ENV KONGFLOW_ADDR=:8080 \
    KONGFLOW_DATA_DIR=/data \
    KONGFLOW_STATIC_DIR=/app/web
RUN mkdir -p /data && chown kongflow:kongflow /data
USER kongflow
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["kong-flow"]
