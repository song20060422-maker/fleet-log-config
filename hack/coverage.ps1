# 分包覆盖率统计（跨包并集）：Windows 本地执行入口。
# 用法：powershell -File hack/coverage.ps1 [-Min 85]
param([double]$Min = 85)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

$outDir = Join-Path $env:TEMP ("fleet-cov-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $outDir | Out-Null
$module = (go list -m)

$jobs = @(
    @("./internal/logic", "", "L.out"),
    @("./internal/logic", "$module/internal/store", "LS.out"),
    @("./internal/logic", "$module/internal/model", "LM.out"),
    @("./internal/controller", "", "C.out"),
    @("./internal/controller", "$module/internal/logic", "CL.out"),
    @("./internal/controller", "$module/internal/store", "CS.out"),
    @("./internal/controller", "$module/internal/model", "CM.out"),
    @("./internal/store", "", "S.out"),
    @("./internal/model", "", "M.out")
)
foreach ($j in $jobs) {
    $pkg = $j[0]; $coverpkg = $j[1]; $name = $j[2]
    $args = @("test", $pkg, "-count=1", "-coverprofile=`"$outDir\$name`"")
    if ($coverpkg -ne "") { $args += "-coverpkg=$coverpkg" }
    Write-Host "go $($args -join ' ')"
    & go @args | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "测试失败: $pkg" }
}
$profiles = ($jobs | ForEach-Object { "$outDir\$($_[2])" }) -join ","
& go run ./hack/coverstat -profile "`"$profiles`"" -min $Min
exit $LASTEXITCODE
