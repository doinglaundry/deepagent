#!/usr/bin/env bash
set -euo pipefail
: "${DEEPAGENT_TEST_MYSQL_DSN:?point to an isolated test MySQL database}"
: "${DEEPAGENT_TEST_REDIS_ADDR:?point to an isolated test Redis instance}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
test_bins="$(mktemp -d "${TMPDIR:-/tmp}/deepagent-test.XXXXXX")"
trap 'rm -rf "$test_bins"' EXIT
go build -o "$test_bins/deepagent" ./cmd/deepagent
go build -o "$test_bins/deepagent_worker" ./cmd/deepagent_worker
export DEEPAGENT_TEST_CLI="$test_bins/deepagent"
export DEEPAGENT_TEST_WORKER="$test_bins/deepagent_worker"
go test -race -count=1 ./manager/... ./deepagent/core/... ./worker/... ./host/...
