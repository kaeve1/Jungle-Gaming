param(
    [string]$RunPattern = '^TestStage11',
    [ValidatePattern('^jungle-gaming-(test|validation-[a-f0-9]{12})$')]
    [string]$ProjectName = 'jungle-gaming-test',
    [string]$EnvFile = 'tests/test.env'
)
$ErrorActionPreference = 'Stop'
$projectPath = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$runName = '.stage11-' + [guid]::NewGuid().ToString('N')
$controlPath = Join-Path $projectPath ('tests/' + $runName)
New-Item -ItemType Directory -Path $controlPath | Out-Null
$pausedContainers = @{}
$testProcess = $null
$testContainerName = $ProjectName + '-distributed-' + [guid]::NewGuid().ToString('N')
Push-Location $projectPath
try {
    foreach ($serviceName in @('postgres','sqs')) {
        $containerName = $ProjectName + '-' + $serviceName + '-1'
        $inspection = docker inspect $containerName | ConvertFrom-Json
        if ($LASTEXITCODE -ne 0 -or $inspection[0].Config.Labels.'com.docker.compose.project' -ne $ProjectName -or $inspection[0].Config.Labels.'com.docker.compose.service' -ne $serviceName) {
            throw 'Test container identity could not be verified; no pause performed'
        }
    }
    $stdoutPath = Join-Path $controlPath 'test.stdout.log'
    $stderrPath = Join-Path $controlPath 'test.stderr.log'
    $dockerArgs = @('compose','-p',$ProjectName,'--env-file',$EnvFile,'run','--rm','--no-deps','--name',$testContainerName,'-e',('STAGE11_CONTROL_DIR=/src/tests/'+$runName),'--entrypoint','go','integration','test','-race','-tags','integration,distributed','-count=1','-timeout','480s','-run',$RunPattern,'-v','./tests/integration/...')
    $launchOptions = @{ FilePath='docker'; ArgumentList=$dockerArgs; WorkingDirectory=$projectPath; PassThru=$true; RedirectStandardOutput=$stdoutPath; RedirectStandardError=$stderrPath }
    if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) { $launchOptions.WindowStyle = 'Hidden' }
    $testProcess = Start-Process @launchOptions
    $deadline = [datetime]::UtcNow.AddMinutes(9)
    while (-not $testProcess.HasExited) {
        if ([datetime]::UtcNow -gt $deadline) { throw 'Distributed test supervisor timed out' }
        foreach ($serviceName in @('postgres','sqs')) {
            $containerName = $ProjectName + '-' + $serviceName + '-1'
            if ((Test-Path -LiteralPath (Join-Path $controlPath ($serviceName+'.pause'))) -and -not (Test-Path -LiteralPath (Join-Path $controlPath ($serviceName+'.paused')))) {
                docker pause $containerName
                if ($LASTEXITCODE -ne 0) { throw 'Test container pause failed' }
                $pausedContainers[$containerName] = $true
                [System.IO.File]::WriteAllText((Join-Path $controlPath ($serviceName+'.paused')), 'paused')
            }
            if ((Test-Path -LiteralPath (Join-Path $controlPath ($serviceName+'.resume'))) -and -not (Test-Path -LiteralPath (Join-Path $controlPath ($serviceName+'.resumed')))) {
                docker unpause $containerName
                if ($LASTEXITCODE -ne 0) { throw 'Test container resume failed' }
                $pausedContainers.Remove($containerName)
                [System.IO.File]::WriteAllText((Join-Path $controlPath ($serviceName+'.resumed')), 'resumed')
            }
        }
        Start-Sleep -Milliseconds 100
        $testProcess.Refresh()
    }
    $testProcess.WaitForExit()
    Get-Content -LiteralPath $stdoutPath
    Get-Content -LiteralPath $stderrPath
    if ($testProcess.ExitCode -ne 0) { throw 'Distributed integration failed; evidence retained in test run directory' }
    Write-Output ('PASS: distributed integration; evidence: ' + $controlPath)
} finally {
    foreach ($containerName in $pausedContainers.Keys) { docker unpause $containerName | Out-Null }
    if ($null -ne $testProcess -and -not $testProcess.HasExited) { docker stop -t 5 $testContainerName | Out-Null; $testProcess.WaitForExit() }
    Pop-Location
}
