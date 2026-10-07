#!/bin/sh
set -eu
GARAGE_KEYS_FILE="${GARAGE_KEYS_FILE:-/run/garage-keys/env}"
if [ -f "${GARAGE_KEYS_FILE}" ]; then
  set -a
  . "${GARAGE_KEYS_FILE}"
  set +a
fi

# Compose DATABASE_URL from parts when a secrets manager injects the
# credentials as separate fields (AWS Secrets Manager via ECS does this).
if [ -z "${DATABASE_URL:-}" ] && [ -n "${DB_HOST:-}" ]; then
  for field in DB_USER DB_PASSWORD DB_NAME; do
    # Only fixed field names reach eval, never a credential value.
    eval "present=\${${field}:-}"
    if [ -z "$present" ]; then
      echo "Missing required database field: $field" >&2
      exit 1
    fi
  done
  case "$DB_HOST" in *[!a-zA-Z0-9.-]*) echo "Invalid DB_HOST" >&2; exit 1;; esac
  DB_PORT="${DB_PORT:-5432}"
  DB_SSLMODE="${DB_SSLMODE:-require}"
  case "$DB_PORT" in ''|*[!0-9]*) echo "Invalid DB_PORT" >&2; exit 1;; esac
  if [ "$DB_PORT" -lt 1 ] || [ "$DB_PORT" -gt 65535 ]; then
    echo "Invalid DB_PORT" >&2; exit 1
  fi
  case "$DB_SSLMODE" in disable|allow|prefer|require|verify-ca|verify-full) ;; *) echo "Invalid DB_SSLMODE" >&2; exit 1;; esac
  percent_encode() { od -An -v -tx1 | tr -d ' \n' | sed 's/../%&/g'; }
  db_user_encoded=$(printf '%s' "$DB_USER" | percent_encode)
  db_password_encoded=$(printf '%s' "$DB_PASSWORD" | percent_encode)
  db_name_encoded=$(printf '%s' "$DB_NAME" | percent_encode)
  DATABASE_URL="postgres://${db_user_encoded}:${db_password_encoded}@${DB_HOST}:${DB_PORT}/${db_name_encoded}?sslmode=${DB_SSLMODE}"
  export DATABASE_URL
fi

# Fetch the local Whisper model on first start when the image has none and
# there is no init container to do it (ECS Fargate, plain Docker).
if [ "${TRANSCRIPTION_ENABLED:-false}" = "true" ] && [ "${TRANSCRIPTION_PROVIDER:-local}" = "local" ] \
   && [ -n "${WHISPER_MODEL_URL:-}" ]; then
  if [ -z "${WHISPER_MODEL_PATH:-}" ] || [ -z "${WHISPER_MODEL_SHA256:-}" ]; then
    echo "Model download requires WHISPER_MODEL_PATH and WHISPER_MODEL_SHA256" >&2
    exit 1
  fi
  if [ ! -f "$WHISPER_MODEL_PATH" ]; then
    mkdir -p "$(dirname "$WHISPER_MODEL_PATH")"
    model_part="${WHISPER_MODEL_PATH}.part.$$"
    trap 'rm -f "$model_part"' EXIT HUP INT TERM
    echo "Downloading pinned transcription model"
    if ! wget -q -T 60 -O "$model_part" "$WHISPER_MODEL_URL"; then
      echo "Transcription model download failed" >&2
      exit 1
    fi
    if ! printf '%s  %s\n' "$WHISPER_MODEL_SHA256" "$model_part" | sha256sum -c - >/dev/null 2>&1; then
      echo "Transcription model checksum failed" >&2
      exit 1
    fi
    mv "$model_part" "$WHISPER_MODEL_PATH"
    trap - EXIT HUP INT TERM
  elif ! printf '%s  %s\n' "$WHISPER_MODEL_SHA256" "$WHISPER_MODEL_PATH" | sha256sum -c - >/dev/null 2>&1; then
    echo "Cached transcription model checksum failed" >&2
    exit 1
  fi
fi

exec "$@"
