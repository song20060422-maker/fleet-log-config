# test/evidence — 测试与核查证据

| 文件 | 内容 | 生成方式 |
|------|------|----------|
| `constraint_report.md` / `constraint_report.json` | 自编 Skill `fleet-log-config-check` 业务约束核查报告（PASS 20 / FAIL 0 / SKIP 0，退出码 0） | `py <skill>/scripts/fleet_constraint_check.py --input state.json --out constraint_report.md --json constraint_report.json` |
| `state.json` | 五组工作场景执行后的业务状态导出（`GET /api/state/export`，Schema 与 Skill 输入一致） | `hack/smoke.ps1` |
| `api_probe.md` | 实时拒绝类探测记录（自审批、绕过推广、跨车回执、未知版本、停止后迟到回执、未确认恢复完成等，均被拒绝且留痕） | `hack/smoke.ps1` |
| `coverage.md` | 分包覆盖率门禁结果（controller/logic/model/store 与整体均 ≥85%） | `bash hack/coverage.sh 85` |

## 复现方式

```powershell
# 1. 清空库并启动服务
Remove-Item -Recurse -Force data; go build -o fleet.exe .; ./fleet.exe
# 2. 驱动五组场景并导出证据
powershell -ExecutionPolicy Bypass -File hack/smoke.ps1
# 3. 业务约束核查
py <skill目录>/scripts/fleet_constraint_check.py --input test/evidence/state.json --out test/evidence/constraint_report.md --json test/evidence/constraint_report.json
# 4. 覆盖率门禁（Linux/WSL）
bash hack/coverage.sh 85
# 5. 单测 + 竞态 + vet
go test ./... -count=1 -race; go vet ./...
```

## 发现→修正→重新检查闭环（共同要求第 8 节）

- 第 1 次核查（2026-11-13）：`C17 恢复版本目标值=活动前值` FAIL —— 恢复下发误用当前活动基线（30 秒，来自上一活动生效值），而非车辆活动前设置（60 秒）。
- 修正：`internal/logic/activity.go` `PublishRestore` 改为按车辆档案基线（60 秒）下发恢复版本（仍为更高版本）。
- 第 2 次核查：PASS 20 / FAIL 0 / SKIP 0，退出码 0，见本目录 `constraint_report.md`。
