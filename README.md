# Jungle Gaming — processamento distribuído de apostas

Validação mais recente: [reavaliação independente com `.env.example`](docs/REAVALIACAO-ENV-EXAMPLE.md), com nova suíte integral e correção do probe operacional de readiness.

Serviço Go com API HTTP autenticada, consumidor SQS, worker de referências e transactional outbox. PostgreSQL garante dinheiro exato, ledger imutável, idempotência persistente e coordenação entre processos. [Enunciado](docs/DESAFIO-ORIGINAL.md), [arquitetura](ARCHITECTURE.md), [contratos](docs/CONTRATOS.md) e [matriz de requisitos](docs/MATRIZ-REQUISITOS.md).

## Navegação

- [Roteiro completo de avaliação](docs/ROTEIRO-DE-AVALIACAO.md): ordem dos testes, comandos, resultados esperados e avaliação externa em três containers.
- [Documentação técnica](docs/README.md) e [mapa do repositório](docs/ESTRUTURA.md).
- [Código interno](internal/README.md), [executáveis](cmd/README.md) e [migrations](migrations/README.md).
- [Infraestrutura](deploy/README.md), [scripts](scripts/README.md) e [testes](tests/README.md).
- [Conformidade com o enunciado](docs/CONFORMIDADE.md) e [validação final](docs/VALIDACAO-FINAL.md).

Validação final de 2026-10-04: integração/distribuição com `-race` aprovada, HTTP externo **7/7**, operações **5/5** e scanner sem vulnerabilidades conhecidas detectadas. [Evidências e limitações](docs/VALIDACAO-FINAL.md).

## Pré-requisitos e início

Docker Engine/Desktop em execução, containers Linux e Docker Compose v2 com suporte a `up --wait`. A primeira execução precisa de internet para imagens e módulos. Go local é opcional: o container de ferramentas usa Go **1.27.1** com compilador C para `-race`. Scripts `.ps1` usam **PowerShell 7 (`pwsh`)**, também disponível em Linux/macOS. Os exemplos Bash abaixo usam curl e jq; a demonstração PowerShell dispensa ambos.

Execute na pasta que contém compose.yaml:

```sh
docker compose up -d --wait --build api
# Equivalente em primeiro plano:
# docker compose up --build
```

Não é necessário criar `.env`: os padrões locais já estão no Compose. Opcionalmente copie `.env.example` para `.env` e ajuste as opções antes de criar os volumes. O startup importa automaticamente o realm/identidades Keycloak, cria papéis PostgreSQL, aplica migrations e provisiona filas/políticas/credenciais SQS. `ready` aguarda os jobs e o IdP; a API só inicia após os probes das dependências. Não é necessário criar filas ou clientes manualmente.

| Serviço | Acesso local | Configuração padrão |
| --- | --- | --- |
| API | http://localhost:18081 | Quatro papéis ativos |
| Keycloak | http://localhost:18080 | Realm jungle-gaming; console admin/local-example-admin |
| PostgreSQL | 127.0.0.1:15432 | Banco wallet; runtime wallet_app; migrations wallet_migrator |
| SQS | Apenas rede interna, http://sqs:4566 | MiniStack com assinatura SigV4 e IAM |

```sh
docker compose logs --tail 30 api
curl http://localhost:18081/health/live
curl http://localhost:18081/health/ready
docker compose down  # para e remove containers/rede, preserva volumes
```

## Variáveis e identidades

A lista completa, incluindo senhas **locais de exemplo**, está em [.env.example](.env.example). Não utilize esses padrões em produção. `.env` e credenciais geradas não devem fazer parte do código. As credenciais do broker são provisionadas em volumes; o runtime recebe apenas wallet-service em montagem somente leitura. `broker-cli` é uma ferramenta interna com os perfis de produtor/observador, sem credencial administrativa.

