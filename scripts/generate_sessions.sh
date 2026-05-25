#!/usr/bin/env bash
# generate_sessions.sh — 在壓力測試前預先登入，把 session cookie 存入 CSV。
#
# 使用方式：
#   BASE_URL=https://example.com \
#   LOGIN_PATH=/auth/login \
#   COOKIE_NAME=session \
#   USER_PREFIX=loadtest \
#   PASSWORD=testpass123 \
#   COUNT=200 \
#   ./scripts/generate_sessions.sh
#
# 輸出：sessions.csv（格式：session_cookie,user_id）
#
# 注意：
#   - 生產環境請使用專用測試帳號（loadtest_001@example.com ... loadtest_N@example.com）
#   - 若需要 CAPTCHA，請先在測試環境設定 bypass，或改用下方的 2captcha 範例
#   - 此腳本假設登入 endpoint 回傳 Set-Cookie: <COOKIE_NAME>=<value>; ...

set -euo pipefail

BASE_URL="${BASE_URL:-https://example.com}"
LOGIN_PATH="${LOGIN_PATH:-/auth/login}"
COOKIE_NAME="${COOKIE_NAME:-session}"
USER_PREFIX="${USER_PREFIX:-loadtest}"
PASSWORD="${PASSWORD:-testpass123}"
COUNT="${COUNT:-100}"
OUTPUT="${OUTPUT:-sessions.csv}"

echo "session_cookie,user_id" > "$OUTPUT"
success=0
failed=0

for i in $(seq -w 1 "$COUNT"); do
  username="${USER_PREFIX}_${i}@example.com"

  response=$(curl -si \
    --max-time 10 \
    -X POST "${BASE_URL}${LOGIN_PATH}" \
    -H "Content-Type: application/json" \
    -d "{\"email\":\"${username}\",\"password\":\"${PASSWORD}\"}" \
    2>/dev/null)

  http_status=$(echo "$response" | grep -m1 "^HTTP/" | awk '{print $2}')

  raw_cookie=$(echo "$response" | grep -i "^Set-Cookie:" | grep -i "${COOKIE_NAME}=" | head -1)
  cookie_value=$(echo "$raw_cookie" | grep -oP "${COOKIE_NAME}=\K[^;]+" 2>/dev/null || true)

  if [[ -z "$cookie_value" ]]; then
    echo "  [WARN] user=${username} status=${http_status} — no ${COOKIE_NAME} cookie found, skipping" >&2
    ((failed++)) || true
    continue
  fi

  echo "${COOKIE_NAME}=${cookie_value},${username}" >> "$OUTPUT"
  ((success++)) || true
done

echo ""
echo "Done: ${success} sessions written to ${OUTPUT}, ${failed} failed."
echo "Verify: head -3 ${OUTPUT}"
head -3 "$OUTPUT"
