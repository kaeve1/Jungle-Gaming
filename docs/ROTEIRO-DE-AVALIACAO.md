# Roteiro completo de avaliação

Este é um roteiro reproduzível para avaliar o projeto. Não sabemos a ordem nem os testes particulares que o entrevistador escolherá. Os comandos abaixo correspondem aos scripts existentes; os resultados executados desta versão estão em VALIDACAO-FINAL.md.

Execute os comandos na raiz, onde está `compose.yaml`. Docker com containers Linux e Compose v2 são necessários. Para os scripts use PowerShell 7 (`pwsh`). Go no host é opcional. Python 3 é necessário apenas para os probes externos; eles usam a biblioteca padrão. A primeira execução precisa de internet para baixar imagens e módulos. Portas ocupadas ou pouca memória podem impedir o startup; consulte os logs antes de atribuir a falha à regra financeira.

## 1. Leitura inicial

Leia [README](../README.md), [enunciado](DESAFIO-ORIGINAL.md), [arquitetura](../ARCHITECTURE.md), [contratos](CONTRATOS.md) e [matriz](MATRIZ-REQUISITOS.md). Compare os comportamentos obrigatórios com seus testes. O [mapa de pastas](ESTRUTURA.md) orienta a leitura do código.

O avaliador deverá encontrar regras em `internal/domain`, coordenação em `internal/application`, infraestrutura em `internal/adapters` e composição Fx em `internal/bootstrap`. Procure especialmente dinheiro sem float, transações SQL, locks por carteira, constraints, unicidade, autorização, timeouts, logs e retomada dos workers. O teste `internal/archtest` verifica fronteiras, mas não substitui a revisão humana.

## 2. Inicialização e saúde

```powershell
docker version
docker compose version
pwsh --version
docker compose config --quiet
docker compose up -d --wait --build api
docker compose ps -a
docker compose logs --tail 50 api
Invoke-RestMethod http://127.0.0.1:18081/health/live
Invoke-RestMethod http://127.0.0.1:18081/health/ready
docker compose run --rm migrate version
```

Esperado: startup sem provisionamento manual, API saudável e migration versão **3**, sem estado dirty. Jobs de provisionamento/migração podem aparecer como encerrados com código 0; não precisam permanecer rodando. IdP, papéis do banco, filas e credenciais são criados automaticamente. Não precisa criar `.env`; `.env.example` documenta os padrões locais.

Se falhar, examine `docker compose logs --tail 80` e identifique se foi build, porta, IdP, banco, broker, migration ou API. Não avance sobre um startup falho.

## 3. Caminho principal pelo serviço real

```powershell
pwsh -NoProfile -File scripts/demo.ps1 -WithSQS -ApiURL http://127.0.0.1:18081 -TokenURL http://127.0.0.1:18080/realms/jungle-gaming/protocol/openid-connect/token
```

O script gera IDs novos, obtém tokens reais no Keycloak e não imprime tokens.

| Ação | Resultado esperado |
| --- | --- |
| Consultar carteira sem token | 401 |
| Abrir carteira com identidade interna | 201; saldo 100,00 |
| BET de 25,00 | 201; saldo 75,00 |
| WIN de 2,00 | Saldo atual 77,00 |
| Repetir o BET | 200; mesmo transactionId; resultado original 75,00 |
| LOSS de zero | Não movimenta dinheiro nem incrementa a versão financeira |
| Ler ledger em páginas | Paginação sem repetição; cursor avança |
| Reconciliar | consistent=true; diferença zero |
| Consultar métricas com identidade interna | 200; métricas disponíveis |
| Enviar WIN de 1,00 por SQS duas vezes, com dedup IDs distintos | Um único crédito; saldo 78,00 |
| Repetir essa operação pelo HTTP | Replay da operação processada via SQS |

Esperado: duas linhas `PASS` ao final. Esta demonstração não cobre sozinha todos os tipos de reversão, todas as permissões ou recuperação de falhas. Exemplos HTTP manuais, tokens e payloads estão no [README](../README.md); contratos detalhados em [CONTRATOS](CONTRATOS.md).