| Grupo | Variáveis e padrão | Observação |
| --- | --- | --- |
| Papéis | APP_ROLES=api,reference-worker,consumer,outbox-publisher no Compose | Execução direta do binário assume apenas api; CONSUMER_NAME=wager-consumer deve ser estável entre réplicas |
| Banco | DATABASE_URL com wallet_app; DB_MAX_CONNS=10 | Nunca usar postgres/wallet_migrator no runtime; cada processo tem seu pool |
| Prazos SQL | DB_OPERATION_TIMEOUT=20s, DB_LOCK_TIMEOUT=3s, DB_STATEMENT_TIMEOUT=5s, DB_MAX_RETRIES=3 | Retry somente após rollback conhecido; commit ambíguo exige mesma chave do cliente |
| HTTP | HTTP_ADDRESS=:8080, API_CONTAINER_PORT=8080, API_HOST_PORT=18081 | Mantenha a porta interna coerente com HTTP_ADDRESS |
| IdP | OIDC_ISSUER, OIDC_JWKS_URL, OIDC_AUDIENCE=wallet-api, OIDC_INTERNAL_CLIENT=wallet-internal | Issuer usa localhost/porta publicada; JWKS usa nome interno keycloak |
| Segurança local | ALLOW_INSECURE_LOCAL=true | Permite HTTP dos emuladores; produção exige TLS |
| Referências | REFERENCE_TTL=10m, BASE_DELAY=1s, MAX_DELAY=1m | Prefixo REFERENCE_ em todas; agenda/TTL usa relógio PostgreSQL |
| SQS | SQS_ENDPOINT, AWS_REGION=us-east-1, SQS_CREDENTIALS_FILE | CONSUMER_QUEUE e CONSUMER_DLQ; CONSUMER_PROVIDERS=provider-a,provider-b |
| Paralelismo | CONSUMER_CONCURRENCY=4, PUBLISHER_CONCURRENCY=4 | Limite16 por papel; não paraleliza uma mesma carteira |
| Saída | PUBLISHER_QUEUE=wallet-events.fifo | Eventos imutáveis/eventId estável; consumo deduplica eventId |
| Lifecycle | START_TIMEOUT=30s, SHUTDOWN_TIMEOUT=20s, STOP_GRACE_PERIOD=30s | Docker deve permitir mais tempo que o shutdown |
| Diagnóstico | INSTANCE_ID opcional, LOG_LEVEL=info | Cada processo gera também identidade própria; /metrics interno |

| Cliente Keycloak | Permissões | Segredo de exemplo |
| --- | --- | --- |
| wallet-internal | wallet:admin e wagering:read; carteira/ledger/reconciliação/métricas | local-example-wallet-internal-secret |
| provider-a | wagering:read/write; provider_id=provider-a | local-example-provider-a-secret |
| provider-b | wagering:read/write; provider_id=provider-b | local-example-provider-b-secret |
| provider-a-short-lived | Token curto para teste de expiração | local-example-provider-a-short-lived-secret |
| no-roles | Autenticado sem permissões, para teste403 | local-example-no-roles-secret |

Alterar senhas de PostgreSQL após inicialização do volume não altera os papéis já criados; faça a alteração administrativa correspondente. Alterar os secretos do realm também exige reaplicar a configuração no IdP já inicializado. O script de demonstração aceita secretos através das variáveis KC_SECRET_* do processo; não os imprime. Se alterar portas, ajuste o issuer no seu `.env` e URLs dos exemplos.

## Fluxo autenticado reproduzível

A demonstração cria uma carteira **nova** e operações locais; não apaga históricos nem imprime tokens. Exercita o binário real em Docker, IdP, abertura, BET/WIN/LOSS, replay com saldo original, leituras/paginação, reconciliação, métricas e acesso sem token recusado. `-WithSQS` envia o mesmo envelope duas vezes com IDs de deduplicação diferentes e verifica replay pelo HTTP.

```powershell
pwsh -File scripts/demo.ps1 -WithSQS
```

Exemplo manual Bash (curl/jq), com os padrões locais:

