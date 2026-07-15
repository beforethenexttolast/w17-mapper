#!/usr/bin/env bash
# SPDX-FileCopyrightText: © 2023 OneEyeFPV oneeyefpv@gmail.com
# SPDX-License-Identifier: GPL-3.0-or-later
# SPDX-License-Identifier: FS-0.9-or-later
#
# Reproducible regeneration of the JoystickControl gRPC stubs from server.proto.
# W17 owned-fork addition: this documents the EXACT pinned generator versions and
# the exact invocation, so `git diff` after a regen is empty unless server.proto
# actually changed. It generates BOTH the Go server stubs and the grpc-web JS
# client stubs (the mapper's own web UI). It does NOT install or commit any tool
# binaries.
#
# Pinned toolchain (must match the versions recorded in the generated headers):
#   protoc                v4.23.2   (protobuf release v23.2)
#   protoc-gen-go         v1.30.0   go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.30.0
#   protoc-gen-go-grpc    v1.3.0    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.3.0
#   protoc-gen-js         v3.21.2   github.com/protocolbuffers/protobuf-javascript releases
#   protoc-gen-grpc-web   v1.4.2    github.com/grpc/grpc-web releases
#
# The three-line SPDX header at the top of every generated file is NOT prepended
# here: protoc-gen-* copies it verbatim from server.proto's own leading comment.
#
# Usage: put the five tools on PATH, then run this script from anywhere:
#   ./pkg/proto/generate.sh
set -euo pipefail

want_protoc="libprotoc 23.2"
want_go="protoc-gen-go v1.30.0"
want_gogrpc="protoc-gen-go-grpc 1.3.0"
want_grpcweb="1.4.2"

need() { command -v "$1" >/dev/null 2>&1 || { echo "ERROR: '$1' not on PATH" >&2; exit 1; }; }
for t in protoc protoc-gen-go protoc-gen-go-grpc protoc-gen-js protoc-gen-grpc-web; do need "$t"; done

check() { # check <label> <got> <want>
  if [ "$2" != "$3" ]; then
    echo "ERROR: $1 version mismatch: got '$2', want '$3'" >&2
    echo "       (installing a different version will churn the generated code)" >&2
    exit 1
  fi
}
check protoc              "$(protoc --version)"             "$want_protoc"
check protoc-gen-go       "$(protoc-gen-go --version)"      "$want_go"
check protoc-gen-go-grpc  "$(protoc-gen-go-grpc --version)" "$want_gogrpc"
check protoc-gen-grpc-web "$(protoc-gen-grpc-web --version | awk '{print $2}')" "$want_grpcweb"

cd "$(dirname "$0")" # pkg/proto
JS_OUT="../../webapp/src/generated"

echo "→ Go server stubs → generated/pb/"
protoc -I ../third-party --proto_path=. \
  --go_out=generated/pb --go_opt=paths=source_relative \
  --go-grpc_out=generated/pb --go-grpc_opt=paths=source_relative \
  server.proto

echo "→ grpc-web JS client stubs → ${JS_OUT}/"
protoc -I ../third-party --proto_path=. \
  --js_out=import_style=commonjs,binary:"${JS_OUT}" \
  --grpc-web_out=import_style=commonjs,mode=grpcwebtext:"${JS_OUT}" \
  server.proto

echo "✓ regeneration complete"
