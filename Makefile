.PHONY: api ui serve build test test-backend test-frontend test-db test-db-down demo demo-kong demo-down demo-logs demo-check docker

# --- development ---------------------------------------------------------
# Backend only, on :8080. Needs a PostgreSQL — `make demo-kong` starts one on
# :5432; point elsewhere with KONGDOTS_DATABASE_URL. Pair with `make ui`, which
# proxies /api to it.
api:
	cd backend && go run ./cmd/server

# Vite dev server with hot reload, on :5173. This is the URL you open.
ui:
	cd frontend && npm run dev

# Single-port alternative: build the UI and let the Go binary serve it on :8080.
serve: build
	KONGDOTS_STATIC_DIR=$(CURDIR)/frontend/dist ./bin/kong-dots

build:
	cd frontend && npm install && npm run build
	cd backend && go build -o $(CURDIR)/bin/kong-dots ./cmd/server

test: test-backend test-frontend

test-backend: test-db
	cd backend && go test ./...

# Throwaway PostgreSQL 18 for the backend tests, on :5433 so it never touches
# the database `make api` uses. Each test runs in a schema of its own.
test-db:
	@docker inspect -f '{{.State.Running}}' kongdots-test-db 2>/dev/null | grep -q true || { \
	  docker rm -f kongdots-test-db >/dev/null 2>&1; \
	  docker run -d --name kongdots-test-db \
	    -e POSTGRES_USER=kongdots -e POSTGRES_PASSWORD=kongdots -e POSTGRES_DB=kongdots_test \
	    -p 127.0.0.1:5433:5432 postgres:18-alpine >/dev/null; }
	@for i in $$(seq 30); do \
	  docker exec kongdots-test-db pg_isready -U kongdots -d kongdots_test >/dev/null 2>&1 && exit 0; \
	  sleep 1; \
	done; echo "kongdots-test-db never became ready" >&2; exit 1

test-db-down:
	docker rm -f kongdots-test-db >/dev/null 2>&1 || true

test-frontend:
	cd frontend && npm test

# --- demo stack ----------------------------------------------------------
# Kong 3.9.1 + demo topology (routes answer with canned JSON) + Kong Dots.
# UI: http://localhost:8080   Kong proxy: :8000   Kong Admin API: :8001
demo:
	docker compose -f deploy/demo.yml --profile app up -d --build

# Kong + the demo data + the Kong Dots database, for use with `make api` /
# `make ui` on the host.
demo-kong:
	docker compose -f deploy/demo.yml up -d

demo-down:
	docker compose -f deploy/demo.yml --profile app down -v

demo-logs:
	docker compose -f deploy/demo.yml logs -f demo-seed kong-dots

# Hit every seeded route and show what the gateway answers.
demo-check:
	@for r in "GET /orders" "POST /orders" "GET /users/me" "GET /users/admin" "GET /payments/status"; do \
	  m=$${r%% *}; p=$${r#* }; \
	  printf '%-6s %-22s -> %s\n' "$$m" "$$p" \
	    "$$(curl -s -o /tmp/kd-body -w '%{http_code}' -X $$m http://localhost:8000$$p) $$(head -c 90 /tmp/kd-body)"; \
	done

docker:
	docker compose build
