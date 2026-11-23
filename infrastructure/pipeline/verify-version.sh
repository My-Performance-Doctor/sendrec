#!/bin/sh
# Verify step (design D6). Waits until $BASE_URL/api/health reports the release's version,
# for up to ATTEMPTS x DELAY seconds (5 minutes by default), and fails if it never does.
set -eu
: "${BASE_URL:?BASE_URL must name the stack's public URL}"
: "${RELEASE_FILE:?RELEASE_FILE must name sendrec-release.json}"
attempts=${ATTEMPTS:-20}
delay=${DELAY:-15}
expected=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["version"])' "$RELEASE_FILE")
version=""
tries=0
while [ "$tries" -lt "$attempts" ]; do
  version=$(curl -fsS --max-time 10 "$BASE_URL/api/health" 2>/dev/null \
    | python3 -c 'import json, sys; print(json.load(sys.stdin).get("version", ""))' 2>/dev/null) || version=""
  if [ "$version" = "$expected" ]; then
    echo "$BASE_URL serves $expected"
    exit 0
  fi
  tries=$((tries + 1))
  [ "$tries" -lt "$attempts" ] && sleep "$delay"
done
echo "$BASE_URL still serves '${version:-nothing}', not $expected" >&2
exit 1
