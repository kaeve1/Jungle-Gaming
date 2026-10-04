$ErrorActionPreference = 'Stop'
$reviewOptions = @{
    DB_OPERATION_TIMEOUT = '7s'; DB_LOCK_TIMEOUT = '1s'; DB_STATEMENT_TIMEOUT = '2s'; DB_MAX_RETRIES = '1'
    OIDC_INTERNAL_CLIENT = 'audit-internal'; OIDC_AUDIENCE = 'wallet-api'; LOG_LEVEL = 'debug'
    START_TIMEOUT = '15s'; SHUTDOWN_TIMEOUT = '12s'; STOP_GRACE_PERIOD = '16s'
    REFERENCE_TTL = '3m'; REFERENCE_BASE_DELAY = '2s'; REFERENCE_MAX_DELAY = '20s'
    CONSUMER_QUEUE = 'wager-transactions.fifo'; CONSUMER_NAME = 'test-consumer'; CONSUMER_PROVIDERS = 'provider-a'; CONSUMER_CONCURRENCY = '3'
    PUBLISHER_QUEUE = 'wallet-events.fifo'; PUBLISHER_CONCURRENCY = '3'; CONSUMER_DLQ = 'wager-transactions-dlq.fifo'
    APP_ROLES = 'api,reference-worker,consumer,outbox-publisher'; HTTP_ADDRESS = ':9090'; API_CONTAINER_PORT = '9090'; API_HOST_PORT = '29090'
    INSTANCE_ID = 'compose-regression'; AWS_REGION = 'us-east-1'; ALLOW_INSECURE_LOCAL = 'true'
}
$savedOptions = @{}
Push-Location (Join-Path $PSScriptRoot '..')
try {
    foreach ($optionName in $reviewOptions.Keys) {
        $savedOptions[$optionName] = [Environment]::GetEnvironmentVariable($optionName, 'Process')
        [Environment]::SetEnvironmentVariable($optionName, $reviewOptions[$optionName], 'Process')
    }
    # Keep rendered configuration private: it also contains local credentials.
    $rendered = docker compose --env-file tests/test.env config --format json
    if ($LASTEXITCODE -ne 0) { throw 'Compose rendering failed' }
    $reviewConfig = $rendered | ConvertFrom-Json
    foreach ($optionName in $reviewOptions.Keys) {
        if ($optionName -in @('STOP_GRACE_PERIOD', 'API_CONTAINER_PORT', 'API_HOST_PORT')) { continue }
        if ($reviewConfig.services.api.environment.$optionName -ne $reviewOptions[$optionName]) {
            throw "API configuration did not forward $optionName"
        }
    }
    if ($reviewConfig.services.api.stop_grace_period -ne '16s') { throw 'Stop grace period was ignored' }
    $reviewPort = $reviewConfig.services.api.ports[0]
    if ($reviewPort.target -ne 9090 -or $reviewPort.published -ne '29090' -or $reviewPort.host_ip -ne '127.0.0.1') {
        throw 'API port mapping is inconsistent'
    }
    Write-Output 'OK: Compose forwards API configuration and port/grace settings'
} finally {
    foreach ($optionName in $savedOptions.Keys) {
        [Environment]::SetEnvironmentVariable($optionName, $savedOptions[$optionName], 'Process')
    }
    Pop-Location
}