## 4. Qualidade, unitários e race

```powershell
docker compose run --rm --no-deps check
```

Esperado: exit 0 e `OK: todas as verificações passaram`. Executa gofmt, módulos verificáveis, tidy sem diferenças, vet normal e com tags de integração, unitários com `-race` e govulncheck. Ausência de achados do govulncheck significa ausência de vulnerabilidades conhecidas detectadas naquela execução, não ausência de toda falha de segurança.

Para investigar uma falha separadamente:

```powershell
docker compose run --rm --no-deps go test ./...
docker compose run --rm --no-deps go test -race -count=1 ./...
docker compose run --rm --no-deps go vet ./...
docker compose run --rm --no-deps go test -run '^$' -fuzz FuzzDuplicateAndTrailingObjects -fuzztime 10s -parallel 2 ./internal/adapters/jsonstrict
```

`go test ./...` sem tags não executa a suíte de integração. O fuzz adicional exercita o parser estrito; não mede capacidade da API.

## 5. Validação completa recomendada

```powershell
pwsh -NoProfile -File scripts/validate-clean.ps1
```

Este é o ponto de entrada recomendado para avaliar o conjunto. Cria pasta irmã `jungle-gaming-validation-<id>`, cópia verificada das fontes e outro projeto Compose com portas livres e recursos novos. Não usa os volumes de desenvolvimento. Execute sem `-SkipDistributed` para incluir as falhas distribuídas.

| Etapa do script | O que o avaliador comprova |
| --- | --- |
| startup-build | Build e provisionamento a partir de fontes copiadas |
| real-service-demo | API real, IdP e fluxo HTTP/SQS |
| migration-version | Schema atual aplicado corretamente |
| check | Formatação, módulos, vet, unitários/race e scanner |
| compose-settings | Repasse das configurações, portas e grace period |
| production-sigterm-nonroot | Binário real encerra com exit 0 e usuário nonroot |
| integration-distributed-race | Toda integração, processos concorrentes, recuperação e pausas de dependências |
| cleanup | Encerra o namespace criado, preserva volumes e evidências |

Esperado: `PASS: complete clean validation`, `results.json` sem etapa failed e etapas executadas com exitCode 0. Ao terminar imprime `Evidence retained: ...`. O script demora vários minutos; o tempo depende do download, cache e máquina. Um teste marcado SKIP não prova o cenário ignorado: leia também `distributed.log`.

As provas ficam em `evidence/results.json`, `source-sha256.json`, `check.log`, `demo.log`, `distributed.log`, `startup-build.log` e outros logs por etapa. Um resultado anterior não substitui o resultado da fonte atual. O script completo não inclui os probes Python da próxima seção.

## 6. Como avaliar cada garantia

| Garantia / tentativa de quebrar | Resultado que deve resistir | Onde verificar |
| --- | --- | --- |
| Dinheiro inválido, overflow, zero indevido | Rejeição sem movimentação indevida | money/domain, API, probes externos |
| JSON com chave duplicada, desconhecida ou conteúdo extra | Entrada recusada | jsonstrict, HTTP/SQS, reviewer |
| Token ausente, expirado, forjado, sem papel | 401/403 conforme contrato | api_test.go, Keycloak |
| Provider A consulta/reexecuta operação de B | Não revela nem executa operação alheia | api_test.go, probes externos |
| 50 cópias da mesma operação | Uma movimentação; resultado original preservado | application/integration, distribuídos, reviewer |
| Mesma chave com outro conteúdo | 409, sem segundo efeito | API e testes de idempotência |
| Dois BET concorrentes que excederiam o saldo | Não fica negativo; um é rejeitado quando aplicável | concorrência SQL e distribuída |
| Carteiras diferentes | Não dependem de um mutex global para correção | locks por carteira, testes de concorrência |
| REFUND/ROLLBACK combinados | Não devolvem duas vezes o mesmo débito | references_test.go, domínio, reviewer |
| Referência chega depois | Pendência durável retomada pelo worker | references_test.go, recuperação distribuída |
| Reinício ou morte antes/depois do commit | Sem estado financeiro parcial ou efeito duplicado | distributed_test.go e distributed_recovery_test.go |
| HTTP e SQS representam a mesma operação | Mesmo resultado financeiro | demo e distributed_test.go |
| Receipt, inbox e reentrega | ACK não elimina operação ainda não confirmada | consumer*_test.go |
| Mensagem inválida repetida | Vai à DLQ conforme redrive | consumer_queue_test.go |
| Banco ou SQS temporariamente indisponível | Retentativa/retomada sem rejeição financeira falsa | distributed_outages_test.go |
| Publicação falha ou processo morre | Outbox permanece retomável; eventId/payload estáveis | outbox_test.go e recuperação distribuída |
| Escrever no banco burlando o caso de uso | Constraints rejeitam inconsistências | schema*_test.go |
| Credenciais indevidas no broker | Assinatura/políticas recusam acesso | sqs_test.go, broker_security_test.go |
| Saldo versus lançamentos | Reconciliação exata, diferença zero | reconciliation_test.go e SQL externo |
| Logs e métricas | IDs corretos, autorização, sem segredos/payload completo | final_review_test.go, operações externas |

