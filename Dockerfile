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
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kong-dots ./cmd/server

# --- runtime ------------------------------------------------------------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 kongdots
WORKDIR /app
COPY --from=api /out/kong-dots /usr/local/bin/kong-dots
COPY --from=ui /ui/dist /app/web
ENV KONGDOTS_ADDR=:8080 \
    KONGDOTS_DATA_DIR=/data \
    KONGDOTS_STATIC_DIR=/app/web
RUN mkdir -p /data && chown kongdots:kongdots /data
USER kongdots
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["kong-dots"]
