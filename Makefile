.PHONY: api ui serve build test test-backend test-frontend demo demo-kong demo-down demo-logs demo-check docker

# --- development ---------------------------------------------------------
# Backend only, on :8080. Pair with `make ui`, which proxies /api to it.
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

test-backend:
	cd backend && go test ./...

test-frontend:
	cd frontend && npm test

# --- demo stack ----------------------------------------------------------
# Kong 3.9.1 + demo topology (routes answer with canned JSON) + Kong Dots.
# UI: http://localhost:8080   Kong proxy: :8000   Kong Admin API: :8001
demo:
	docker compose -f deploy/demo.yml --profile app up -d --build

# Only Kong + the demo data, for use with `make api` / `make ui`.
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
