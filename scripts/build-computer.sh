#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
output_dir=${1:-.eino-cli/bin}
mkdir -p "$output_dir"
go build -o "$output_dir/deepagent-worker" ./cmd/deepagent_worker
swiftc -parse-as-library -swift-version 5 -framework AppKit -framework ApplicationServices -framework ScreenCaptureKit deepagent/graph/computer/native/main.swift -o "$output_dir/deepagent-computer"
