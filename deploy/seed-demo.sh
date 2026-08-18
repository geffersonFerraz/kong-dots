#!/bin/sh
# Seeds the demo Kong with a topology that answers with canned JSON.
#
# Every route carries a `request-termination` plugin, so the gateway replies
# straight away with a dummy body and no real upstream has to exist. Re-running
# the script is safe: entities use fixed names/ids and are upserted.
set -eu

KONG_ADMIN="${KONG_ADMIN:-http://localhost:8001}"
DOTS_API="${DOTS_API:-}"

log() { echo "[seed] $*"; }

wait_for() {
  i=0
  until curl -sf -o /dev/null "$1"; do
    i=$((i + 1))
    if [ "$i" -gt 60 ]; then
      log "timed out waiting for $1"
      return 1
    fi
    sleep 2
  done
}

# upsert <path> <json> — PUT creates or replaces, so the script is idempotent.
upsert() {
  code=$(curl -sS -o /tmp/out -w '%{http_code}' -X PUT "$KONG_ADMIN$1" \
    -H 'Content-Type: application/json' -d "$2")
  case "$code" in
    2*) log "ok   $1" ;;
    *) log "FAIL $1 -> $code $(cat /tmp/out)"; exit 1 ;;
  esac
}

# plugin <parent path> <fixed uuid> <json> — plugins have no natural key, so the
# id is pinned and the previous copy removed before recreating it.
plugin() {
  curl -sS -o /dev/null -X DELETE "$KONG_ADMIN/plugins/$2" || true
  code=$(curl -sS -o /tmp/out -w '%{http_code}' -X POST "$KONG_ADMIN$1/plugins" \
    -H 'Content-Type: application/json' -d "$(printf '%s' "$3" | sed "s|^{|{\"id\":\"$2\",|")")
  case "$code" in
    2*) log "ok   plugin $2 on ${1:-global}" ;;
    *) log "FAIL plugin $2 -> $code $(cat /tmp/out)"; exit 1 ;;
  esac
}

log "waiting for Kong at $KONG_ADMIN"
wait_for "$KONG_ADMIN/"

# ---------------------------------------------------------------- services ---
upsert /services/orders-api   '{"host":"orders.demo.internal","port":8080,"protocol":"http","path":"/","tags":["demo"]}'
upsert /services/users-api    '{"host":"users.demo.internal","port":8080,"protocol":"http","path":"/","tags":["demo"]}'
# host is the Upstream name, which is how Kong links a Service to a load balancer.
upsert /services/payments-api '{"host":"payments-pool","port":8080,"protocol":"http","path":"/","tags":["demo"]}'

# ------------------------------------------------------------------ routes ---
upsert /routes/orders-list      '{"service":{"name":"orders-api"},"methods":["GET"],"paths":["/orders"],"strip_path":false,"tags":["demo"]}'
upsert /routes/orders-create    '{"service":{"name":"orders-api"},"methods":["POST"],"paths":["/orders"],"strip_path":false,"tags":["demo"]}'
upsert /routes/users-me         '{"service":{"name":"users-api"},"methods":["GET"],"paths":["/users/me"],"strip_path":false,"tags":["demo"]}'
upsert /routes/users-admin      '{"service":{"name":"users-api"},"methods":["GET"],"paths":["/users/admin"],"strip_path":false,"tags":["demo"]}'
upsert /routes/payments-status  '{"service":{"name":"payments-api"},"methods":["GET"],"paths":["/payments/status"],"strip_path":false,"tags":["demo"]}'

# ------------------------------------------------- dummy responses per route ---
plugin /routes/orders-list 0a000000-0000-4000-8000-000000000001 \
  '{"name":"request-termination","config":{"status_code":200,"content_type":"application/json; charset=utf-8","body":"{\"data\":[{\"id\":\"ord_1001\",\"customer\":\"mobile-app\",\"total\":42.5,\"status\":\"paid\"},{\"id\":\"ord_1002\",\"customer\":\"partner-portal\",\"total\":18.9,\"status\":\"pending\"}],\"page\":1,\"total\":2}"}}'