```sh
base=http://localhost:18081
token_url=http://localhost:18080/realms/jungle-gaming/protocol/openid-connect/token
internal=$(curl -fsS -d grant_type=client_credentials -d client_id=wallet-internal -d client_secret=local-example-wallet-internal-secret "$token_url" | jq -r .access_token)
provider=$(curl -fsS -d grant_type=client_credentials -d client_id=provider-a -d client_secret=local-example-provider-a-secret "$token_url" | jq -r .access_token)
player=0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1
wallet=$(curl -fsS "$base/wallets" -H "Authorization: Bearer $internal" -H 'Content-Type: application/json' -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","initialBalance":{"amount":"100.00","currency":"BRL"}}' | jq -r .id)
body=$(jq -n --arg wallet "$wallet" --arg player "$player" '{providerId:"provider-a",externalTransactionId:"example-bet",playerId:$player,walletId:$wallet,roundId:"round-1",gameId:"fortune-chimp",kind:"BET",money:{amount:"25.00",currency:"BRL"}}')
curl -i "$base/wagering/transactions" -H "Authorization: Bearer $provider" -H 'Content-Type: application/json' -H 'Idempotency-Key: example-key' -d "$body"
# Repetir a linha anterior: 200/replay, mesmo transactionId e saldo75.00.
curl -fsS "$base/providers/provider-a/wagering/transactions/example-bet" -H "Authorization: Bearer $provider"
curl -fsS "$base/wallets/$wallet" -H "Authorization: Bearer $internal"
curl -fsS "$base/wallets/$wallet/ledger?limit=50" -H "Authorization: Bearer $internal"
curl -fsS -X POST "$base/wallets/$wallet/reconciliation" -H "Authorization: Bearer $internal"
curl -fsS "$base/metrics" -H "Authorization: Bearer $internal"
unset internal provider
```

O exemplo manual usa IDs fixos: executá-lo novamente exige outro playerId/externalTransactionId ou tratar409 da abertura. A demonstração automática sempre usa IDs novos. Cursor do ledger é opaco: passe `nextCursor` por URL encoding na próxima consulta. O provedor não tem acesso às rotas de carteira; consultas/replays de transação são limitados ao seu providerId.

| Resultado HTTP | Código | Comportamento |
| --- | --- | --- |
| Processada nova / replay | 201 / 200 | Resultado confirmado; replay preserva saldo original |
| Referência pendente | 202 | Worker continua; consultar transactionId |
| Rejeição de negócio | 422 | failureCode definitivo e resultado persistido |
| Entrada inválida / conflito | 400 / 409 | Nenhuma nova movimentação |
| Ausência de credencial / sem permissão | 401 / 403 | Sem execução financeira |
| Registro não acessível | 404 | Não revela transação de outro provedor |
| Indisponibilidade / erro interno | 503 / 500 | 503 de negócio informa Retry-After; repetir com mesma chave |

Corpos, códigos e contratos completos em [CONTRATOS.md](docs/CONTRATOS.md). REFUND/ROLLBACK exigem referência externa; zero só em abertura e LOSS. OPENING não pode entrar por HTTP/SQS.

## SQS, eventos e recuperação

Filas provisionadas: wager-transactions.fifo (entrada), wager-transactions-dlq.fifo (redrive) e wallet-events.fifo (saída). Produtor é um gateway interno confiável: autentica o provedor antes de formar data.providerId. Não distribuímos credenciais desse gateway a provedores arbitrários.

Sem AWS CLI instalado no host:

```sh
docker compose run --rm broker-cli --profile wager-producer sqs get-queue-url --queue-name wager-transactions.fifo
docker compose run --rm broker-cli --profile events-reader sqs get-queue-url --queue-name wallet-events.fifo
```

Use o envelope de [CONTRATOS.md](docs/CONTRATOS.md), incluindo messageId/type/occurredAt/data.idempotencyKey; não confunda o messageId do envelope com o ID gerado pelo broker. Na entrada, MessageGroupId=walletId canônico; MessageDeduplicationId recomendado=messageId. Inbox e idempotência SQL mantêm a correção mesmo fora da janela de dedup do FIFO. `scripts/demo.ps1 -WithSQS` fornece envio executável completo sem montar arquivos no host.

Consumidor: visibilidade30s, operação10s, ACK3s, redrive após10 recebimentos, retry transitório2/4/8/16/32/60s; mensagens inválidas chegam à DLQ. SIGTERM interrompe busca e drena/libera receipts conhecidos; resposta de Receive perdida reaparece por expiração natural. Publisher: lease30s, envio10s, confirmação3s, retry limitado a60s e sem descarte de evento pendente. Tokens de claim e relógio DB protegem retomada; eventId e payload não mudam. Consumidores da saída deduplicam eventId e lidam com publicação fora de ordem usando walletVersion; FIFO não representa ordem de commit SQL.

## Migrations

SQL versionado/embutido em migrations/. Startup aplica `up`; versão atual3. Nunca reverter o banco com histórico que queira preservar: `down` é destrutivo. Para exercitar a reversão, a integração cria banco descartável e faz up/down/up.

