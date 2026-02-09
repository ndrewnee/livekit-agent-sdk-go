#!/usr/bin/env bash
set -euo pipefail

BIN="./livekit-cloud-example"
AGENT_NAME="${PUBLISHER_AGENT_NAME:-publisher-rejoin-agent}"
PUBLISHER_IDENTITY="${PUBLISHER_IDENTITY:-rejoin-publisher}"
PUBLISH_DURATION="${PUBLISH_DURATION:-5s}"
WAIT_FOR_JOB_END_SECONDS="${WAIT_FOR_JOB_END_SECONDS:-15}"
WAIT_FOR_JOBS_SECONDS="${WAIT_FOR_JOBS_SECONDS:-20}"
DISPATCH_MODE="${DISPATCH_MODE:-api}"
EMPTY_TIMEOUT="${EMPTY_TIMEOUT:-300}"
GOCACHE_DIR="${GOCACHE:-/tmp/gocache}"
KEEP_LOGS="${KEEP_LOGS:-0}"
ENV_FILE="${ENV_FILE:-.env}"

export GOCACHE="${GOCACHE_DIR}"
mkdir -p "${GOCACHE}"

cleanup() {
  if [[ -n "${AGENT_PID:-}" ]]; then
    kill "${AGENT_PID}" >/dev/null 2>&1 || true
    wait "${AGENT_PID}" >/dev/null 2>&1 || true
  fi

  if [[ -n "${ROOM_NAME:-}" ]]; then
    "${BIN}" delete-room --room "${ROOM_NAME}" >/dev/null 2>&1 || true
  fi

  if [[ "${KEEP_LOGS}" != "1" ]]; then
    rm -f agent_redispatch.log room.log pub1.log pub2.log dispatch1.log dispatch2.log >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

wait_for_count() {
  local file="$1"
  local pattern="$2"
  local min_count="$3"
  local timeout="$4"

  for ((i = 0; i < timeout; i++)); do
    local c
    c="$(grep -c "${pattern}" "${file}" 2>/dev/null || true)"
    if [[ "${c}" -ge "${min_count}" ]]; then
      return 0
    fi
    sleep 1
  done

  return 1
}

echo ""
echo "============================================================"
echo "JT_PUBLISHER rejoin redispatch e2e (expects 2 jobs)"
echo "============================================================"
echo ""

if [[ ! -f "${ENV_FILE}" ]]; then
  echo "✗ Missing ${ENV_FILE}."
  echo "  Create ${ENV_FILE} in this directory with LIVEKIT_URL/LIVEKIT_API_KEY/LIVEKIT_API_SECRET."
  exit 1
fi

LIVEKIT_URL_VALUE="$(grep -E '^LIVEKIT_URL=' "${ENV_FILE}" | head -n 1 | cut -d= -f2- || true)"
if [[ -z "${LIVEKIT_URL_VALUE}" ]]; then
  echo "✗ ${ENV_FILE} is missing LIVEKIT_URL."
  exit 1
fi

echo "➤ Using LIVEKIT_URL=${LIVEKIT_URL_VALUE}"

echo "➤ Building example binary..."
go build -o livekit-cloud-example .

echo "➤ Starting publisher agent (${AGENT_NAME})..."
"${BIN}" publisher-agent \
  --agent-name "${AGENT_NAME}" \
  --max-jobs 1 \
  --cancel-on-target-leave \
  > agent_redispatch.log 2>&1 &
AGENT_PID=$!

sleep 3
if ! kill -0 "${AGENT_PID}" >/dev/null 2>&1; then
  echo "✗ Agent failed to start"
  sed -n '1,200p' agent_redispatch.log || true
  exit 1
fi

echo "➤ Creating room with dispatch (${AGENT_NAME})..."
"${BIN}" create-room-publisher --agent-name "${AGENT_NAME}" --dispatch-mode "${DISPATCH_MODE}" --empty-timeout "${EMPTY_TIMEOUT}" > room.log 2>&1

if ! grep -q "Room created successfully" room.log; then
  echo "✗ Failed to create room"
  cat room.log || true
  exit 1
fi

ROOM_NAME="$(grep "Name:" room.log | awk '{print $2}')"
echo "  Room: ${ROOM_NAME}"

echo "➤ Publisher join #1 (identity=${PUBLISHER_IDENTITY}, duration=${PUBLISH_DURATION})..."
"${BIN}" publisher-client --room "${ROOM_NAME}" --identity "${PUBLISHER_IDENTITY}" --duration "${PUBLISH_DURATION}" > pub1.log 2>&1

echo "➤ Waiting for job #1 to end (${WAIT_FOR_JOB_END_SECONDS}s)..."
if ! wait_for_count agent_redispatch.log "JOB CONTEXT DONE:" 1 "${WAIT_FOR_JOB_END_SECONDS}"; then
  echo "✗ FAIL: job #1 did not end after publisher disconnect"
  echo ""
  echo "Last 200 lines of agent_redispatch.log:"
  tail -n 200 agent_redispatch.log || true
  exit 1
fi

echo "➤ Dispatch state after job #1 end..."
"${BIN}" list-dispatch --room "${ROOM_NAME}" > dispatch1.log 2>&1 || true

echo "➤ Publisher join #2 (rejoin, identity=${PUBLISHER_IDENTITY}, duration=${PUBLISH_DURATION})..."
"${BIN}" publisher-client --room "${ROOM_NAME}" --identity "${PUBLISHER_IDENTITY}" --duration "${PUBLISH_DURATION}" > pub2.log 2>&1

echo "➤ Waiting for job #2 to be dispatched (${WAIT_FOR_JOBS_SECONDS}s)..."
if ! wait_for_count agent_redispatch.log "JOB ASSIGNED:" 2 "${WAIT_FOR_JOBS_SECONDS}"; then
  echo "✗ FAIL: expected 2 JT_PUBLISHER jobs (publish + leave + rejoin)"
  echo ""
  echo "Last 200 lines of agent_redispatch.log:"
  tail -n 200 agent_redispatch.log || true
  echo ""
  echo "Dispatch state after job #1 end:"
  tail -n 200 dispatch1.log || true
  echo ""
  echo "Dispatch state after join #2:"
  "${BIN}" list-dispatch --room "${ROOM_NAME}" > dispatch2.log 2>&1 || true
  tail -n 200 dispatch2.log || true
  exit 1
fi

echo "➤ Dispatch state after job #2..."
"${BIN}" list-dispatch --room "${ROOM_NAME}" > dispatch2.log 2>&1 || true

JOB_REQUESTS="$(grep -c "Job Type: JT_PUBLISHER" agent_redispatch.log || true)"
JOB_ASSIGNED="$(grep -c "JOB ASSIGNED:" agent_redispatch.log || true)"
JOB_ENDED="$(grep -c "JOB CONTEXT DONE:" agent_redispatch.log || true)"

echo ""
echo "------------------------------------------------------------"
echo "Agent log summary"
echo "------------------------------------------------------------"
echo "Job requests: ${JOB_REQUESTS}"
echo "Jobs assigned: ${JOB_ASSIGNED}"
echo "Jobs ended: ${JOB_ENDED}"
echo ""

echo "✓ PASS: got a new JT_PUBLISHER job on rejoin"
