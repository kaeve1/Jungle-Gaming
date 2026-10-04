param([switch]$SkipDistributed, [switch]$EnvironmentCheckOnly)
$ErrorActionPreference='Stop'
if ($PSVersionTable.PSVersion.Major -lt 7) { throw 'Use PowerShell 7 (pwsh)' }
$sourcePath=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
function EnterLocalValidationEnvironment {
    $saved=@{}
    $names=@('COMPOSE_FILE','COMPOSE_PROFILES','COMPOSE_ENV_FILES','COMPOSE_PROJECT_NAME')
    foreach ($line in Get-Content -LiteralPath (Join-Path $sourcePath '.env.example')) {
        if ($line -match '^([A-Z][A-Z0-9_]*)=') {
            $optionName=$Matches[1]
            if ($optionName -match '^(INSTANCE_ID|APP_ROLES|LOG_LEVEL|START_TIMEOUT|SHUTDOWN_TIMEOUT|STOP_GRACE_PERIOD|DATABASE_URL|DB_[A-Z_]+|HTTP_ADDRESS|API_[A-Z_]+|OIDC_[A-Z_]+|ALLOW_INSECURE_LOCAL|SQS_[A-Z_]+|AWS_REGION|POSTGRES_[A-Z_]+|WALLET_[A-Z_]+|KC_[A-Z_]+|KEYCLOAK_HOST_PORT|REFERENCE_[A-Z_]+|CONSUMER_[A-Z_]+|PUBLISHER_[A-Z_]+)$') { $names+=$optionName }
        }
    }
    foreach ($name in $names | Select-Object -Unique) {
        $saved[$name]=[Environment]::GetEnvironmentVariable($name,'Process')
        Remove-Item -LiteralPath ('Env:'+$name) -ErrorAction SilentlyContinue
    }
    return $saved
}
function RestoreLocalValidationEnvironment($saved) {
    foreach ($name in $saved.Keys) {
        if ($null -eq $saved[$name]) { Remove-Item -LiteralPath ('Env:'+$name) -ErrorAction SilentlyContinue }
        else { [Environment]::SetEnvironmentVariable($name,$saved[$name],'Process') }
    }
}
if ($EnvironmentCheckOnly) {
    $priorURL=[Environment]::GetEnvironmentVariable('DATABASE_URL','Process')
    $priorFile=[Environment]::GetEnvironmentVariable('COMPOSE_FILE','Process')
    $saved=$null
    Push-Location $sourcePath
    try {
        [Environment]::SetEnvironmentVariable('DATABASE_URL','postgres://wallet_app:validation-only@production.invalid:5432/wallet','Process')
        [Environment]::SetEnvironmentVariable('COMPOSE_FILE','nonexistent-validation-poison.yaml','Process')
        $saved=EnterLocalValidationEnvironment
        $rendered=docker compose -p jungle-gaming-test --env-file tests/test.env config --format json
        if ($LASTEXITCODE -ne 0) { throw 'Isolation config rendering failed' }
        $config=$rendered | ConvertFrom-Json
        if ($config.services.api.environment.DATABASE_URL -cne 'postgres://wallet_app:local-example-app@postgres:5432/wallet?sslmode=disable' -or $config.services.api.environment.SQS_ENDPOINT -cne 'http://sqs:4566') { throw 'Inherited configuration escaped isolation' }
        RestoreLocalValidationEnvironment $saved
        $saved=$null
        if ([Environment]::GetEnvironmentVariable('COMPOSE_FILE','Process') -cne 'nonexistent-validation-poison.yaml' -or [Environment]::GetEnvironmentVariable('DATABASE_URL','Process') -cne 'postgres://wallet_app:validation-only@production.invalid:5432/wallet') { throw 'Environment restoration failed' }
        Write-Output 'PASS: inherited DATABASE_URL/COMPOSE_FILE ignored, local defaults used, environment restored; no resources started'
    } finally {
        if ($null -ne $saved) { RestoreLocalValidationEnvironment $saved }
        RestoreLocalValidationEnvironment @{DATABASE_URL=$priorURL;COMPOSE_FILE=$priorFile}
        Pop-Location
    }
    exit
}
# Fail before creating a validation copy when the Docker engine is unavailable.
docker info --format '{{.OSType}}' 2>$null | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Docker engine unavailable. Start Docker Desktop with Linux containers and retry.' }
$validationID=[guid]::NewGuid().ToString('N').Substring(0,12)
$projectName='jungle-gaming-validation-'+$validationID
$validationPath=Join-Path (Split-Path $sourcePath -Parent) $projectName
$cleanPath=Join-Path $validationPath 'source'
$evidencePath=Join-Path $validationPath 'evidence'
$null=New-Item -ItemType Directory -Path $cleanPath,$evidencePath
$results=[Collections.Generic.List[object]]::new()
function Port {
    $listener=[Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,0)
    $listener.Start()
    try { return $listener.LocalEndpoint.Port } finally { $listener.Stop() }
}
function RunDocker([string]$step,[string[]]$arguments) {
    Write-Output ("RUN: $step")
    $watch=[Diagnostics.Stopwatch]::StartNew()
    $output=& docker @arguments 2>&1
    $code=$LASTEXITCODE
    $output | Set-Content -LiteralPath (Join-Path $evidencePath ($step+'.log')) -Encoding utf8
    $results.Add(@{step=$step;exitCode=$code;seconds=[math]::Round($watch.Elapsed.TotalSeconds,3)})
    if ($code -ne 0) { throw "Validation step failed: $step; see private local evidence" }
    Write-Output ("PASS: $step")
}
# Explicit source roots: never copy machine .env, broker credentials, logs or binaries.
$files=@()
foreach ($name in @('cmd','internal','migrations','deploy','scripts','tests','docs')) {
    $files+=Get-ChildItem -LiteralPath (Join-Path $sourcePath $name) -File -Recurse
}
foreach ($name in @('README.md','ARCHITECTURE.md','go.mod','go.sum','compose.yaml','Dockerfile','.env.example','.gitignore','.dockerignore')) {
    $files+=Get-Item -LiteralPath (Join-Path $sourcePath $name)
}
$manifest=@()
foreach ($file in $files) {
    $relative=[IO.Path]::GetRelativePath($sourcePath,$file.FullName)
    if ($relative -match '(^|[\\/])\.stage\d+-|(^|[\\/])(bin|tmp|coverage|__pycache__)([\\/]|$)|\.(log|exe|test|pyc)$|(^|[\\/])\.env($|\.)' -and $file.Name -ne '.env.example') { continue }
    if (($file.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Source symlink requires explicit inspection' }
    $content=[IO.File]::ReadAllText($file.FullName)
    # Exact negative-authentication fixture, not a live AWS identity. Other
    # matches (including in tests) still fail; never print the matched value.
    if ($relative.Replace('\','/') -ceq 'tests/integration/sqs_test.go') {
        $content=$content.Replace(('"AKIA'+'UNKNOWNKEY000000"'),'"known-invalid-test-key"')
    }
    if ($content -match '-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----|\bAKIA[A-Z0-9]{16}\b|\beyJ[A-Za-z0-9_-]{25,}\.[A-Za-z0-9_-]{25,}\.[A-Za-z0-9_-]{25,}') { throw "Possible secret in source: $relative (content suppressed)" }
    $target=Join-Path $cleanPath $relative
    $null=New-Item -ItemType Directory -Path (Split-Path $target -Parent) -Force
    Copy-Item -LiteralPath $file.FullName -Destination $target
    $hash=(Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash
    if ($hash -ne (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash) { throw 'Copy hash mismatch' }
    $manifest+=@{path=$relative;sha256=$hash}
}
$manifest | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $evidencePath 'source-sha256.json') -Encoding utf8
$pgPort=Port;$idpPort=Port;$apiPort=Port
if (@($pgPort,$idpPort,$apiPort | Select-Object -Unique).Count -ne 3) { throw 'Port selection collision; run again' }
$envPath=Join-Path $cleanPath 'tests/validation.env'
@("COMPOSE_PROJECT_NAME=$projectName","POSTGRES_HOST_PORT=$pgPort","KEYCLOAK_HOST_PORT=$idpPort","API_HOST_PORT=$apiPort",'APP_ROLES=api,reference-worker,consumer,outbox-publisher') | Set-Content -LiteralPath $envPath -Encoding utf8
$savedOptions=EnterLocalValidationEnvironment
Push-Location $cleanPath
$compose=@('compose','-p',$projectName,'--env-file','tests/validation.env')
$primaryFailure=$null
$resourcesMayExist=$false
try {
    $existing=docker ps -a --filter "label=com.docker.compose.project=$projectName" --format '{{.ID}}'
    if ($LASTEXITCODE -ne 0 -or $existing) { throw 'Validation project must be new' }
    $existingVolumes=docker volume ls --filter "label=com.docker.compose.project=$projectName" --format '{{.Name}}'
    if ($LASTEXITCODE -ne 0 -or $existingVolumes) { throw 'Validation volumes must be new' }
    Write-Output ('Copia limpa e recursos novos: '+$validationPath)
    $resourcesMayExist=$true
    RunDocker 'startup-build' ($compose+@('up','-d','--wait','--build','api'))
    & ./scripts/demo.ps1 -ApiURL "http://localhost:$apiPort" -TokenURL "http://localhost:$idpPort/realms/jungle-gaming/protocol/openid-connect/token" -ProjectName $projectName -EnvFile tests/validation.env -WithSQS | Set-Content -LiteralPath (Join-Path $evidencePath 'demo.log') -Encoding utf8
    $results.Add(@{step='real-service-demo';exitCode=0})
    Write-Output 'PASS: real-service-demo'
    RunDocker 'migration-version' ($compose+@('run','--rm','--no-deps','migrate','version'))
    RunDocker 'check' ($compose+@('run','--rm','--no-deps','check'))
    & ./scripts/test-compose-config.ps1 | Set-Content -LiteralPath (Join-Path $evidencePath 'compose-settings.log') -Encoding utf8
    $results.Add(@{step='compose-settings';exitCode=0})
    # Stop real API before tests so it cannot compete with fixture consumers.
    RunDocker 'stop-api' ($compose+@('stop','api'))
    $inspection=docker inspect ($projectName+'-api-1') | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0 -or $inspection[0].State.ExitCode -ne 0) { throw 'Production binary SIGTERM did not exit cleanly' }
    if ($inspection[0].Config.User -ne 'nonroot:nonroot') { throw 'Production runtime is not nonroot' }
    $results.Add(@{step='production-sigterm-nonroot';exitCode=0})
    if ($SkipDistributed) {
        RunDocker 'integration' ($compose+@('run','--rm','--no-deps','integration'))
    } else {
        & ./scripts/test-distributed.ps1 -ProjectName $projectName -EnvFile tests/validation.env -RunPattern '.' | Set-Content -LiteralPath (Join-Path $evidencePath 'distributed.log') -Encoding utf8
        $results.Add(@{step='integration-distributed-race';exitCode=0})
        Write-Output 'PASS: integration-distributed-race'
    }
    Write-Output 'PASS: complete clean validation'
} catch {
    $primaryFailure=$_.Exception
    $results.Add(@{step='failed';message=$_.Exception.Message})
    throw
} finally {
    # Only the fresh project created here; preserve all volumes, including evidence.
    $cleanupFailure=$null
    try {
        if ($resourcesMayExist) { RunDocker 'cleanup' ($compose+@('down')) }
        else { $results.Add(@{step='cleanup-not-needed';exitCode=0}) }
    } catch {
        $cleanupFailure=$_.Exception
        Write-Warning 'Cleanup failed; inspect cleanup.log. Validation evidence will still be written.'
    } finally {
      try {
        @{project=$projectName;source=$cleanPath;ports=@{postgres=$pgPort;idp=$idpPort;api=$apiPort};results=$results} | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $evidencePath 'results.json') -Encoding utf8
        Write-Output ('Evidence retained: '+$evidencePath)
      } finally {
        Pop-Location
        RestoreLocalValidationEnvironment $savedOptions
      }
    }
    if ($null -ne $cleanupFailure -and $null -eq $primaryFailure) { throw $cleanupFailure }
}
