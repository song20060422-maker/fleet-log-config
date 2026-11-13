# 端到端演示冒烟：驱动五组业务场景并导出业务状态（Skill fleet-log-config-check 输入）。
# 前置：本地 fleet 服务已启动（默认 :8000），data/fleet.db 为空库。
# 用法：powershell -File hack/smoke.ps1
$ErrorActionPreference = "Stop"
$base = "http://127.0.0.1:8000"
$evidence = Join-Path $PSScriptRoot "..\test\evidence"
New-Item -ItemType Directory -Force -Path $evidence | Out-Null
$script:probeLog = @()

function Invoke-Api($method, $path, $token, $body) {
    $headers = @{ "Content-Type" = "application/json" }
    if ($token) { $headers["Authorization"] = "Bearer $token" }
    try {
        $res = Invoke-RestMethod -Method $method -Uri "$base$path" -Headers $headers -Body ($body | ConvertTo-Json -Depth 5) -ContentType "application/json"
        return @{ ok = $true; data = $res.data; message = $res.message }
    } catch {
        $resp = $_.Exception.Response
        $status = [int]$resp.StatusCode
        $stream = $resp.GetResponseStream()
        $reader = New-Object IO.StreamReader($stream)
        $raw = $reader.ReadToEnd() | ConvertFrom-Json
        return @{ ok = $false; status = $status; code = $raw.code; message = $raw.message }
    }
}

function Login($username) {
    $r = Invoke-Api "POST" "/api/auth/login" $null @{ username = $username; password = "fleet123456" }
    if (-not $r.ok) { throw "登录失败: $username $($r.message)" }
    return $r.data.token
}

function Expect-Deny($r, $code, $desc) {
    if ($r.ok) { throw "探测失败（应被拒绝却成功）: $desc" }
    if ($r.code -ne $code) { throw "探测失败: $desc 期望 $code 实际 $($r.code)（$($r.message)）" }
    $script:probeLog += "| $desc | $code（HTTP $($r.status)） | $($r.message) |"
    Write-Host "  [拒绝] $desc -> $code"
}

function Expect-Ok($r, $desc) {
    if (-not $r.ok) { throw "步骤失败: $desc -> $($r.code) $($r.message)" }
    Write-Host "  [成功] $desc"
}

function DesiredVersion($token, $idx) {
    $name = "VEH-{0:D2}" -f $idx
    $r = Invoke-Api "GET" "/api/vehicle/state" $token $null
    return $r.data.state.desired_version
}

Write-Host "=== 登录预置账号 ==="
$dev = Login "dev"; $approver = Login "approver"; $ops = Login "ops"
$veh = @{}
for ($i = 1; $i -le 5; $i++) { $veh[$i] = Login ("vehicle-{0:D2}" -f $i) }
Expect-Ok (Invoke-Api "GET" "/health" $null $null) "健康检查"
Expect-Ok (Invoke-Api "GET" "/api/meta" $dev $null) "演示元信息"

Write-Host "=== 场景1 正常推广 ==="
$app1 = (Invoke-Api "POST" "/api/applications" $dev @{ reason = "排查车机诊断日志偶发缺失"; vehicle_ids = @("VEH-01","VEH-02","VEH-03","VEH-04","VEH-05"); interval = 30 }).data
Expect-Deny (Invoke-Api "POST" "/api/applications/$($app1.id)/approve" $dev $null) "SELF_APPROVAL" "申请人自审批"
Expect-Ok (Invoke-Api "POST" "/api/applications/$($app1.id)/approve" $approver $null) "审批负责人批准"
$act1 = (Invoke-Api "POST" "/api/activities" $ops @{ application_id = $app1.id; pilot_vehicle_ids = @("VEH-01","VEH-02") }).data
$pull3 = Invoke-Api "GET" "/api/vehicle/config" $veh[3] $null
if ($pull3.ok -and $pull3.data.granted) { throw "探测失败: 未试点车提前取得了活动配置" }
$script:probeLog += "| 未试点车提前拉取配置 | granted=false（HTTP 200，业务拒绝） | $($pull3.data.reject_reason) |"
Write-Host "  [拒绝] 未试点车提前拉取配置 -> granted=false（$($pull3.data.reject_reason)）"
Expect-Deny (Invoke-Api "POST" "/api/activities/$($act1.id)/promote" $ops $null) "PROMOTE_BLOCKED" "试点回执前推广"
Expect-Ok (Invoke-Api "POST" "/api/vehicle/receipt" $veh[1] @{ version = (DesiredVersion $veh[1] 1); success = $true; message = "应用成功" }) "试点 VEH-01 成功"
Expect-Ok (Invoke-Api "POST" "/api/vehicle/receipt" $veh[1] @{ version = (DesiredVersion $veh[1] 1); success = $true; message = "重复上报" }) "试点 VEH-01 重复成功（幂等）"
Expect-Deny (Invoke-Api "POST" "/api/activities/$($act1.id)/promote" $ops $null) "PROMOTE_BLOCKED" "仅一台试点成功时推广"
Expect-Ok (Invoke-Api "POST" "/api/vehicle/receipt" $veh[2] @{ version = (DesiredVersion $veh[2] 2); success = $true; message = "应用成功" }) "试点 VEH-02 成功"
# 两台试点成功后允许推广
$p = (Invoke-Api "GET" "/api/activities/$($act1.id)/progress" $ops $null).data
if (-not $p.can_promote) { throw "两台试点成功后应可推广" }
Expect-Ok (Invoke-Api "POST" "/api/activities/$($act1.id)/promote" $ops $null) "确认推广"
Expect-Deny (Invoke-Api "POST" "/api/vehicle/receipt" $veh[1] @{ version = (DesiredVersion $veh[2] 2); success = $true }) "CROSS_VEHICLE" "跨车回执"
Expect-Deny (Invoke-Api "POST" "/api/vehicle/receipt" $veh[1] @{ version = 999999; success = $true }) "UNKNOWN_VERSION" "未知版本回执"
for ($i = 3; $i -le 5; $i++) {
    Expect-Ok (Invoke-Api "POST" "/api/vehicle/receipt" $veh[$i] @{ version = (DesiredVersion $veh[$i] $i); success = $true; message = "应用成功" }) "推广 VEH-$i 成功"
}
Expect-Ok (Invoke-Api "POST" "/api/activities/$($act1.id)/complete" $approver $null) "逐车核查后记为完成"

