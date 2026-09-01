#!/usr/bin/env bash
# End-to-end smoke test: boots the real example backend and the real grpcmcp
# binary (not the unit-test harness), and exercises both transports over a
# live HTTP connection. Exercises exactly what a deployed pod does.
set -uo pipefail
cd "$(dirname "$0")/.."

PASS=0
FAIL=0
check() {
  local desc="$1" got="$2" want="$3"
  if [[ "$got" == "$want" ]]; then
    echo "  ok   - $desc"
    PASS=$((PASS+1))
  else
    echo "  FAIL - $desc (got: $got, want: $want)"
    FAIL=$((FAIL+1))
  fi
}

WORKDIR=$(mktemp -d)
# Declared before the trap, with set -u active: if a later command exits
# before a PID is assigned (e.g. go build fails), the trap must still be able
# to reference it without an unbound-variable error aborting the trap itself
# mid-command -- which would skip rm -rf "$WORKDIR" on exactly the early
# failure the trap exists to clean up after.
BACKEND_PID=
HTTP_PID=
SSE_PID=
FWD_PID=
trap 'kill $BACKEND_PID $HTTP_PID $SSE_PID $FWD_PID 2>/dev/null; wait 2>/dev/null; rm -rf "$WORKDIR"' EXIT

echo "== building =="
go build -o "$WORKDIR/grpcmcp" . || exit 1
go build -o "$WORKDIR/example-backend" ./example || exit 1

echo "== starting example gRPC backend (:8090) =="
"$WORKDIR/example-backend" &
BACKEND_PID=$!
sleep 0.5

echo "== starting grpcmcp --transport=http (:8091) =="
"$WORKDIR/grpcmcp" --hostport=localhost:8091 --reflect --transport=http >"$WORKDIR/http.log" 2>&1 &
HTTP_PID=$!
sleep 0.5

echo "== starting grpcmcp --transport=sse (:8092) =="
"$WORKDIR/grpcmcp" --hostport=localhost:8092 --reflect --transport=sse >"$WORKDIR/sse.log" 2>&1 &
SSE_PID=$!
sleep 0.5

echo "== starting grpcmcp --forward-header=X-Forwarded-User (:8093) =="
"$WORKDIR/grpcmcp" --hostport=localhost:8093 --reflect --transport=http --forward-header=X-Forwarded-User >"$WORKDIR/fwd.log" 2>&1 &
FWD_PID=$!
sleep 0.5

if ! kill -0 "$BACKEND_PID" 2>/dev/null; then echo "backend failed to start"; cat "$WORKDIR"/*.log; exit 1; fi
if ! kill -0 "$HTTP_PID" 2>/dev/null; then echo "http server failed to start"; cat "$WORKDIR/http.log"; exit 1; fi
if ! kill -0 "$SSE_PID" 2>/dev/null; then echo "sse server failed to start"; cat "$WORKDIR/sse.log"; exit 1; fi
if ! kill -0 "$FWD_PID" 2>/dev/null; then echo "forward-header server failed to start"; cat "$WORKDIR/fwd.log"; exit 1; fi

INIT_BODY='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"e2e","version":"1"}}}'
LIST_BODY='{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
CALL_BODY='{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"Echo","arguments":{"message":"hello-e2e"}}}'

echo
echo "== Streamable HTTP (:8091) =="

status=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:8091/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -d "$INIT_BODY")
check "POST /mcp initialize -> 200" "$status" "200"

status=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:8091/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -d "$LIST_BODY")
check "POST /mcp tools/list -> 200" "$status" "200"

call_result=$(curl -s -X POST http://localhost:8091/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -d "$CALL_BODY")
if echo "$call_result" | grep -q 'hello-e2e'; then
  echo "  ok   - tools/call Echo round-trips a real argument through the backend"
  PASS=$((PASS+1))
else
  echo "  FAIL - tools/call Echo did not echo the argument back: $call_result"
  FAIL=$((FAIL+1))
fi

status=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8091/mcp)
check "GET /mcp (stateless, no stream) -> 405" "$status" "405"

status=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:8091/mcp \
  -H 'Content-Type: application/json' -H 'Origin: https://evil.example' -d "$INIT_BODY")
check "POST /mcp with Origin header -> 403" "$status" "403"

status=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:8091/mcp \
  -H 'Content-Type: application/json' -H 'Mcp-Protocol-Version: 1999-01-01' -d "$INIT_BODY")
check "POST /mcp with bogus Mcp-Protocol-Version -> 400" "$status" "400"

echo
echo
echo "== --forward-header=X-Forwarded-User (:8093) =="

curl -s -X POST http://localhost:8093/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d "$INIT_BODY" > /dev/null

fwd_result=$(curl -s -X POST http://localhost:8093/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -H 'X-Forwarded-User: alice@basiccapital.com' \
  -d "$CALL_BODY")
if echo "$fwd_result" | grep -q 'hello-e2e|alice@basiccapital.com'; then
  echo "  ok   - X-Forwarded-User reached the backend over a real gRPC call"
  PASS=$((PASS+1))
else
  echo "  FAIL - X-Forwarded-User did not reach the backend: $fwd_result"
  FAIL=$((FAIL+1))
fi

echo "== SSE (:8092, deprecated) =="

if grep -q '\[deprecated\].*transport=sse' "$WORKDIR/sse.log"; then
  echo "  ok   - deprecation warning printed to stderr"
  PASS=$((PASS+1))
else
  echo "  FAIL - deprecation warning missing from stderr"
  FAIL=$((FAIL+1))
  cat "$WORKDIR/sse.log"
fi

content_type=$(curl -s -m 1 -D - -o /dev/null http://localhost:8092/sse | grep -i '^content-type:' | tr -d '\r')
case "$content_type" in
  *text/event-stream*) echo "  ok   - GET /sse -> text/event-stream"; PASS=$((PASS+1));;
  *) echo "  FAIL - GET /sse content-type: $content_type"; FAIL=$((FAIL+1));;
esac

status=$(curl -s -m 1 -o /dev/null -w '%{http_code}' -H 'Origin: https://evil.example' http://localhost:8092/sse)
check "GET /sse with Origin header -> 403" "$status" "403"

echo
echo "== results: $PASS passed, $FAIL failed =="
[[ "$FAIL" -eq 0 ]]
