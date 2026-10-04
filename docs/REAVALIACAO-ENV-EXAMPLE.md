# Reavaliação independente com .env.example — 2026-10-04

Resultado: os requisitos obrigatórios conferidos correspondem à implementação e os cenários executados passaram. Não reproduzi falha financeira nem acesso indevido nesta rodada. Encontrei e corrigi um erro do probe operacional de readiness. A conclusão se limita aos cenários e às interpretações abaixo; não é garantia universal de ausência de bugs nem previsão de nota.

## Base e ambiente

Releitura do [enunciado cru](DESAFIO-ORIGINAL.md), inspeção das fontes e novas execuções. Resultados anteriores não foram usados como prova desta rodada. A pasta de entrega foi copiada para ambiente isolado, com novos volumes, banco, IdP, filas e caches. `.env` foi uma cópia byte a byte de `.env.example`, sem alterar valores/portas/papéis: API 18081, IdP 18080, PostgreSQL 15432. Apenas o nome do projeto Compose foi isolado: `jungle-gaming-validation-74887ba27953`. Variáveis herdadas correspondentes ao exemplo e opções COMPOSE foram neutralizadas.

SHA256 do exemplo: `2b9e0f53e66650d100f849cc468432a91c3488a2c1039f41645077fce9d6f7c5`. [Prova do ambiente](evidence/env-example/example-env.json), [execução](evidence/env-example/results.json) e [fontes congeladas](evidence/env-example/source-sha256.json). 104 arquivos de runtime/infra/migrations/módulos do material entregue conferidos idênticos à cópia executada. O novo probe literal foi adicionado depois do snapshot inicial; ele e a correção de operations foram executados separadamente e constam no manifesto final.

## Execução nova

| Verificação | Resultado e evidência |
| --- | --- |
| Build/startup, PostgreSQL, Keycloak, MiniStack e provisionamento | [PASS](evidence/env-example/startup-build.log) |
| Demonstração API real e SQS | [PASS](evidence/env-example/demo.log) |
| Migrations | [Versão 3, clean](evidence/env-example/migration-version.log); roundtrip na integração |
| gofmt, modules verify/tidy, vet normal/com tags, unitários -race | [PASS](evidence/env-example/check.log) |
| govulncheck | Sem vulnerabilidade conhecida detectada nesta execução; mesmo log |
| Imagem nonroot e SIGTERM exit0 | [PASS](evidence/env-example/results.json) |
| Integração/distribuição/falhas com -race | [80 testes principais PASS em 273.464s](evidence/env-example/distributed.log) |
| HTTP externo em três containers reais da mesma imagem | [7/7 PASS](evidence/env-example/final-blackbox.json); [identidades](evidence/env-example/final-real-services.json) |
| SQL/restart/identidades/readiness/crash do broker | [5/5 PASS após correção do executor](evidence/env-example/final-operations.json) |
| Exemplos literais HTTP/reconciliação/paginação/replay | [PASS](evidence/env-example/exercise-examples.json) |
| Reviewer Money/parser com -race | [PASS](evidence/env-example/reviewer-domain-race.log) |
| Fuzz parser 15s/dois workers | [PASS, 38213 execuções](evidence/env-example/parser-fuzz.log) |
| Autenticação/durabilidade do broker local | [Cinco testes PASS](evidence/env-example/broker-python-tests.log) |
| Cleanup | [PASS](evidence/env-example/final-probes-cleanup.log); volumes/evidências preservados |

Os dois SKIP no processo pai são `TestStage8CrashConsumerHelper` e `TestStage11ProcessHelper`: helpers executados pelos testes de recuperação em processos filhos. Os testes que os invocam passaram. Não representam cenários de recuperação omitidos.

O probe literal abriu 1000.00, debitou 25.00, recebeu 975.00 e reconciliou exatamente os seis campos do contrato com dois lançamentos. Paginação limit 1 devolveu dois IDs sem repetição. WIN 5.00 levou a carteira a 980.00/versão 3; replay da BET conservou 975.00 e não modificou a carteira.

## Conferência por requisito

As linhas abaixo cobrem as 15 seções e seus grupos obrigatórios. Referências são nomes reais presentes nos logs novos ou fontes inspecionadas, não resultados reaproveitados. O [JSON](REAVALIACAO-ENV-EXAMPLE.json) preserva fontes e evidências por linha.