Write-Host "=== 场景2 审批退回与修改 ==="
$app2 = (Invoke-Api "POST" "/api/applications" $dev @{ reason = "排查网关日志采样异常"; vehicle_ids = @("VEH-01","VEH-02","VEH-03","VEH-04","VEH-05"); interval = 15 }).data
Expect-Ok (Invoke-Api "POST" "/api/applications/$($app2.id)/reject" $approver @{ reason = "请补充采样异常的复现条件" }) "退回申请"
Expect-Deny (Invoke-Api "PUT" "/api/applications/$($app2.id)" $ops @{ reason = "越权修改"; vehicle_ids = @("VEH-01","VEH-02","VEH-03","VEH-04","VEH-05"); interval = 15 }) "FORBIDDEN_ROLE" "非申请人修改"
Expect-Ok (Invoke-Api "PUT" "/api/applications/$($app2.id)" $dev @{ reason = "排查网关日志采样异常（已补充复现条件）"; vehicle_ids = @("VEH-01","VEH-02","VEH-03","VEH-04","VEH-05"); interval = 15 }) "修改后重新提交"
Expect-Ok (Invoke-Api "POST" "/api/applications/$($app2.id)/approve" $approver $null) "重新批准"

Write-Host "=== 场景3 试点失败/超时 -> 暂停 -> 停止 ==="
$act2 = (Invoke-Api "POST" "/api/activities" $ops @{ application_id = $app2.id; pilot_vehicle_ids = @("VEH-01","VEH-02") }).data
Expect-Ok (Invoke-Api "POST" "/api/vehicle/receipt" $veh[1] @{ version = (DesiredVersion $veh[1] 1); success = $true; message = "应用成功" }) "试点 VEH-01 成功"
Write-Host "  等待 62 秒触发 VEH-02 超时判定…"
Start-Sleep -Seconds 62
$p2 = (Invoke-Api "GET" "/api/activities/$($act2.id)/progress" $ops $null).data
if ($p2.activity.status -ne "paused") { throw "试点超时后活动应暂停，实际 $($p2.activity.status)" }
Write-Host "  [成功] VEH-02 超时 -> 活动已暂停"
Expect-Deny (Invoke-Api "POST" "/api/activities/$($act2.id)/promote" $ops $null) "PROMOTE_BLOCKED" "暂停后绕过检查推广"
Expect-Ok (Invoke-Api "POST" "/api/activities/$($act2.id)/stop" $approver @{ reason = "试点车离线未回执，停止未发送部分" }) "负责人停止活动"
Expect-Deny (Invoke-Api "POST" "/api/vehicle/receipt" $veh[2] @{ version = (DesiredVersion $veh[2] 2); success = $true; message = "停机后迟到回执" }) "ACTIVITY_CLOSED" "已停止活动的迟到回执"

Write-Host "=== 场景4 恢复分支 ==="
Expect-Deny (Invoke-Api "POST" "/api/activities/$($act2.id)/restore/publish" $ops $null) "INVALID_STATUS" "未批准即下发恢复"
Expect-Ok (Invoke-Api "POST" "/api/activities/$($act2.id)/restore/approve" $approver $null) "批准恢复"
Expect-Ok (Invoke-Api "POST" "/api/activities/$($act2.id)/restore/publish" $ops $null) "下发恢复版本（更高版本，目标活动前值 60）"
Expect-Ok (Invoke-Api "POST" "/api/vehicle/receipt" $veh[1] @{ version = (DesiredVersion $veh[1] 1); success = $true; message = "已恢复原设置" }) "VEH-01 确认恢复"
Expect-Deny (Invoke-Api "POST" "/api/activities/$($act2.id)/restore/complete" $approver $null) "RESTORE_BLOCKED" "VEH-02 未确认时记恢复完成"
Expect-Ok (Invoke-Api "POST" "/api/vehicle/receipt" $veh[2] @{ version = (DesiredVersion $veh[2] 2); success = $false; message = "恢复写入失败" }) "VEH-02 恢复失败回执"
Expect-Ok (Invoke-Api "POST" "/api/vehicle/receipt" $veh[2] @{ version = (DesiredVersion $veh[2] 2); success = $true; message = "重试后恢复成功" }) "VEH-02 恢复成功回执"
Expect-Ok (Invoke-Api "POST" "/api/activities/$($act2.id)/restore/complete" $approver $null) "记为恢复完成"

Write-Host "=== 导出业务状态 ==="
$state = Invoke-RestMethod -Method GET -Uri "$base/api/state/export" -Headers @{ Authorization = "Bearer $ops" }
$state | ConvertTo-Json -Depth 8 | Out-File -FilePath (Join-Path $evidence "state.json") -Encoding utf8
$probeLog | Out-File -FilePath (Join-Path $evidence "api_probe.md") -Encoding utf8
Write-Host "已导出 test/evidence/state.json 与 test/evidence/api_probe.md"
Write-Host "SMOKE PASSED"
