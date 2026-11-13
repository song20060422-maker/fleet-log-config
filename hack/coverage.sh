#!/usr/bin/env bash
# 分包覆盖率统计（跨包并集）：各包独立测试 + 跨包 coverpkg 精确包名组合，合并后由 coverstat 执行门禁。
# 用法：bash hack/coverage.sh [最低覆盖率，默认 85]
set -euo pipefail
cd "$(dirname "$0")/.."

MIN="${1:-85}"
OUT_DIR="$(mktemp -d)"
MODULE="$(go list -m)"

run() { go test "$1" -count=1 ${2:+-coverpkg="$2"} -coverprofile="$OUT_DIR/$3" >/dev/null; }

run ./internal/logic  ""                            "L.out"
run ./internal/logic  "$MODULE/internal/store"      "LS.out"
run ./internal/logic  "$MODULE/internal/model"      "LM.out"
run ./internal/controller ""                       "C.out"
run ./internal/controller "$MODULE/internal/logic"  "CL.out"
run ./internal/controller "$MODULE/internal/store"  "CS.out"
run ./internal/controller "$MODULE/internal/model"  "CM.out"
run ./internal/store  ""                            "S.out"
run ./internal/model  ""                            "M.out"

PROFILES="$OUT_DIR/L.out,$OUT_DIR/LS.out,$OUT_DIR/LM.out,$OUT_DIR/C.out,$OUT_DIR/CL.out,$OUT_DIR/CS.out,$OUT_DIR/CM.out,$OUT_DIR/S.out,$OUT_DIR/M.out"
go run ./hack/coverstat -profile "$PROFILES" -min "$MIN"