| Seção | Ponto conferido | Comportamento/decisão | Evidência nova |
| --- | --- | --- | --- |
| 1 | Objetivo | HTTP e consumidor usam o mesmo caso de uso; saldo/ledger recuperáveis entre processos. | TestStage11ThreeProcessesConcurrencyRestartAndTransports |
| 2 | Autenticação e autorização | Keycloak real, client_credentials, issuer/audience/assinatura/exp; identidade determina provider; consultas/replay isolados e carteira/metrics internos. Credenciais e políticas do broker verificadas. | TestStage6AuthenticatedHTTPAndProviderIsolation; TestStage6ExpiredAndForgedKeycloakTokens; TestBrokerRejectsAuthenticationBypass |
| 3 | Falhas distribuídas | Duplicatas HTTP/SQS, referência antecipada, disputas, morte antes/depois do commit e interrupção de dependências. | TestStage11HTTPKillBeforeAndAfterCommit; TestStage11RealContainerOutagesAndRecovery; TestStage11FxConsumerKilledAfterCommitBeforeDelete |
| 4 | Stack e persistência | Go declarado, Modules/sum, Fx, net/http, pgx com SQL explícito, PostgreSQL, MiniStack adaptado e Compose; migrations up/down testadas. | check.log; TestMigrationsRoundTrip; TestIntegrityMigrationPreservesExistingHistory |
| 4 | Fx e I/O | Construtores e módulos; startup validado; contextos/prazos; ordem de parada, término de workers e dependências fechadas. | TestStage10FxAllRolesPublishesAndShutsDown; TestStage6TransientHTTPFailureAndLifecycle; stop-api.log |
| 5 | Garantias financeiras | Dinheiro inteiro; idempotência e invariantes no banco; eventos só pós-commit; ledger imutável; coordenação por carteira; sem lost updates. | TestDatabasePreventsLostUpdateWithoutAppLock; TestLedgerProtections; TestStage9UncommittedInvisibleAndNetworkOutsideWalletLock |
| 6 | Encapsulamento | Estado privado, criação/reidratação separadas, zero values rejeitados, erros classificáveis e I/O com contexto; domínio sem bibliotecas de infraestrutura. | check.log (unitários -race e testes de arquitetura) |
| 6.1 | Money | String decimal/duas casas/BRL; sem float, arredondamento ou exponencial; compatibilidade de moedas e overflow em parsing/soma/subtração/negação; BIGINT e moeda exatos. | check.log; reviewer-domain-race.log; final-blackbox.json |
| 6.2 | Wallet | Unicidade jogador/moeda, identidade/timestamps, saldo não negativo, ledger no mesmo commit; versão inicial1, incrementada só por mudança efetiva. | TestWalletProtections; TestStage5OpeningAndBusinessResults; exercise-examples.json |
| 6.3 | WagerTransaction | Metadados, chave/hash, referência resolvida, estados/resultado/failureCode persistidos; estados terminais protegidos. Sem commit intermediário de PENDING. FAILED durável no worker; erro síncrono desfaz operação. | TestTransactionProtections; TestStage7PermanentFailureIsDurableAndTerminal; TestStage7TransientFailureNeverFinalizes |
| 6.3 | OPENING | Origem interna distinta e crédito único; valor positivo cria ledger e dois eventos no commit, versão 1; zero não cria eventos financeiros; proibida externamente. | TestStage5OpeningAndBusinessResults; TestStage6StrictJSONAndLedgerPagination; final-blackbox.json |
| 6.4 | Ledger | Lançamento validado antes/depois/direção, unicidade carteira/transação e bloqueio de edição/exclusão; LOSS/rejeição sem lançamento. | TestLedgerProtections; TestSchemaAcceptsCoherentHistory; TestStage5OpeningAndBusinessResults |
| 6.5 | Inbox/outbox | Identidade/hash/conclusão, eventos com snapshot imutável, tentativas/lease/backoff; inbox e domínio compartilham UOW. | TestStage8InboxRollbackAtEveryWrite; TestInboxAndOutboxProtections |
| 7 | Tipos e reversões | Cinco tipos/valores zero, referências por provedor, jogador/carteira/moeda/rodada e valor integral; direções corretas, devolução única, insuficiência de reversão distinta de BET. | TestStage7DirectRollbackDirections; TestStage7DurableResolutionAndCombinedReversals; TestStage12ReferenceMetadataBoundaries; final-blackbox.json |
| 7 | Referências pendentes | Pendência durável, backoff exponencial/TTL, retomada após restart, referência pendente/terminal inválida e expiração REFERENCE_NOT_FOUND com rejeição/evento. | TestStage11ReferencesExpireAndInvalidAcrossProcesses; TestStage7PendingChain; TestStage7RecoveryRollbackAtEveryWrite |
| 8 | Concorrência | Três processos Fx e três containers cmd/service; 100.00 e duas BET 80.00 resultam 20.00/um débito/uma rejeição; 50 duplicatas e outras carteiras em paralelo. | TestStage11ThreeProcessesConcurrencyRestartAndTransports; TestStage12ThreeProcessesContestReplays; final-blackbox.json |
| 9 | HTTP e idempotência | Abertura/leitura/ledger opaco/consultas isoladas/envio com chave recebida; SHA256 de JSON canônico sem transporte/chave; normalizações documentadas; conflitos/pendências/erros distinguíveis e resultado original no replay. | TestStage6AmbiguousHeadersRejected; TestStage6StrictJSONAndLedgerPagination; exercise-examples.json; final-blackbox.json |
| 9 | Reconciliação e health | Snapshot SQL único, abertura incluída, stored-calculated; sem modificar saldo; divergência em resposta/log/métrica; live/ready públicos e filas configuradas verificadas. | TestStage10ReconciliationExactSnapshotAndLargeTurnover; TestStage10RealAuthorizationMetricsAndDivergence; final-operations.json; exercise-examples.json |
| 10 | SQS | FIFO/DLQ/redrive provisionados; envelope estrito/messageId+hash duráveis; ACK pós-commit, rejeição terminal, retry/backoff e inválidas/exaustão para DLQ. Group=wallet canônica e dedup não substitui aplicação. | TestQueuesAreProvisioned; TestStage8MalformedMessagesReachRealDLQ; TestStage8ExhaustedTransientRetryReachesDLQ; TestStage8GroupsParallelAndRetryKeepsFIFOOrder |
| 10 | SQS shutdown e equivalência | Para busca em SIGTERM, conclui/libera visibilidade; HTTP/SQS mesma operação e inbox atômica, inclusive referência pendente. | TestStage8RealConsumerHTTPAndPendingReference; TestStage11FxConsumerKilledAfterCommitBeforeDelete; TestStage10FxAllRolesPublishesAndShutsDown |
| 11 | Transactional outbox | Atomicidade e publicação pós-commit, workers concorrentes com SKIP LOCKED/lease/backoff; evento recuperado e eventId estável depois de send sem confirmação. | TestStage9ClaimsLeaseAndStaleConfirmation; TestStage11FxPublishersKilledBeforeSendAndBeforeConfirmation; TestStage9RealPublishersRetryRecoveryAndSnapshots |
| 11 | Eventos e destino | Quatro tipos concretos/envelope versionado por construtor/UTC/RFC3339/money string; WalletBalanceChanged completo. wallet-events.fifo documentada; snapshots e eventId imutáveis. | check.log (eventos unitários); TestStage9RealPublishersRetryRecoveryAndSnapshots; TestStage9WalletEventsMayArriveOutOfCommitOrder |
| 12 | Observabilidade | Logs JSON com identidades disponíveis e erros sanitizados; métricas por status/duplicata/retry/DLQ/conflito/outbox/latência/divergência, sem IDs como labels. | TestStage12ReferenceLogKeepsInputMessageIdentity; TestStage12FailedMetricRequiresConfirmedAudit; final-operations.json |
| 13 | Verificação obrigatória | Unitários, real containers, schema/atomicidade, Fx lifecycle, auth, 50 duplicatas, disputa80/100, paralelismo, três processos, crash/ACK/publish/referências/restart/HTTP-SQS com reconciliação e race. | distributed.log (80 PASS); check.log; final-blackbox.json; final-operations.json |
| 14 | Eliminatórios e opcionais | Nenhum eliminatório reproduzido nesta rodada; garantias anteriores cobrem os dez. Carga, partidas dobradas, tracing e dashboards opcionais não implementados; pontuação cabe ao avaliador. | Logs e resultados novos desta revisão |
| 15 | Entrega | Código/migrations/Compose/env example/IdP/filas e identidades locais; README pré-requisitos, startup/down, exemplos autenticados, testes/tags; decisões e limites em arquitetura. Sem publicação. | startup-build.log; demo.log; check.log; migration-version.log; revisão documental |

