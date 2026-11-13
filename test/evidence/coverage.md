# 分包覆盖率门禁（跨包并集）

生成时间：2026-11-14 00:15  命令：$env:TEMP 下 9 份 profile 经 hack/coverstat 合并（等价 hack/coverage.sh，Windows 无 bash 用 PowerShell 执行同一矩阵）

``\nPACKAGE                                                   STMTS   COVERAGE
fleet/internal/controller                                   255      85.5%
fleet/internal/logic                                        940      85.4%
fleet/internal/model                                          8     100.0%
fleet/internal/store                                        215      90.7%
TOTAL                                                      1418      86.3%
覆盖率门禁通过：各包与整体均 >= 85.0%
```n门禁：各包与整体 >= 85%（退出码 0）