O catálogo [TESTES](TESTES.md) detalha os nomes e as limitações de cada teste.

## 7. Integração por partes, para diagnóstico

Se já executou a validação completa com sucesso, não precisa repetir estes passos. Esta alternativa usa `jungle-gaming-test` e as portas definidas em `tests/test.env`. Não execute duas suítes no mesmo namespace simultaneamente.

```powershell
docker compose --env-file tests/test.env up -d --wait --build api
# Para evitar competição da API com os consumers dos testes:
docker compose --env-file tests/test.env stop api
docker compose --env-file tests/test.env run --rm --no-deps integration
pwsh -NoProfile -File scripts/test-distributed.ps1 -RunPattern '.'
docker compose --env-file tests/test.env down
```

O serviço integration executa `-race -tags integration`; o supervisor acrescenta a tag distributed, pausa/restaura PG e SQS e registra `tests/.stage11-*/`. Helpers desses testes são processos independentes com a composição de produção; não equivalem a três containers cmd/service. Para avaliar esses containers use a próxima seção.

Para restringir uma investigação:

```powershell
docker compose --env-file tests/test.env run --rm --no-deps --entrypoint go integration test -race -tags integration -count=1 -timeout 480s -run '^TestStage6' ./tests/integration/...
```

Troque o padrão por um nome/grupo do catálogo. Não substitua o supervisor por um comando Go direto para os testes que exigem pausas externas.

## 8. Probes externos contra três containers reais

Complemento mais exigente: requisições HTTP externas, modelo independente de dinheiro inteiro e inspeção operacional. O probe operacional purga a fila de saída, remove fila temporária e mata o broker **apenas no ambiente de validação**. Use uma cópia isolada da seção 5; não execute contra desenvolvimento ou dados necessários.

Depois de executar a seção 5, entre na pasta `source` indicada pelo script. O `results.json` em `../evidence` contém namespace e portas dessa execução. Não reutilize namespaces antigos de relatórios como instrução fixa.

```powershell
# Dentro de jungle-gaming-validation-<id>/source:
$runInfo = Get-Content ../evidence/results.json -Raw | ConvertFrom-Json
$reviewProject = $runInfo.project
if ($reviewProject -notmatch '^jungle-gaming-validation-[a-f0-9]{12}$') { throw 'Namespace de validação inválido' }
$reviewApiPort = $runInfo.ports.api
$reviewIdpPort = $runInfo.ports.idp
docker compose -p $reviewProject --env-file tests/validation.env up -d --wait api
docker compose -p $reviewProject --env-file tests/validation.env run --no-deps -d --name "$reviewProject-review-api-2" -p '127.0.0.1::8080' -e INSTANCE_ID=review-api-2 api
docker compose -p $reviewProject --env-file tests/validation.env run --no-deps -d --name "$reviewProject-review-api-3" -p '127.0.0.1::8080' -e INSTANCE_ID=review-api-3 api
$reviewPeer2 = 'http://' + (docker port "$reviewProject-review-api-2" 8080/tcp).Trim()
$reviewPeer3 = 'http://' + (docker port "$reviewProject-review-api-3" 8080/tcp).Trim()
$reviewApi = "http://127.0.0.1:$reviewApiPort"
$reviewIdp = "http://127.0.0.1:$reviewIdpPort"
```

