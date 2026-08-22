.PHONY: api ui serve build test test-backend test-frontend test-db test-db-down demo demo-kong demo-down demo-logs demo-check docker

# --- development ---------------------------------------------------------
# Backend only, on :8080. Needs a PostgreSQL — `make demo-kong` starts one on
# :5432; point elsewhere with KONGFLOW_DATABASE_URL. Pair with `make ui`, which
# proxies /api to it.
api:
	cd backend && go run ./cmd/server

# Vite dev server with hot reload, on :5173. This is the URL you open.
ui:
	cd frontend && npm run dev

# Single-port alternative: build the UI and let the Go binary serve it on :8080.
serve: build
	KONGFLOW_STATIC_DIR=$(CURDIR)/frontend/dist ./bin/kong-flow

build:
	cd frontend && npm install && npm run build
	cd backend && go build -o $(CURDIR)/bin/kong-flow ./cmd/server

test: test-backend test-frontend

test-backend: test-db
	cd backend && go test ./...

# Throwaway PostgreSQL 18 for the backend tests, on :5433 so it never touches
# the database `make api` uses. Each test runs in a schema of its own.
test-db:
	@docker inspect -f '{{.State.Running}}' kongflow-test-db 2>/dev/null | grep -q true || { \
	  docker rm -f kongflow-test-db >/dev/null 2>&1; \
	  docker run -d --name kongflow-test-db \
	    -e POSTGRES_USER=kongflow -e POSTGRES_PASSWORD=kongflow -e POSTGRES_DB=kongflow_test \
	    -p 127.0.0.1:5433:5432 postgres:18-alpine >/dev/null; }
	@for i in $$(seq 30); do \
	  docker exec kongflow-test-db pg_isready -U kongflow -d kongflow_test >/dev/null 2>&1 && exit 0; \
	  sleep 1; \
	done; echo "kongflow-test-db never became ready" >&2; exit 1

test-db-down:
	docker rm -f kongflow-test-db >/dev/null 2>&1 || true

test-frontend:
	cd frontend && npm test

# --- demo stack ----------------------------------------------------------
# docker compose loads .env from the directory of the compose file, which for
# the demo stack is deploy/ — so the repo-root .env is ignored unless it is
# passed explicitly. Without this, KONGFLOW_BIND and the port overrides look
# like they work only because they match the defaults.
COMPOSE_DEMO := docker compose $(if $(wildcard .env),--env-file .env,) -f deploy/demo.yml

# Kong 3.9.1 + demo topology (routes answer with canned JSON) + Kong Flow.
# UI: http://localhost:8080   Kong proxy: :8000   Kong Admin API: :8001
demo:
	$(COMPOSE_DEMO) --profile app up -d --build

# Kong + the demo data + the Kong Flow database, for use with `make api` /
# `make ui` on the host.
demo-kong:
	$(COMPOSE_DEMO) up -d

demo-down:
	$(COMPOSE_DEMO) --profile app down -v

demo-logs:
	$(COMPOSE_DEMO) logs -f demo-seed kong-flow

# Hit every seeded route and show what the gateway answers.
demo-check:
	@for r in "GET /orders" "POST /orders" "GET /users/me" "GET /users/admin" "GET /payments/status"; do \
	  m=$${r%% *}; p=$${r#* }; \
	  printf '%-6s %-22s -> %s\n' "$$m" "$$p" \
	    "$$(curl -s -o /tmp/kd-body -w '%{http_code}' -X $$m http://localhost:8000$$p) $$(head -c 90 /tmp/kd-body)"; \
	done

docker:
	docker compose build