```sh
docker compose run --rm migrate version
docker compose run --rm migrate up
# Somente em banco descartável conhecido:
# docker compose run --rm migrate down 1
# docker compose run --rm migrate up
```

## Verificação e testes separados

Unitários, race e vet (equivalentes aos comandos Go exigidos):

```sh
docker compose run --rm go test ./...
docker compose run --rm go test -race ./...
docker compose run --rm go vet ./...
docker compose run --rm check  # gofmt/mod verify/tidy/vet/unit-race/govulncheck
```

Com Go1.27.1/compilador C local, `go test ./...`, `go test -race ./...` e `go vet ./...` rodam diretamente. Esses comandos sem tags não executam a integração. Dependências dos testes em projeto separado, portas25432/28080/28081 e volumes jungle-gaming-test:

```sh
docker compose --env-file tests/test.env up -d --wait --build api
docker compose --env-file tests/test.env run --rm integration
# A integração já usa -race -tags integration -count=1 -timeout480s.
```

Três processos e falhas da aplicação, sem pausar os containers:

```sh
docker compose --env-file tests/test.env run --rm --no-deps --entrypoint go integration test -race -tags integration -count=1 -timeout480s -run '^TestStage11' ./tests/integration/...
```

Pausas reais PG/SQS exigem supervisor no host, build tags integration,distributed. Não executar duas suítes simultâneas no mesmo projeto:

```powershell
pwsh -File scripts/test-distributed.ps1
# Toda integração, incluindo pausas e cenários anteriores:
pwsh -File scripts/test-distributed.ps1 -RunPattern '.'
```

O supervisor verifica labels e restaura apenas containers de teste; não monta docker.sock nos serviços. Helpers child-only executam a composição Fx de produção em processos próprios; não são três containers cmd/service. SIGKILL usa barreiras somente no binário de testes/scratch DB e leases/visibilidade reais. Evidências em tests/.stage11-*/. [Etapa11](docs/VALIDACAO-FINAL.md) e [catálogo de testes](docs/TESTES.md) descrevem cenários e limites.

Ensaio completo a partir de cópia limpa e **novos** volumes/filas/IdP/cache, preservando desenvolvimento e testes existentes:

```powershell
pwsh -File scripts/validate-clean.ps1
```

Copia fontes verificadas por SHA256 (sem .env real/credenciais/logs/binários) para uma pasta irmã jungle-gaming-validation-<id>/source; sobe outro projeto com portas livres, executa demonstração do binário real e SQS, check, migrations, configuração, SIGTERM e toda integração/race/distribuição/pausas. Isola opções locais e COMPOSE_* herdadas, restaurando o ambiente ao terminar. Guarda evidence/ e encerra somente o projeto criado, preservando volumes. Não cria pacote de entrega nem publica/enviará arquivos. [Relatório final](docs/VALIDACAO-FINAL.md).

`pwsh -File scripts/validate-clean.ps1 -EnvironmentCheckOnly` verifica esse isolamento e a restauração com opções herdadas deliberadamente inválidas, sem iniciar containers.

```sh
docker compose --env-file tests/test.env down  # preserva volumes de teste
```

## Limites da solução

A imagem MiniStack local inclui checkpoint SQS/IAM com fsync antes da resposta; eventos aceitos, reentrega e Delete foram preservados sob SIGKILL nos testes REV-01. Snapshots completos custam throughput conforme cresce o estado; requer volume preservado e filesystem que respeite fsync. O MiniStack padrão sem essa adaptação não tem a mesma garantia. Autenticação local funciona no perfil SigV4/IAM documentado em [deploy/sqs/strict/README.md](deploy/sqs/strict/README.md). Ambiente usa HTTP/start-dev/sslmode=disable e secretos de exemplo para execução local. Em produção, TLS/IdP/banco externo/AWS SQS e gestão de segredos são necessários.

Testes comprovam os cenários especificados; não são garantia universal nem benchmark. Mesma carteira serializa, tabelas de auditoria crescem e pool por réplica exige orçamento de conexões. Tracing, dashboards, partidas dobradas e carga são opcionais ausentes. Não há RPS mínimo no enunciado. Decisões e interpretações, inclusive FAILED e gateway confiável, em ARCHITECTURE.md. Este projeto não é produto oficial da Jungle Gaming.
