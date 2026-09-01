# Retry multi-arch push until success. ASCII only (PS 5.1 reads UTF-8 no-BOM as ANSI, so no non-ASCII chars here).
Set-Location $PSScriptRoot
$docker = "C:\Program Files\Docker\Docker\resources\bin\docker.exe"
for ($i = 1; $i -le 30; $i++) {
    Write-Host ("[$i] " + (Get-Date -Format 'HH:mm:ss') + " push...")
    & $docker buildx build --platform linux/amd64,linux/arm64 --push -t ssccing/mediamanager:latest -t ssccing/mediamanager:20260831 . 2>&1 | Select-Object -Last 2
    if ($LASTEXITCODE -eq 0) { Write-Host "PUSH_OK"; exit 0 }
    Write-Host ("[$i] failed, wait 60s")
    Start-Sleep -Seconds 60
}
Write-Host "ALL_ATTEMPTS_FAILED"
exit 1
