# skill-usage.md — 自编 Skill 调用记录

> 本文件登记每次自编 Skill 的真实调用：输入、时间、结果与后续动作，供验收 Agent 复核。

| 日期 | Skill | 触发阶段 | 输入 | 结果 | 后续动作 |
|------|-------|----------|------|------|----------|
| 2026-11-13 | fleet-log-config-check | 开发自测完成后（提交前） | 运行中服务导出 `test/evidence/state.json`（含五组场景全量业务状态）+ `test/evidence/api_probe.md` 实时探测 | 第 1 次核查：C17 FAIL（恢复版本目标值取当前活动基线 30，应为车辆活动前设置 60）；修复 `PublishRestore` 改用车辆档案基线后重跑：**PASS 20 / FAIL 0 / SKIP 0**（`test/evidence/constraint_report.md`，退出码 0） | 缺陷已修复并复测（发现→修正→重新检查闭环，前后报告见 test/evidence/） |
| 2026-11-13 | fleet-log-acceptance | 提交前正式验收 | 题面原文、共同要求、仓库/PR、测试证据、部署证据 | 见 `acceptance/acceptance-report.md` | 验收结论随报告给出；未通过项进入整改 |

## 调用说明

- 两次调用均覆盖五组工作场景对应的检查项（正常推广 / 审批与试点分支 / 恢复分支 / 网络分支 / 结果核查）。
- 实时拒绝类检查通过 `hack/smoke.ps1` 对运行中服务实际执行（自审批、绕过推广、跨车回执、未知版本、停止后迟到回执、未确认恢复完成等），结果归档于 `test/evidence/api_probe.md`。
- 约束核查脚本：`py <skill>/scripts/fleet_constraint_check.py --input test/evidence/state.json --out test/evidence/constraint_report.md --json test/evidence/constraint_report.json`。
