param(
    [string]$ApiURL = 'http://localhost:18081',
    [string]$TokenURL = 'http://localhost:18080/realms/jungle-gaming/protocol/openid-connect/token',
    [string]$ProjectName = 'jungle-gaming',
    [string]$EnvFile = '',
    [switch]$WithSQS
)
$ErrorActionPreference = 'Stop'
if ($PSVersionTable.PSVersion.Major -lt 7) { throw 'Use PowerShell 7 (pwsh)' }
function Token($client, $secret) {
    $reply = Invoke-RestMethod -Method Post -Uri $TokenURL -TimeoutSec 10 -Body @{grant_type='client_credentials';client_id=$client;client_secret=$secret}
    if (-not $reply.access_token) { throw 'IdP did not return access token' }
    return $reply.access_token
}
function Call($method, $path, $token, $body, $key, $expected) {
    $headers = @{'X-Correlation-Id'=('demo-'+[guid]::NewGuid().ToString('N'))}
    if ($token) { $headers.Authorization = 'Bearer '+$token }
    if ($key) { $headers.'Idempotency-Key' = $key }
    $options = @{Method=$method;Uri=($ApiURL+$path);Headers=$headers;TimeoutSec=30;SkipHttpErrorCheck=$true}
    if ($null -ne $body) { $options.ContentType='application/json';$options.Body=($body | ConvertTo-Json -Depth 10 -Compress) }
    $reply=Invoke-WebRequest @options
    if ([int]$reply.StatusCode -ne $expected) { throw "Unexpected HTTP status for $method $path : $($reply.StatusCode), expected $expected" }
    return ($reply.Content | ConvertFrom-Json)
}
function Broker($arguments) {
    $compose=@('compose','-p',$ProjectName)
    if ($EnvFile) { $compose+=@('--env-file',$EnvFile) }
    $text=docker @compose run --rm --no-deps broker-cli @arguments
    if ($LASTEXITCODE -ne 0) { throw 'Broker CLI failed' }
    return ($text | ConvertFrom-Json)
}
Push-Location (Join-Path $PSScriptRoot '..')
try {
    $internalSecret = if ($env:KC_SECRET_WALLET_INTERNAL) { $env:KC_SECRET_WALLET_INTERNAL } else { 'local-example-wallet-internal-secret' }
    $providerSecret = if ($env:KC_SECRET_PROVIDER_A) { $env:KC_SECRET_PROVIDER_A } else { 'local-example-provider-a-secret' }
    $internal=Token 'wallet-internal' $internalSecret
    $provider=Token 'provider-a' $providerSecret
    $null=Call GET '/health/ready' '' $null '' 200
    $null=Call GET '/wallets/0192f291-27dd-7d3f-8071-5f8685deef37' '' $null '' 401
    $player=[guid]::NewGuid().ToString()
    $wallet=Call POST '/wallets' $internal @{playerId=$player;initialBalance=@{amount='100.00';currency='BRL'}} '' 201
    $external='demo-'+[guid]::NewGuid().ToString('N')
    $request=@{providerId='provider-a';externalTransactionId=$external;playerId=$player;walletId=$wallet.id;roundId='demo-round';gameId='fortune-chimp';kind='BET';money=@{amount='25.00';currency='BRL'}}
    $key='provider-a:'+$external
    $first=Call POST '/wagering/transactions' $provider $request $key 201
    if ($first.status -ne 'PROCESSED' -or $first.balance.amount -cne '75.00') { throw 'BET result mismatch' }
    $win=$request.Clone();$win.externalTransactionId+='-win';$win.kind='WIN';$win.money=@{amount='2.00';currency='BRL'}
    $null=Call POST '/wagering/transactions' $provider $win $win.externalTransactionId 201
    $replay=Call POST '/wagering/transactions' $provider $request $key 200
    if (-not $replay.idempotentReplay -or $replay.balance.amount -cne '75.00' -or $replay.transactionId -ne $first.transactionId) { throw 'Replay did not preserve original result' }
    $loss=$request.Clone();$loss.externalTransactionId+='-loss';$loss.kind='LOSS';$loss.money=@{amount='0.00';currency='BRL'}
    $null=Call POST '/wagering/transactions' $provider $loss $loss.externalTransactionId 201
    $current=Call GET ('/wallets/'+$wallet.id) $internal $null '' 200
    if ($current.balance.amount -cne '77.00' -or $current.version -ne 3) { throw 'LOSS/replay changed wallet unexpectedly' }
    $ledger=Call GET ('/wallets/'+$wallet.id+'/ledger?limit=2') $internal $null '' 200
    if ($ledger.entries.Count -ne 2 -or -not $ledger.nextCursor) { throw 'Pagination result mismatch' }
    $page=Call GET ('/wallets/'+$wallet.id+'/ledger?limit=2&cursor='+[uri]::EscapeDataString($ledger.nextCursor)) $internal $null '' 200
    if ($page.entries.Count -ne 1) { throw 'Ledger cursor did not advance' }
    $reconciliation=Call POST ('/wallets/'+$wallet.id+'/reconciliation') $internal $null '' 200
    if (-not $reconciliation.consistent -or $reconciliation.difference.amount -cne '0.00' -or $reconciliation.checkedEntries -ne 3) { throw 'Reconciliation mismatch' }
    $null=Call GET ('/providers/provider-a/wagering/transactions/'+$external) $provider $null '' 200
    $metric=Invoke-WebRequest -Uri ($ApiURL+'/metrics') -Headers @{Authorization=('Bearer '+$internal)} -TimeoutSec 10
    if ($metric.StatusCode -ne 200 -or $metric.Content -notmatch 'wallet_') { throw 'Metrics unavailable' }
    if ($WithSQS) {
        $sqsRequest=$request.Clone();$sqsRequest.externalTransactionId+='-sqs';$sqsRequest.kind='WIN';$sqsRequest.money=@{amount='1.00';currency='BRL'};$sqsRequest.idempotencyKey=$sqsRequest.externalTransactionId
        $envelope=@{messageId=('msg-'+[guid]::NewGuid().ToString('N'));type='WagerTransactionRequested';occurredAt=[datetime]::UtcNow.ToString('o');data=$sqsRequest} | ConvertTo-Json -Depth 10 -Compress
        $queue=Broker @('--profile','wager-producer','sqs','get-queue-url','--queue-name','wager-transactions.fifo')
        for ($i=0;$i -lt 2;$i++) {
            $null=Broker @('--profile','wager-producer','sqs','send-message','--queue-url',$queue.QueueUrl,'--message-body',$envelope,'--message-group-id',$wallet.id,'--message-deduplication-id',[guid]::NewGuid().ToString('N'))
        }
        $deadline=[datetime]::UtcNow.AddSeconds(45)
        do {
            $current=Call GET ('/wallets/'+$wallet.id) $internal $null '' 200
            if ($current.balance.amount -ceq '78.00') { break }
            Start-Sleep -Milliseconds 100
        } while ([datetime]::UtcNow -lt $deadline)
        if ($current.balance.amount -cne '78.00') { throw 'SQS operation not processed exactly once' }
        $null=Call POST '/wagering/transactions' $provider $sqsRequest $sqsRequest.idempotencyKey 400
        # SQS-only idempotencyKey is not part of the HTTP body.
        $httpReplay=$sqsRequest.Clone();$httpReplay.Remove('idempotencyKey')
        $replay=Call POST '/wagering/transactions' $provider $httpReplay $sqsRequest.idempotencyKey 200
        if (-not $replay.idempotentReplay -or $replay.balance.amount -cne '78.00') { throw 'HTTP/SQS replay mismatch' }
    }
    Write-Output 'PASS: API real, IdP, BET/WIN/LOSS, replay original, ledger paginado, reconciliacao, metricas e acesso sem token recusado'
    if ($WithSQS) { Write-Output 'PASS: SQS real com envelope repetido/dedup IDs distintos e replay cruzado HTTP' }
    Write-Output ('Carteira local criada para demonstracao: '+$wallet.id)
} finally {
    $internal=$null;$provider=$null
    Pop-Location
}
