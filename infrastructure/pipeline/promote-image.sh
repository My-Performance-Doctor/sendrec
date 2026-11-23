#!/bin/sh
# PromoteImage step (design D4). Copies the release's digest from staging's sendrec repository
# into production's as :<commit>, keeping the digest, and checks the copy. Production is never
# built from source. A tag that already names the same digest is left alone; one that names
# another digest fails the step. $RELEASE_FILE is sendrec-release.json from the cloud assembly.
set -eu
REGION=ap-southeast-2
: "${RELEASE_FILE:?RELEASE_FILE must name sendrec-release.json}"
CRANE=${CRANE:-crane}

field() {
  python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' "$RELEASE_FILE" "$1"
}
revision=$(field revision)
digest=$(field digest)
staging_image=$(field stagingImage)
production_image=$(field productionImage)
if ! printf '%s' "$revision" | grep -Eqx '[0-9a-f]{40}' \
  || ! printf '%s' "$digest" | grep -Eqx 'sha256:[0-9a-f]{64}' \
  || [ "${staging_image#*@}" != "$digest" ] || [ "${production_image#*@}" != "$digest" ]; then
  echo "$RELEASE_FILE does not describe one release" >&2
  exit 1
fi
staging_repository=${staging_image%@*}
production_repository=${production_image%@*}

for registry in "${staging_repository%%/*}" "${production_repository%%/*}"; do
  aws ecr get-login-password --region "$REGION" | "$CRANE" auth login "$registry" -u AWS --password-stdin
done

target="$production_repository:$revision"
errors=$(mktemp)
trap 'rm -f "$errors"' EXIT
if existing=$("$CRANE" digest "$target" 2>"$errors"); then
  if [ "$existing" != "$digest" ]; then
    echo "$target already names $existing, not $digest" >&2
    exit 1
  fi
  echo "Production already holds $target at $digest"
  exit 0
elif ! grep -q MANIFEST_UNKNOWN "$errors"; then
  # Anything but "no such tag", such as a missing repository policy, stops here with its own message.
  cat "$errors" >&2
  exit 1
fi
"$CRANE" copy "$staging_repository@$digest" "$target"
copied=$("$CRANE" digest "$target")
if [ "$copied" != "$digest" ]; then
  echo "The copy of $target has digest $copied, not $digest" >&2
  exit 1
fi
echo "Copied $digest to $target"