As portas dos peers são atribuídas pelo Docker para evitar colisões. Confira exit codes dos comandos Docker e aguarde HTTP 200 em `/health/ready` nas três URLs antes de continuar. Containers devem usar a mesma imagem construída na validação e compartilhar banco, broker e IdP. Os nomes são exigidos pelo probe operacional. Se a porta principal da cópia ficou ocupada desde a primeira validação, ajuste a configuração local da cópia e o issuer de modo coerente, ou faça uma nova validação.

```powershell
Invoke-RestMethod "$reviewApi/health/ready"
Invoke-RestMethod "$reviewPeer2/health/ready"
Invoke-RestMethod "$reviewPeer3/health/ready"
python tests/reviewer/blackbox.py --apis $reviewApi $reviewPeer2 $reviewPeer3 --idp $reviewIdp --output ../evidence/reviewer-blackbox.json
# Continue apenas se blackbox terminou com exit 0 e sete grupos PASS.
python tests/reviewer/operations.py --project $reviewProject --apis $reviewApi $reviewPeer2 $reviewPeer3 --idp $reviewIdp --blackbox ../evidence/reviewer-blackbox.json --output ../evidence/reviewer-operations.json
docker compose -p $reviewProject --env-file tests/validation.env run --rm --no-deps go test -race -tags reviewer -count=1 ./tests/reviewer
```

blackbox: sete grupos PASS, incluindo segurança, entradas adversariais, reversões, limites monetários, 50 duplicatas entre três serviços, referência pendente e 80 operações conferidas por modelo independente.

operations: cinco grupos PASS, incluindo saldo/ledger por SQL, reinício com replay original, IDs reais em logs, readiness de fila configurada e durabilidade/redelivery/Delete após SIGKILL do broker. Leia o JSON: FINDING sinaliza problema observado; PROBE_ERROR pode ser erro da montagem/teste e precisa de investigação. Exit 0 é obrigatório. O comando operations aceita `--env-file`; o padrão é `tests/validation.env`. Se a montagem usar outro arquivo, informe o mesmo arquivo ao probe.

O teste Go reviewer cobre UnmarshalJSON com conteúdo extra. Os probes não medem RPS e não substituem todas as outras suítes.

Ao terminar, confira labels e encerre somente esse projeto:

```powershell
docker compose -p $reviewProject --env-file tests/validation.env down --remove-orphans
docker ps -a --filter "label=com.docker.compose.project=$reviewProject" --format '{{.Names}}'
```

Se os peers one-off permanecerem, confirme que os labels pertencem a esse namespace e remova somente os dois nomes criados acima. Preserve volumes e JSON/logs; não use `-v`.

## 9. Aprovação e limites

Um resultado favorável exige startup reproduzível, fluxo funcional, check verde, integração completa com race, falhas distribuídas realmente executadas e invariantes preservadas. Para o complemento externo, exigir 7/7 HTTP e 5/5 operacionais. Falhas, skips e erros de harness devem ser explicados, nunca escondidos como aprovação.

Ainda são limites explícitos: produção não validada, padrões locais HTTP/credenciais de exemplo, capacidade/RPS não medidos, crescimento da auditoria, orçamento de conexões por réplica, ordenação de eventos por walletVersion e deduplicação do consumidor de saída. A durabilidade do broker depende da adaptação local, fsync e volume preservado; não cobre perda física do volume. Mesmo uma suíte verde não garante aprovação na entrevista nem ausência universal de bugs.

Na discussão, o entrevistador pode pedir que o candidato explique por que o replay retorna saldo original, por que FIFO não substitui idempotência no banco, como saldo/ledger/outbox ficam atômicos, como referências voltam após reinício e onde a identidade limita o acesso. Entender essas decisões faz parte da avaliação.