## Achado e correções desta rodada

REV-EXAMPLE-01: `tests/reviewer/operations.py` fixava `tests/validation.env` na criação do container de readiness. A primeira tentativa usando `.env` não conseguiu criar esse container; o `finally` tentou parar um container inexistente e mascarou o erro original. [Primeira tentativa preservada](evidence/env-example/first-attempt-operations.json). Não foi uma falha financeira observada, nem um PASS daquele cenário.

Codex adicionou `--env-file`, com padrão anterior para compatibilidade, e condicionou stop/rm à criação bem-sucedida do container. A política temporária continua sendo removida no finally. Reexecutados todos os sete grupos HTTP, o exemplo literal e os cinco grupos operacionais com `--env-file .env`: todos PASS. Novo `tests/reviewer/exercise_examples.py` faz a aceitação literal por HTTP sem importar a aplicação. Código produtivo, schema, dinheiro e dependências não foram alterados.

## Interpretações e limites para o avaliador

- Não há commit intermediário de PENDING no caminho síncrono; o requisito de retomada desse estado é condicional. PENDING_REFERENCE é durável e outro worker assume. FAILED é terminal e auditado no processamento assíncrono; falha técnica síncrona desfaz a operação e o cliente deve consultar/repetir a mesma identidade, especialmente quando o resultado do commit é incerto.
- Só uma reversão direta bem-sucedida de uma BET devolve seu débito, incluindo a combinação REFUND/ROLLBACK. ROLLBACK de REFUND é permitido na direção oposta. Referências pendentes/terminais e insuficiência de saldo têm políticas/códigos documentados.
- A identidade limita o provedor às suas transações. O enunciado não define vínculo comercial jogador/provedor; o produtor SQS é um gateway interno confiável, com credenciais próprias. Não distribuir credenciais internas de broker aos provedores.
- A mesma carteira serializa no PostgreSQL; carteiras diferentes avançam em paralelo. Isso prova correção concorrente, não capacidade ilimitada: pool por réplica e crescimento de ledger/inbox/outbox exigem planejamento. Throughput e p95/p99 não medidos.
- A outbox é at-least-once. Não promete ordem de commits entre publishers; consumidores precisam deduplicar eventId e usar walletVersion para projeções. Repetições e publicação fora de ordem estão cobertas nos testes.
- O MiniStack local foi adaptado para políticas/autenticação e persistência antes da resposta. SIGKILL preservou dois eventos aceitos, ReceiveCount e Delete. Snapshots completos custam conforme cresce o estado; exige volume preservado e filesystem com fsync correto. Não testamos perda física do volume nem equivalência integral com AWS.
- `.env.example` usa HTTP/start-dev/sslmode=disable e credenciais locais públicas de exemplo. Adequado ao desafio local; produção requer TLS, segredos externos e infraestrutura apropriada. Nenhum teste de produção/rolling upgrade foi executado. Tracing, dashboards, partidas dobradas e carga são opcionais ausentes.

## Como repetir o exemplo literal

Em uma cópia isolada com `.env` copiado de `.env.example`, portas originais livres e API iniciada:

```powershell
python tests/reviewer/exercise_examples.py --api http://127.0.0.1:18081 --idp http://127.0.0.1:18080 --output exercise-examples.json
```

Para a suíte completa, usar o mesmo projeto/env nas etapas: `docker compose -p <projeto> --env-file .env up -d --wait --build api`, `docker compose -p <projeto> --env-file .env run --rm check`, parar a API e executar `pwsh -File scripts/test-distributed.ps1 -ProjectName <projeto> -EnvFile .env -RunPattern '.'`. O [roteiro](ROTEIRO-DE-AVALIACAO.md) prepara três containers e descreve os probes externos; usando o exemplo literal, passar `--env-file .env` também a operations. Não executar pausas/purgas em ambientes com dados necessários.

O material técnico está na pasta de entrega; comunicação dos agentes permanece na pasta privada AUTOMATION. Nenhum Git, ZIP, upload ou envio foi executado.