plugin /routes/orders-create 0a000000-0000-4000-8000-000000000002 \
  '{"name":"request-termination","config":{"status_code":201,"content_type":"application/json; charset=utf-8","body":"{\"id\":\"ord_1003\",\"status\":\"created\",\"note\":\"dummy response from request-termination\"}"}}'

plugin /routes/users-me 0a000000-0000-4000-8000-000000000003 \
  '{"name":"request-termination","config":{"status_code":200,"content_type":"application/json; charset=utf-8","body":"{\"id\":\"usr_42\",\"username\":\"mobile-app\",\"roles\":[\"reader\"]}"}}'

plugin /routes/users-admin 0a000000-0000-4000-8000-000000000004 \
  '{"name":"request-termination","config":{"status_code":200,"content_type":"application/json; charset=utf-8","body":"{\"id\":\"usr_1\",\"username\":\"root\",\"roles\":[\"admin\"]}"}}'

plugin /routes/payments-status 0a000000-0000-4000-8000-000000000005 \
  '{"name":"request-termination","config":{"status_code":503,"content_type":"application/json; charset=utf-8","body":"{\"error\":\"payments provider under maintenance\",\"retry_after\":120}"}}'

# ------------------------------------------- plugins that run before the dummy ---
# key-auth (priority 1250) beats request-termination (2), so /users/admin answers
# 401 until a credential is presented.
plugin /routes/users-admin 0a000000-0000-4000-8000-000000000006 '{"name":"key-auth"}'
plugin /routes/payments-status 0a000000-0000-4000-8000-000000000007 \
  '{"name":"rate-limiting","config":{"minute":5,"policy":"local"}}'
plugin /services/orders-api 0a000000-0000-4000-8000-000000000008 \
  '{"name":"request-size-limiting","config":{"allowed_payload_size":8}}'
plugin "" 0a000000-0000-4000-8000-000000000009 '{"name":"correlation-id"}'

# --------------------------------------------------------------- consumers ---
upsert /consumers/mobile-app     '{"custom_id":"app-ios-android","tags":["demo"]}'
upsert /consumers/partner-portal '{"custom_id":"partner-b2b","tags":["demo"]}'

# A consumer-scoped dummy response: this partner always gets a quota error.
plugin /consumers/partner-portal 0a000000-0000-4000-8000-00000000000a \
  '{"name":"request-termination","config":{"status_code":429,"content_type":"application/json; charset=utf-8","body":"{\"error\":\"quota exceeded for partner-portal\"}"}}'

# ---------------------------------------------------- upstream and targets ---
upsert /upstreams/payments-pool '{"algorithm":"round-robin","slots":10000,"tags":["demo"]}'
for t in 0a000000-0000-4000-8000-0000000000b1:10.10.0.11:8080:100 \
         0a000000-0000-4000-8000-0000000000b2:10.10.0.12:8080:50; do
  id=${t%%:*}; rest=${t#*:}; addr=${rest%:*}; weight=${rest##*:}
  curl -sS -o /dev/null -X DELETE "$KONG_ADMIN/upstreams/payments-pool/targets/$id" || true
  curl -sS -o /dev/null -X POST "$KONG_ADMIN/upstreams/payments-pool/targets" \
    -H 'Content-Type: application/json' \
    -d "{\"id\":\"$id\",\"target\":\"$addr\",\"weight\":$weight}"
  log "ok   target $addr"
done

log "Kong seeded. Try: curl http://localhost:8000/orders"

# ------------------------------------- register this Kong inside Kong Dots ---
if [ -n "$DOTS_API" ] && curl -sf -o /dev/null "$DOTS_API/healthz"; then
  if curl -sf "$DOTS_API/api/connections" | grep -q '"name":"Demo Kong"'; then
    log "workspace 'Demo Kong' already registered"
  else
    curl -sS -o /dev/null -X POST "$DOTS_API/api/connections" -H 'Content-Type: application/json' \
      -d '{"name":"Demo Kong","admin_api_url":"http://kong:8001","auth_type":"none","environment":"dev","tags":"demo"}'
    log "registered workspace 'Demo Kong' in Kong Dots"
  fi
else
  log "Kong Dots not reachable — skipping workspace registration"
fi
