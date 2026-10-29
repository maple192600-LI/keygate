param(
  [Parameter(Mandatory = $true)]
  [ValidateSet('Install', 'Remove')]
  [string]$Action
)

$ErrorActionPreference = 'Stop'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$entryName = 'XiaoYunqueAuthorizationCenter'

if ($Action -eq 'Install') {
  $appPath = Join-Path $PSScriptRoot '小云雀客户授权中心.exe'
  if (-not (Test-Path -LiteralPath $appPath -PathType Leaf)) {
    throw '请从小云雀客户授权中心程序目录运行此脚本。'
  }
  $command = '"' + $appPath + '" --startup'
  New-Item -Path $runKey -Force | Out-Null
  Set-ItemProperty -LiteralPath $runKey -Name $entryName -Value $command
  Write-Output '已配置当前 Windows 用户登录后自动启动后台授权服务。'
} else {
  if (Test-Path -LiteralPath $runKey) {
    Remove-ItemProperty -LiteralPath $runKey -Name $entryName -ErrorAction SilentlyContinue
  }
  Write-Output '已移除当前 Windows 用户的后台服务启动配置。'
}
