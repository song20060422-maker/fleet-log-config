# fleet-log-config — 车队诊断日志配置发布与恢复（GoFrame 后端）

同一车队五台测试车辆的日志间隔发布场景：研发申请 → 负责人审批 → 运营试点（2 台）→ 确认推广（3 台）→ 车端回执确认 → 停止/恢复。期望值、实际生效值与失败原因逐车可解释，"下发 ≠ 生效"，旧回执不覆盖新配置。

配套前端：https://github.com/song20060422-maker/fleet-log-config-frontend （Vue3 + Vite，五页面原型）

## 快速运行

```bash
go run .            # 服务 :8000，MySQL（fleet_log 库）自动建表并种子（幂等）
```

- 数据库：MySQL 8.0，默认链接 `root:root@tcp(127.0.0.1:3307)/fleet_log`（见 `manifest/config/config.yaml`），
  可用环境变量 `FLEET_DB_LINK` 覆盖；库需预先创建（`CREATE DATABASE fleet_log DEFAULT CHARACTER SET utf8mb4`）。
- 单元测试使用纯 Go SQLite（无外部依赖）；MySQL 方言另有集成用例 `TestMySQLInitSeedAndUpsert`（设 `FLEET_TEST_MYSQL_LINK` 启用）。

前端：

```bash
git clone https://github.com/song20060422-maker/fleet-log-config-frontend
cd fleet-log-config-frontend && npm install && npm run dev   # http://localhost:5173
```

## 口径与预置

- 车队 `FLEET-01`，五台车 `VEH-01 ~ VEH-05`；日志间隔 10~300 秒，初始 60 秒；发布 60 秒未获有效回执显示超时（与测试同一规则路径，`manifest/config/config.yaml` 可调 `fleet.timeoutSeconds`）。
- 预置账号（口令见 `GET /api/meta` 的 `demo_password`）：`dev`（研发申请人）、`approver`（审批负责人）、`ops`（发布运营）、`vehicle-01 ~ vehicle-05`（模拟车辆，各自绑定车辆）。
- 模拟车辆协议：`GET /api/vehicle/config`（拉取面向自己的最新已发布版本）、`POST /api/vehicle/receipt`（成功/失败回执）、`GET /api/vehicle/state`（期望值/生效值/拉取记录）。
- 全量业务状态导出：`GET /api/state/export`（Schema 与自编 Skill `fleet-log-config-check` 输入一致）。

## 主要接口

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/auth/login` | 登录（返回 token） |
| POST/GET/PUT | `/api/applications[...]` | 申请创建/列表/详情/退回后修改 |
| POST | `/api/applications/:id/approve`、`/reject` | 审批（审批人不能是申请人） |
| POST/GET | `/api/activities[...]` | 创建活动（试点 2 台）/列表/详情 |
| GET | `/api/activities/:id/progress` | 逐车进度与前值/期望值/生效值/原因 |
| POST | `/api/activities/:id/promote` ` /stop` ` /retry-pilot` ` /complete` | 推广 / 停止 / 重试试点 / 记完成 |
| POST | `/api/activities/:id/restore/approve` ` /publish` ` /complete` | 批准恢复 / 下发恢复版本 / 记恢复完成 |
| GET | `/api/vehicle/config` | 车辆拉取配置 |
| POST | `/api/vehicle/receipt` | 车辆回执 |
| GET | `/api/vehicle/state` | 车辆自身状态 |
| GET | `/api/audit/events`、`/api/state/export` | 留痕 / 业务状态导出 |

## 质量门禁

```bash
go test ./... -count=1 -race   # 单测 + 竞态（Linux/CI）
bash hack/coverage.sh 85       # 分包覆盖率门禁（跨包并集，controller/logic/model/store ≥85%）
go vet ./... && gofmt -l .
powershell -File hack/smoke.ps1   # 端到端演示冒烟（五组场景 + 拒绝类探测 + 状态导出）
```

证据目录：`test/evidence/`（覆盖率门禁、业务约束核查 PASS 20/FAIL 0、实时探测留证）、`acceptance/acceptance-report.md`（验收报告）。

## 部署与回退

```bash
cd deploy && docker compose up -d --build   # MySQL 8.0 + 服务 :8000，数据卷 fleet-mysql
curl http://127.0.0.1:8000/health           # 健康检查
```

- CI（`.github/workflows/ci.yml`）：gofmt → vet → 单测(-race) → MySQL 方言集成用例 → 覆盖率门禁 → 构建 → MySQL 启动冒烟（健康检查 + 登录）。
- 回退：镜像按 git commit 构建并打版本 tag，回退即 `docker compose` 重新拉起上一版本镜像 tag；MySQL 数据卷独立于容器，回退不回滚业务数据（业务规则要求停止/恢复记录保留）。

## 需求对照

业务规则 R1~R8、工作场景 S1~S5、原型 P1~P4、验收点 V1~V24 的实现与证据映射见 `AGENTS.md`（第 2 节任务映射）、`.agents/02-business-rules.md`、`test/evidence/`、`acceptance/acceptance-report.md`。
