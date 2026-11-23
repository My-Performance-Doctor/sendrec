#!/bin/sh
# Build step (design D3). Builds the image for $REVISION once, pushes it to staging's sendrec
# repository as :$REVISION and prints its digest. If the tag is already there, as when the
# pipeline restarts after updating itself, it prints that tag's digest and builds nothing: the
# repository's tags are immutable, so the tag cannot name another image. Run from the
# repository root. Only the digest goes to stdout.
set -eu
REGION=ap-southeast-2
REPOSITORY=sendrec
: "${REVISION:?REVISION must name the commit}"
: "${STAGING_REGISTRY:?STAGING_REGISTRY must name staging's registry}"
if ! printf '%s' "$REVISION" | grep -Eqx '[0-9a-f]{40}'; then
  echo "REVISION must be a full commit SHA" >&2
  exit 1
fi
errors=$(mktemp)
trap 'rm -f "$errors"' EXIT

image_digest() {
  aws ecr describe-images --region "$REGION" --registry-id "${STAGING_REGISTRY%%.*}" \
    --repository-name "$REPOSITORY" --image-ids "imageTag=$REVISION" \
    --query 'imageDetails[0].imageDigest' --output text 2>"$errors"
}

if digest=$(image_digest); then
  echo "Reusing $REPOSITORY:$REVISION, already in staging's repository" >&2
elif grep -q ImageNotFoundException "$errors"; then
  aws ecr get-login-password --region "$REGION" \
    | docker login --username AWS --password-stdin "$STAGING_REGISTRY" >&2
  image="$STAGING_REGISTRY/$REPOSITORY:$REVISION"
  docker build --platform linux/amd64 --build-arg "VERSION=$(printf '%s' "$REVISION" | cut -c1-8)" \
    --label "org.opencontainers.image.revision=$REVISION" -t "$image" . >&2
  docker push "$image" >&2
  digest=$(image_digest) || { cat "$errors" >&2; exit 1; }
else
  cat "$errors" >&2
  exit 1
fi
if ! printf '%s' "$digest" | grep -Eqx 'sha256:[0-9a-f]{64}'; then
  echo "Staging's repository gave no digest for $REPOSITORY:$REVISION" >&2
  exit 1
fi
echo "$digest"
