# 一键构建并推送 Docker Hub（ssccing/mediamanager）
# 用法（在 123bot重构 目录）：
#   powershell -ExecutionPolicy Bypass -File build_push.ps1
#
# 步骤：buildx 多架构构建（linux/amd64 + linux/arm64）-> 直接推送 latest + 当日日期标签
# 发布前请先更新 cmd/mmbot/main.go 里的 var version（版本号递增）。

$ErrorActionPreference = "Stop"
# 构建上下文是当前目录，必须先切到脚本所在目录（否则会在调用方的目录里构建）
Set-Location $PSScriptRoot
# Docker CLI 与凭据助手（docker-credential-desktop）不在默认 PATH，需把 bin 目录加进去
# 2026-08-31 装机时为 Program Files，2026-09-01 实测本机是用户级安装（AppData\Local\Programs）
$dockerBin = "C:\Users\Administrator\AppData\Local\Programs\DockerDesktop\resources\bin"
if (-not (Test-Path (Join-Path $dockerBin "docker.exe"))) { $dockerBin = "C:\Program Files\Docker\Docker\resources\bin" }
# 本机（2026-08-31 实测）直连 auth.docker.io/registry 超时，但系统代理 127.0.0.1:7897 可用：
# buildkit 推 layers 走 VM 网络能通，最终 manifest 前的 token 请求由 Windows 侧发起会直连超时，
# 必须设 HTTP(S)_PROXY 走代理。已有代理环境变量时保持原值。
if (-not $env:HTTP_PROXY -and -not $env:HTTPS_PROXY) {
    try {
        $p = Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' -ErrorAction Stop
        if ($p.ProxyEnable -eq 1 -and $p.ProxyServer) {
            $proxy = $p.ProxyServer
            $env:HTTP_PROXY = "http://$proxy"
            $env:HTTPS_PROXY = "http://$proxy"
            Write-Host "使用系统代理: $proxy"
        }
    } catch { }
}
$env:Path = $dockerBin + ";" + $env:Path
$docker = Join-Path $dockerBin "docker.exe"
$repo = "ssccing/mediamanager"
$dateTag = Get-Date -Format "yyyyMMdd"

# 0. 从 main.go 提取当前版本号，提醒确认
$mainFile = Join-Path $PSScriptRoot "cmd\mmbot\main.go"
if (Test-Path $mainFile) {
    $m = Select-String -Path $mainFile -Pattern 'var version = "([^"]+)"'
    if ($m) { Write-Host "当前版本号: $($m.Matches[0].Groups[1].Value)  （发布前请确认已递增）" }
}

# 1. 检查 Docker daemon
Write-Host "== 检查 Docker =="
& $docker info --format "{{.ServerVersion}}" | Out-Null
if ($LASTEXITCODE -ne 0) { Write-Host "Docker 未运行，请先启动 Docker Desktop"; exit 1 }

# 2. 多架构构建并直接推送（amd64 + arm64；buildx 按 TARGETARCH 自动选 compose 二进制）
# 注意：PS 5.1 中 ErrorActionPreference=Stop 时 native 命令的 stderr 会终止脚本，这里临时切回 Continue
$oldEA = $ErrorActionPreference
$ErrorActionPreference = "Continue"
$platforms = "linux/amd64,linux/arm64"
for ($i = 1; $i -le 3; $i++) {
    Write-Host "== 构建并推送多架构镜像（$platforms，第 ${i} 次）=="
    Write-Host "   标签: ${repo}:latest / ${repo}:${dateTag}"
    & $docker buildx build --platform $platforms --push `
        -t "${repo}:latest" -t "${repo}:${dateTag}" .
    if ($LASTEXITCODE -eq 0) { break }
    if ($i -lt 3) { Write-Host "构建/推送超时，10 秒后重试..."; Start-Sleep -Seconds 10 }
}
if ($LASTEXITCODE -ne 0) { Write-Host "构建/推送失败，请手动重试"; exit 1 }

# 3. 验证 Hub 上的多架构 manifest
& $docker buildx imagetools inspect "${repo}:latest" 2>&1 | Select-String -Pattern "Platform|MediaType" | Select-Object -First 4
$ErrorActionPreference = $oldEA

Write-Host ""
Write-Host "发布完成。NAS 上发送 /update 即可更新容器。"

