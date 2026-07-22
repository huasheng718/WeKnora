#!/usr/bin/env bash
set -euo pipefail

if [[ "${PLAYWRIGHT_PRODUCTION_QA:-}" != "1" || "${PLAYWRIGHT_QA_BACKEND_MODE:-}" != "ephemeral-sqlite" ]]; then
  echo "Refusing to start without explicit ephemeral production-QA opt-in." >&2
  exit 1
fi

data_dir="${PLAYWRIGHT_QA_DATA_DIR:?PLAYWRIGHT_QA_DATA_DIR is required}"
temporary_root="${TMPDIR:-/tmp}"
temporary_root="${temporary_root%/}"
case "${data_dir}/" in
  "${temporary_root}/"*/ ) ;;
  *)
    echo "Refusing to manage a QA data directory outside ${temporary_root}." >&2
    exit 1
    ;;
esac

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
backend_pid=""
cleanup() {
  trap - EXIT INT TERM
  if [[ -n "$backend_pid" ]]; then
    kill "$backend_pid" 2>/dev/null || true
    wait "$backend_pid" 2>/dev/null || true
  fi
  rm -rf "$data_dir"
}
trap cleanup EXIT INT TERM

rm -rf "$data_dir"
mkdir -p "$data_dir/files"

cd "$repo_root"
export DB_DRIVER=sqlite
export DB_PATH="$data_dir/weknora.db"
export RETRIEVE_DRIVER=sqlite
export STORAGE_TYPE=local
export LOCAL_STORAGE_BASE_DIR="$data_dir/files"
export STREAM_MANAGER_TYPE=memory
export SERVER_HOST=127.0.0.1
export SERVER_PORT="${PLAYWRIGHT_BACKEND_PORT:?PLAYWRIGHT_BACKEND_PORT is required}"
export DISABLE_REGISTRATION=false

go run -tags sqlite_fts5 ./cmd/server &
backend_pid=$!
wait "$backend_pid"
