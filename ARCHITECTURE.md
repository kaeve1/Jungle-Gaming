# Arquitetura e decisões

Este documento registra as decisões técnicas e as interpretações do enunciado ([docs/DESAFIO-ORIGINAL.md](docs/DESAFIO-ORIGINAL.md)). Contratos detalhados (HTTP, SQS, eventos, códigos de falha) estão em [docs/CONTRATOS.md](docs/CONTRATOS.md). A ligação requisito → componente → teste está em [docs/MATRIZ-REQUISITOS.md](docs/MATRIZ-REQUISITOS.md).

**Estado:** implementação disponível; testes e limites da versão entregue em [VALIDACAO-FINAL.md](docs/VALIDACAO-FINAL.md), conformidade em [CONFORMIDADE.md](docs/CONFORMIDADE.md).

---

## 1. Visão geral

Um único binário Go (`cmd/service`), executável em várias instâncias idênticas. Cada instância pode exercer quatro papéis, ligados por configuração (`APP_ROLES`, padrão atual: api; Compose ativa os quatro papéis; o binário direto assume api):

| Papel | Função |
| --- | --- |
| `api` | Servidor HTTP autenticado |
| `consumer` | Consumidor da fila `wager-transactions.fifo` |
| `reference-worker` | Retomada de operações em `PENDING_REFERENCE` |
| `outbox-publisher` | Publicação dos eventos da outbox em `wallet-events.fifo` |

Todas as instâncias compartilham um PostgreSQL, um Keycloak e um SQS local. **Nenhuma garantia financeira depende de memória do processo**: locks, unicidade, idempotência, pendências e eventos ficam no PostgreSQL. Por isso qualquer instância pode morrer a qualquer momento e outra assume.

```
 provedor ──HTTP+JWT──► api ─┐
                             ├──► caso de uso ProcessWager ──► PostgreSQL (1 transação SQL)
 produtor ──SQS FIFO──► consumer ┘                                │
                                                                   ├─ outbox ──► outbox-publisher ──► wallet-events.fifo
                                                                   └─ PENDING_REFERENCE ──► reference-worker
```

Organização de pacotes (pastas são criadas na etapa que as usa):

```
cmd/service                 main: valida a configuração e roda a aplicação Fx
internal/config             leitura e validação de variáveis de ambiente (sem Fx)
internal/domain             Wallet, WagerTransaction, LedgerEntry, pedido externo e hash, eventos, erros, decisão (Process)
internal/domain/money       value object Money e moedas
internal/application        casos de uso e portas (repositórios, UnitOfWork, IDs e diagnóstico; tempo SQL pela Session)
internal/adapters/postgres  repositórios pgx, UnitOfWork, classificação de erros do banco
internal/adapters/http      handlers, middleware de autenticação, mapeamento de erros
internal/adapters/sqsconsumer e outboxpublisher: consumidor e publisher
internal/adapters/auth      validação de JWT (JWKS do Keycloak)
internal/bootstrap          ÚNICO lugar com Fx: um arquivo por módulo, lifecycle, papéis
internal/archtest           testes que impõem as fronteiras abaixo
migrations                  SQL versionado (up/down)
deploy                      realm do Keycloak, provisionamento de filas, init do PostgreSQL
scripts                     scripts executados dentro de containers (ex.: check.sh)
tests                       integração, múltiplas instâncias, falhas (build tags)
```

**Fronteiras, verificadas por `internal/archtest` (o build de testes falha se forem violadas):**

| Pacote | Pode importar | Não pode importar |
| --- | --- | --- |
| `domain` | biblioteca padrão e o próprio domínio | qualquer outra coisa (inclusive `net/http`, `database/sql`) |
| `application` | domínio, biblioteca padrão | Fx, pgx, AWS, `net/http`, `database/sql`, adapters, bootstrap |
| `adapters/*` | application, domain, bibliotecas de infraestrutura | Fx, bootstrap |
| `config` | biblioteca padrão | Fx, outros pacotes internos |
| Fx | só `internal/bootstrap` e `cmd` | — |

Concentrar o Fx no `bootstrap` faz com que domínio, casos de uso e adaptadores sejam construtores Go comuns, testáveis sem o container de injeção, e permite trocar a composição sem tocar na regra de negócio.

**Identidade de instância:** `INSTANCE_ID` (ou o hostname) aparece nos logs; além disso cada execução gera um `ProcessID` único (`INSTANCE_ID/aleatório`). É o `ProcessID` que fica gravado como prefixo dos tokens de leases da outbox; referências usam locks SQL transacionais, para que duas réplicas iniciadas com o mesmo `.env`, ou um processo reiniciado com o mesmo nome, nunca confirmem trabalho uma da outra.

## 2. Stack

| Item | Escolha | Motivo |
| --- | --- | --- |
| Go | 1.27.1, fixada em go.mod/Dockerfile/toolchain | exigência |
| Composição | Uber Fx | exigência |
| HTTP | `net/http` com o roteador da biblioteca padrão (padrões `GET /wallets/{id}`) | sem dependência extra |
| Banco | PostgreSQL + `pgx/v5` com SQL explícito | preferência do enunciado; locks e transações visíveis no código |
| Migrations | `golang-migrate` v4.20.1, SQL embutido no binário `cmd/migrate` (`migrations/*.sql`) | up/down versionados; serviço `migrate` do Compose aplica antes da aplicação |
| IdP | Keycloak 26.8.0 (`start-dev`), realm importado de `deploy/keycloak` | recomendado no enunciado |
| PostgreSQL | 18.6 | versão estável atual |
| JWT | coreos/go-oidc v3 (go-jose transitivo), JWKS remoto/cache, RS256 | validar assinatura, não apenas decodificar |
| SQS | AWS SDK for Go v2 contra **MiniStack 1.5.20** com `AUTH=true` e verificação SigV4 na imagem local `deploy/sqs/strict` | o LocalStack atual exige token de conta para iniciar (testado), o que impediria reproduzir o projeto num checkout limpo; limitações do MiniStack em VULN-007 |
| Logs | `log/slog` em JSON | biblioteca padrão |
| Métricas | formato de exposição Prometheus próprio, GET /metrics protegido na porta HTTP | padrão de mercado |
| IDs | UUIDv7 gerados pela aplicação | ordenáveis no tempo; os exemplos do enunciado são v7 |

## 3. Dinheiro (`Money`)

**Representação:** `int64` em unidades mínimas (centavos) + código de moeda. Nunca `float32`/`float64` em parsing, cálculo, JSON, hash ou banco.

- **Moedas suportadas:** `BRL`, `USD`, `EUR` — todas ISO 4217 com 2 casas decimais. Ter mais de uma moeda permite testar incompatibilidade de verdade. Códigos desconhecidos ou com outra escala (ex.: `JPY`) são recusados como entrada inválida.
- **Formato de entrada externa (estrito, sem normalização):** expressão `^(0|[1-9][0-9]*)\.[0-9]{2}$`.
  - Aceito: `"0.00"`, `"25.00"`, `"1000.50"`.
  - Recusado: `""`, `"25"`, `"25.0"`, `"25.000"`, `"025.00"`, `"+25.00"`, `"-25.00"`, `"1e3"`, `"NaN"`, `"Infinity"`, `" 25.00"`, número JSON (`25.00` sem aspas).
  - Como só existe uma forma válida para cada valor, **não há normalização antes do hash**: a string aceita é a forma canônica. Isso elimina o risco de dois textos diferentes gerarem o mesmo efeito com hashes diferentes.
- **Moeda:** exatamente 3 letras maiúsculas e pertencente à lista suportada. `"brl"` é recusado (sem normalização silenciosa).
- **Limites:** o maior valor é `92233720368547758.07` (máximo de `int64` em centavos). Parsing que excede o limite é recusado; soma, subtração e negação verificam overflow e retornam erro (inclusive a negação do menor `int64`).
- **Valores negativos:** permitidos internamente (diferenças, reconciliação). A serialização de negativo é `"-5.00"`. O parser externo recusa sinal; existe um parser interno separado que aceita sinal, usado apenas para dados já confiáveis.
- **Operações:** `Parse`, `Zero(currency)`, `Add`, `Sub`, `Neg`, `Cmp`/`IsZero`/`IsPositive`, `String`, `MarshalJSON`/`UnmarshalJSON` (objeto `{"amount","currency"}`). Operações entre moedas diferentes retornam `ErrCurrencyMismatch`.
- **Valor zero de Go:** `Money{}` (sem moeda) é inválido; qualquer operação com ele retorna erro em vez de agir como zero.
- **Persistência:** colunas `*_amount BIGINT` (centavos) + `currency CHAR(3)`, com `CHECK` da lista de moedas.

## 4. Transação SQL e concorrência

### Delimitação

Um componente `UnitOfWork` abre a transação (`pgx.Tx`) e entrega aos repositórios uma interface que usa **a mesma transação**. Repositórios nunca abrem transações próprias. O caso de uso decide o início e o fim; os repositórios só executam SQL.

### Estratégia de concorrência: lock pessimista por carteira + verificação de versão

Para toda operação financeira, dentro da transação:

1. `SELECT ... FROM wallets WHERE id = $1 FOR UPDATE` — trava **somente aquela linha**. Carteiras diferentes não disputam lock (não há lock global).
2. O domínio calcula o novo saldo.
3. `UPDATE wallets SET balance_amount = $novo, version = version + 1, updated_at = now() WHERE id = $1 AND version = $versaoLida` — a condição de versão é uma segunda barreira: se, por um erro de código, o lock não tivesse sido obtido, a atualização afetaria 0 linhas e a operação falharia em vez de perder outra atualização.
4. `CHECK (balance_amount >= 0)` no banco impede saldo negativo mesmo com bug na aplicação.

**Por que pessimista:** a disputa típica (muitas apostas na mesma carteira) faria um controle otimista repetir muito. Com `FOR UPDATE` a segunda transação simplesmente espera a primeira terminar e então lê o saldo já atualizado. No teste das duas apostas de 80 sobre 100, a segunda vê saldo 20 e é rejeitada por saldo insuficiente.

**Ordem de locks (regra global):** sempre a carteira primeiro, depois linhas de transação (referência, pendência). Todos os caminhos — HTTP, SQS, worker de referências — seguem essa ordem, o que evita deadlocks entre eles. Uma operação nunca envolve duas carteiras.

**Conexões:** cada instância tem seu pool (`DB_MAX_CONNS`, definido na etapa 5). A soma dos pools das instâncias, mais as conexões de migrations e testes, precisa ficar abaixo de `max_connections` do PostgreSQL (padrão 100). Escalar instâncias exige ajustar os dois.

**Prazos:** `lock_timeout = 3s`, `statement_timeout = 5s`, prazo total da operação controlado pelo `context.Context` da requisição/mensagem. Erros `40P01` (deadlock), `40001` (serialização) e `55P03` (lock não obtido) são retentados até 3 vezes com espera aleatória curta; depois viram indisponibilidade transitória (HTTP 503 / mensagem volta para a fila).

**Nível de isolamento:** `READ COMMITTED` para operações (o lock de linha garante a serialização por carteira); uma única instrução SQL em READ COMMITTED para a reconciliação (um snapshot consistente da carteira e ledger).

## 5. Processamento de uma operação externa (BET, WIN, LOSS, REFUND, ROLLBACK)

HTTP e SQS chamam o **mesmo** caso de uso `ProcessWager`, com o mesmo objeto de comando. Fluxo:

1. **Validação de entrada** (formato, campos, `kind`, política de zero, `OPENING` proibido). Falha → erro corrigível, nada é persistido. HTTP e SQS usam decodificação estrita: recusam chaves repetidas, grafia diferente da do contrato e campos desconhecidos — o padrão do Go aceita os dois primeiros em silêncio (VULN-005). Os dois transportes montam um `RawExternalRequest` e chamam o mesmo `domain.NewExternalRequest`, que valida e calcula o hash.
2. **Consulta de idempotência sem lock** por `(providerId, idempotencyKey)`. Se existe → replay ou conflito (seção 6). Replays não tocam no lock da carteira.
3. Inicia transação SQL; trava a carteira (`FOR UPDATE`). Carteira inexistente → erro corrigível `WALLET_NOT_FOUND`, nada persistido.
4. Se há referência, lê a transação referenciada por `(providerId, referenceExternalTransactionId)` **sem lock próprio**: uma referência válida pertence à mesma carteira, que já está travada, e linhas terminais são imutáveis. Travar a linha de uma referência de *outra* carteira (caso de divergência) criaria esperas cruzadas entre carteiras sem nenhum benefício.
5. O domínio decide: **processar**, **rejeitar** ou **aguardar referência**.
6. Grava, nesta ordem: a transação (ON CONFLICT para a chave; conflito de ID externo também solicita rollback), o lançamento do ledger, a carteira com condição de versão anterior e os eventos da outbox. A inbox será integrada na etapa 8, na mesma transação.
7. Se o `INSERT` da transação não inseriu (outra requisição concorrente com a mesma chave ou o mesmo ID externo venceu), a transação SQL é desfeita inteira e o fluxo volta ao passo 2, que agora encontra o registro do vencedor.
8. `COMMIT`. Só depois disso a resposta é enviada e a mensagem SQS é removida.

**Interrupções e commit ambíguo:**

| Momento da falha | O que fica no banco | O que acontece depois |
| --- | --- | --- |
| antes do `COMMIT` | nada | cliente/fila repete; processa normalmente |
| conexão cai **durante** o `COMMIT` (não se sabe se confirmou) | tudo ou nada | HTTP responde 503; a repetição com a mesma chave vira replay (se confirmou) ou processamento (se não) |
| depois do `COMMIT`, antes da resposta HTTP | tudo | cliente repete com a mesma chave → replay com o resultado original |
| depois do `COMMIT`, antes do `DeleteMessage` | tudo, inclusive a inbox | mensagem reaparece → inbox reconhece → removida sem efeito |

Por isso nenhum caminho precisa saber se o commit "deu certo" para ser seguro: a repetição sempre resolve.

**Onde fica cada regra:** a decisão de negócio é uma função pura do domínio, `domain.Process(tx, wallet, referência, política, ids, agora)`. Ela recebe a carteira já travada e o que a aplicação encontrou sobre a referência (`ReferenceLookup`, inclusive se já existe reversão processada) e devolve o lançamento e os eventos a gravar. A aplicação só carrega, trava e persiste. Por isso as regras dos cinco tipos são testáveis sem banco, e HTTP, SQS e o worker de referências usam exatamente o mesmo código.

**Sem commit intermediário de `PENDING`:** operações sem dependência terminam no mesmo commit (`PROCESSED` ou `REJECTED`). `PENDING` existe apenas em memória dentro da transação. O único estado não terminal confirmado no banco é `PENDING_REFERENCE`, que tem retomada durável (seção 8). Consequência: interromper o processo antes do commit não deixa nada; depois do commit, tudo está completo.

## 6. Idempotência

**Persistida** na própria tabela `wager_transactions` (sobrevive a reinícios de todos os processos).

| Restrição no banco | Efeito |
| --- | --- |
| `UNIQUE (provider_id, idempotency_key)` | a chave tem escopo por provedor; um provedor não colide com outro |
| `UNIQUE (provider_id, external_transaction_id)` | a mesma operação não pode ser aplicada duas vezes, nem com chave diferente |

**Casos:**

| Situação | Resultado |
| --- | --- |
| Chave nova, ID externo novo | processa |
| Chave existente, mesmo hash | replay: devolve o resultado **persistido** (status, failureCode, saldo da época), `idempotentReplay: true` |
| Chave existente, hash diferente | `409 IDEMPOTENCY_KEY_REUSED`, sem efeito |
| Chave nova, ID externo já usado | `409 EXTERNAL_TRANSACTION_CONFLICT`, sem efeito (não reaplica com outra chave) |

O servidor usa a chave recebida como está; não a substitui por `{providerId}:{externalTransactionId}`.

**Hash do payload** — SHA-256 em hexadecimal minúsculo sobre o JSON canônico dos campos de negócio:

- Campos: `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money` (`amount`, `currency`) e `referenceExternalTransactionId` (omitido quando ausente).
- Excluídos: chave de idempotência, `messageId`, `type`, `occurredAt`, headers e qualquer metadado de transporte.
- Canonicalização: chaves em ordem lexicográfica (inclusive dentro de `money`), sem espaços, UTF-8, sem escape de HTML. Todos os valores são strings, então não há ambiguidade de números. Como os identificadores externos aceitam apenas `[A-Za-z0-9._:-]` (CONTRATOS §1), nenhum valor precisa de escape — o que elimina diferenças sutis entre bibliotecas de JSON. UUIDs são validados e escritos em minúsculas. O valor monetário já é canônico (seção 3).
- O hash é versionado (`FingerprintVersion = 1`); a versão é gravada junto (`RequestHashVersion`). Um hash de outra versão nunca é comparado diretamente: o domínio devolve erro em vez de declarar conflito ou replay falso.
- Pelo contrato, HTTP e SQS devem montar o mesmo comando e usar o mesmo código de hash. Equivalência HTTP/SQS comprovada nas etapas8/11: mesmo processWagerSession e hash financeiro; inbox acrescenta o hash do envelope.

**Concorrência entre duplicatas:** 50 cópias simultâneas da mesma aposta disputam o lock da carteira; a primeira grava e as outras repetem a consulta de idempotência depois de obter o lock, encontrando o vencedor e retornando replay. Corridas entre carteiras diferentes continuam protegidas pelos índices únicos; o perdedor desfaz a tentativa inteira e consulta de novo. Resultado: um débito, 49 replays.

## 7. Estados da operação

```
          ┌─────────────► PROCESSED   (terminal)
PENDING ──┼─────────────► REJECTED    (terminal)
          └──► PENDING_REFERENCE ──┬─► PROCESSED
                    ▲   │          ├─► REJECTED
                    └───┘ retry    └─► FAILED (terminal)
```

- Transições validadas no domínio (`Process`, `Reject(code)`, `AwaitReference`, `Fail(code)`); um estado terminal recusa qualquer transição.
- No banco, um trigger impede alterar campos financeiros e impede mudar uma linha que já está em estado terminal.
- **Falha transitória** (não muda estado; operação será repetida): conexão perdida, timeout, cancelamento, deadlock/serialização/lock (`40P01`, `40001`, `55P03`), `57P01` (servidor encerrando), classe `08` (conexão), `53300` (conexões esgotadas), erros de rede do SQS.
- **Falha permanente**: violações de constraint não mapeadas para um conflito de negócio conhecido, dados corrompidos, erro de SQL (bug). No caminho síncrono (HTTP/SQS) a transação é desfeita e nada é gravado (HTTP 500 / mensagem segue para a DLQ). No worker de referências, uma pendência que esbarra repetidamente em falha permanente é finalizada como `FAILED` com código `INTERNAL_PROCESSING_FAILED`, para auditoria e para não ficar pendente para sempre.
- Banco indisponível **nunca** vira rejeição financeira.

## 8. Referências e reversões

### Regras por tipo

| Tipo | Movimento | Referência |
| --- | --- | --- |
| `BET` | débito; exige saldo suficiente (`INSUFFICIENT_FUNDS`) | não aceita |
| `WIN` | crédito | opcional; se informada, deve ser `BET` `PROCESSED` da mesma rodada; valor livre; uma `BET` pode ter vários `WIN`; reverter a `BET` depois não afeta o `WIN` (cada um se desfaz com seu próprio `ROLLBACK`) |
| `LOSS` | nenhum (sem ledger, sem mudança de versão); valor `"0.00"` | não aceita |
| `REFUND` | crédito do valor da `BET` | obrigatória; `BET` `PROCESSED` |
| `ROLLBACK` | inverso do original | obrigatória; `BET`, `WIN` ou `REFUND` `PROCESSED` |

`OPENING` é recusado em qualquer transporte externo (`INVALID_KIND`).

A referência é resolvida por `(providerId do token/mensagem, referenceExternalTransactionId)` — portanto uma operação de outro provedor ou um `OPENING` (que não tem provedor) nunca é encontrado como referência. Ela precisa concordar em provedor, jogador, carteira, moeda e rodada; o valor da reversão precisa ser igual ao da referência (sem reversões parciais).

### Política de reversões combinadas

**Regra única: cada transação pode ter no máximo uma reversão bem-sucedida, seja `REFUND` ou `ROLLBACK`.** Isso é imposto pelo banco com um índice único parcial:

```sql
CREATE UNIQUE INDEX one_successful_reversal
  ON wager_transactions (reference_transaction_id)
  WHERE kind IN ('REFUND','ROLLBACK') AND status = 'PROCESSED';
```

Consequências sobre uma mesma `BET` de 50,00:

| Sequência | Resultado |
| --- | --- |
| `REFUND` | +50 |
| `REFUND`, depois `ROLLBACK(BET)` | o `ROLLBACK` é rejeitado (`REFERENCE_ALREADY_REVERSED`) — a aposta já foi devolvida |
| `ROLLBACK(BET)`, depois `REFUND` | o `REFUND` é rejeitado pelo mesmo motivo |
| `REFUND`, depois `ROLLBACK(REFUND)` | +50 −50: a devolução foi desfeita, a aposta volta a valer |
| `REFUND`, `ROLLBACK(REFUND)`, novo `REFUND` da mesma `BET` | rejeitado: a `BET` já usou sua única reversão; evita ciclos devolve/desfaz/devolve |
| `ROLLBACK(ROLLBACK)` | não permitido pelo enunciado (`REFERENCE_KIND_NOT_ALLOWED`) |

Assim o mesmo débito nunca é devolvido duas vezes, e a regra não depende de um `if` em Go: duas reversões concorrentes da mesma referência ficam serializadas pelo lock da carteira e, mesmo sem ele, a segunda esbarraria no índice.

**Reversão sem saldo:** `ROLLBACK` de `WIN` ou de `REFUND` debita; se o saldo não basta, a operação é `REJECTED` com `REVERSAL_INSUFFICIENT_FUNDS` (código diferente do `INSUFFICIENT_FUNDS` da aposta) e fica auditável na tabela e no evento de rejeição.

### Referência ainda indisponível

| Situação da referência | Comportamento |
| --- | --- |
| não existe | grava `PENDING_REFERENCE` + evento `WagerTransactionPendingReference`; retry |
| existe, mas está em `PENDING_REFERENCE` (ex.: `ROLLBACK` de um `REFUND` que espera a `BET`) | continua pendente; retry |
| existe, `REJECTED` ou `FAILED` | `REJECTED` com `REFERENCE_NOT_PROCESSED` (definitivo) |
| existe, `PROCESSED`, mas diverge (jogador, rodada, valor, tipo...) | `REJECTED` com o código específico; a referência encontrada fica registrada na rejeição, para auditoria |
| a aplicação entrega uma referência que não corresponde à busca | erro interno (bug), nada é gravado — nunca uma rejeição definitiva |

- **Retentativa:** backoff exponencial — 1s, 2s, 4s... limitado a 60s entre tentativas — com **TTL de 10 minutos** desde o recebimento (ambos configuráveis, com limites de sanidade: TTL até 24h e espera até 1h; os testes usam valores curtos). Expirado → `REJECTED` com `REFERENCE_NOT_FOUND` + evento `WagerTransactionRejected`.
- **Expiração só com o banco respondendo:** ao encontrar uma pendência vencida, o worker faz **uma última tentativa de resolução** antes de rejeitar. Se o banco ficou fora do ar por 15 minutos e a `BET` chegou nesse meio-tempo, a reversão é processada, não rejeitada. Falhas transitórias nunca contam como "referência não encontrada".
- **Aceleração futura (otimização opcional não implementada):** quando uma operação é processada, pode antecipar pendências que a referenciam. No desenho proposto elas têm `next_attempt_at` antecipado para agora, na mesma transação, pulando linhas travadas (`SKIP LOCKED`) para nunca esperar por outra carteira. Se for pulada, a pendência é resolvida no próximo ciclo normal.
- **Worker:** busca candidatas vencidas (`next_reference_attempt_at <= clock_timestamp()`); para cada uma abre uma transação, trava **primeiro a carteira** e depois a pendência, confere que ainda está pendente e resolve. Várias instâncias podem rodar o worker; a que chegar depois encontra a pendência já resolvida e pula. Como o estado está no banco, um reinício não perde nada.

## 9. Inbox, outbox e eventos

### Inbox (entrada SQS)

Tabela `inbox_messages` com chave primária `(consumer_name, message_id)`, hash da mensagem, `received_at` e `processed_at`. É gravada **na mesma transação** da operação. Reentrega da mesma mensagem: a inbox já existe → nada é reaplicado e a mensagem é removida. Mesmo `messageId` com conteúdo diferente → falha permanente (vai para a DLQ). Uma operação que fica em `PENDING_REFERENCE` conclui a inbox; o worker de referências assume a continuidade.

A inbox é uma segunda camada: mesmo que dois `messageId` diferentes carreguem a mesma operação, a idempotência da seção 6 impede o efeito duplicado.

### Outbox (saída)

Tabela `outbox_events`: `event_id` (estável, UUIDv7), agregado, tipo, versão, `payload` (snapshot completo do envelope, imutável por trigger), `occurred_at`, `attempts`, `next_attempt_at`, `published_at`, `claimed_by`, `claim_expires_at`, `last_error` (sanitizado).

Eventos são gravados na mesma transação da operação; **nada é publicado antes do commit** porque o publisher só lê linhas confirmadas.

**Publisher (várias instâncias em paralelo):**

1. *Claim*: em transação curta, reserva somente tantos eventos quanto vagas de envio imediato, usando FOR UPDATE SKIP LOCKED. claimed_by contém ProcessID e um token único da tentativa; claim_expires_at usa clock_timestamp() do PostgreSQL + 30s. Commit. Não reservar lote que aguardará serialmente em memória.
2. Publica no destino **fora** de qualquer transação e sem lock de carteira.
3. Confirma ou reagenda somente com event_id, token exato do claim e lease ainda válido; resultado atrasado de tentativa antiga não altera o novo claim.
4. Claim incrementa tentativas; falha no envio agenda `next_attempt_at` com backoff (limite 60s). Eventos **nunca** são descartados; o atraso aparece em métrica.

O envio individual ao SQS tem prazo de 10s e lease inicial de 30s. Antes de iniciar, verificar margem suficiente; se a espera local consumiu a margem, não iniciar com claim vencendo. Limitar claims às vagas disponíveis; renovação, se necessária, exige o mesmo token e posse ainda válida. Testar lote lento, expiração e confirmação atrasada; 10s < 30s sozinho não garante a duração de um lote.

**Relógio:** operações usam Session.Now, implementado com clock_timestamp() do PostgreSQL dentro da mesma transação SQL; na aposta é amostrado depois do lock. Leases, backoff e expiração de pendências também devem usar esse relógio, nunca o da instância. now() fica fixo no início da transação e não é usado para obter o instante após esperar um lock. Assim, instâncias com relógios ligeiramente diferentes não roubam trabalho umas das outras nem expiram pendências cedo demais.

Se uma instância morre depois do claim, o lease expira e outra assume. Se morre depois de publicar e antes de marcar, o evento é publicado de novo **com o mesmo `eventId`**. A entrega é at-least-once: consumidores devem deduplicar por `eventId`. Não prometemos "exatamente uma vez".

**Destino:** fila `wallet-events.fifo`. `MessageGroupId = walletId` (agrupamento por carteira e ordem de chegada ao broker; não promete ordem de commits entre publishers), `MessageDeduplicationId = eventId` (o broker descarta republicações dentro de 5 minutos, o que reduz mas não elimina duplicatas). Roteamento por eventType/version do envelope; não exige atributos adicionais de transporte. O contrato permite eventos fora da ordem financeira. Consumidores deduplicam eventId; projeções de saldo só avançam walletVersion e não sobrescrevem estado mais recente com saldo de evento antigo. Lacunas devem ser detectadas e tratadas por recuperação/reconciliação; auditoria conserva os eventos únicos, inclusive atrasados. Eventos de transação não têm sequência global comum; não presumir que PendingReference chegará antes de Processed. Testar explicitamente publisher antigo atrasado e evento novo enviado primeiro. A ordem dos eventos no JSON/UUIDv7 não substitui ordem de commit.

## 10. Banco de dados: proteções

- **Papéis:** `wallet_migrator` é dono do schema e roda as migrations (os papéis em si são criados por um script de inicialização do container do PostgreSQL, porque papéis existem no servidor inteiro e não pertencem a uma migration reversível); `wallet_app` é o usuário da aplicação, sem ser dono das tabelas, sem superusuário e sem `BYPASSRLS` — portanto não consegue desligar triggers nem alterar o schema.
- **Ledger append-only:** `REVOKE UPDATE, DELETE, TRUNCATE` de `wallet_app` **e** trigger que recusa `UPDATE`/`DELETE` (protege inclusive contra um papel com mais privilégio usado por engano).
- **Equação do lançamento:** `CHECK (amount > 0)` e `CHECK` de `balance_after = balance_before ± amount` conforme a direção.
- **Encadeamento:** cada lançamento guarda a versão da carteira resultante; `UNIQUE (wallet_id, wallet_version)` e `UNIQUE (wallet_id, transaction_id)`. Um trigger confere que o `balance_before` de um lançamento é igual ao `balance_after` do lançamento anterior.
- **Coerência saldo × ledger:** trigger de restrição adiado (verificado no commit) sobre `wallets`: toda mudança de saldo precisa de exatamente um lançamento com `wallet_version = NEW.version`, `balance_before = OLD.balance` e `balance_after = NEW.balance`; mudar a versão sem mudar o saldo é recusado. Um `UPDATE wallets SET balance = 999` direto falha.
- **Carteira:** `UNIQUE (player_id, currency)`, `CHECK (balance_amount >= 0)`, `CHECK (version >= 1)`.
- **Origem das transações:** coluna `origin` (`INTERNAL`/`EXTERNAL`) com `CHECK` que obriga os campos de provedor, chave, hash, rodada e jogo nas externas e os proíbe na interna; `OPENING` só com origem interna; `UNIQUE (wallet_id) WHERE kind = 'OPENING'` impede crédito inicial duplicado.
- **Valores e estados:** `CHECK` de listas de `kind`, `status`, `direction`, `currency`; `failure_code` obrigatório se e somente se o status for `REJECTED`/`FAILED`.

Implementado em `migrations/000001_initial_schema.up.sql` e validado na etapa 4 por testes que gravam SQL direto como `wallet_app` (e como o dono, para provar que a proteção não depende só de permissões). Detalhes:

- **Privilégios por coluna:** a aplicação só pode atualizar `balance_amount`, `version` e `updated_at` da carteira; o status, o resultado e a agenda de referência da transação; e os campos de publicação da outbox. Ledger e inbox: só `SELECT` e `INSERT`. Nenhum `EXECUTE` em funções.
- **Verificações no COMMIT** (triggers de restrição adiados): carteira igual ao último lançamento; operação PROCESSED com exatamente um lançamento (nenhum para LOSS), mesma moeda e jogador da carteira, e referência resolvida válida (mesma carteira e provedor, tipo permitido, mesmo valor nas reversões).
- **Verificações imediatas** (`CHECK`, `UNIQUE`, triggers `BEFORE`): equação e encadeamento do ledger, direção do lançamento conforme o tipo, imutabilidade, transições, políticas de valor e de referência.
- **CHECK e NULL:** regras que poderiam resultar em `NULL` usam `COALESCE(..., FALSE)`, porque no PostgreSQL um `CHECK` nulo é aprovado (VULN-008).
- **Erros:** violações das regras próprias usam SQLSTATE `23000` com o nome da regra em `CONSTRAINT`; a aplicação as trata como erro permanente (bug), nunca como rejeição de negócio.
- **Transação com moeda diferente da carteira:** só pode existir como rejeição (`CURRENCY_MISMATCH`); por isso o saldo de resultado tem moeda própria (`result_balance_currency`).

## 11. Abertura de carteira (`POST /wallets`)

- Cria a carteira com versão 1. `(playerId, currency)` duplicado → `409 WALLET_ALREADY_EXISTS`, com o `walletId` existente no corpo (a rota é só do serviço interno, então não há vazamento) para que um cliente que perdeu a resposta consiga continuar.
- Saldo inicial positivo: no mesmo commit, transação `OPENING` (`PROCESSED`, origem interna), lançamento de crédito com `balance_before = 0` e versão 1, e dois eventos na outbox (`WagerTransactionProcessed`, `WalletBalanceChanged`).
- Saldo inicial zero: só a carteira; sem `OPENING`, ledger ou eventos financeiros.
- Exceção à regra de coerência do item 10: a criação já nasce com o saldo do `OPENING`; o trigger aceita o `INSERT` da carteira se, no commit, existir o lançamento de abertura correspondente (ou o saldo for zero).

## 12. Autenticação e autorização

### Identidades (Keycloak, realm `jungle-gaming`, provisionado por arquivo de realm importado no start)

| Cliente | Fluxo | Papéis no cliente `wallet-api` | Claim `provider_id` |
| --- | --- | --- | --- |
| `wallet-internal` | `client_credentials` | `wallet:admin`, `wagering:read` | — |
| `provider-a` | `client_credentials` | `wagering:write`, `wagering:read` | `provider-a` |
| `provider-b` | `client_credentials` | `wagering:write`, `wagering:read` | `provider-b` |

- `wallet-api` é o cliente que representa a API (público-alvo, `aud`). Um mapper de audiência inclui `wallet-api` nos tokens; um mapper de claim fixa `provider_id` **na configuração do cliente no IdP** — nunca vem da requisição.
- **Validação do token:** assinatura RS256 com chaves do JWKS do Keycloak (cache com renovação ao encontrar `kid` desconhecido); `iss` exatamente igual ao esperado; `aud` contém `wallet-api`; `exp` validado pelo verificador OIDC, `nbf` sem tolerância adicional no adaptador e `iat` futuro limitado a 30s; algoritmo `none`/HS* recusados.
- **Emissor visto do host e dos containers (verificado na etapa 2):** `KC_HOSTNAME=http://localhost:18080` fixa o `iss`; `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true` mantém o JWKS acessível pelo nome interno `keycloak:8080`. Tokens pedidos pelo host e de dentro da rede têm o mesmo `iss` (teste `TestTokensForEachIdentity`). A aplicação terá configurações separadas para o emissor esperado (`OIDC_ISSUER`) e o endereço das chaves (`OIDC_JWKS_URL`). Nenhuma validação é desativada.
- **JWKS:** o Keycloak publica uma chave de assinatura (`use: sig`, RS256) e uma de criptografia (`use: enc`, RSA-OAEP). O validador aceita apenas chaves `sig` com o algoritmo esperado.
- **Identidades só de teste:** `provider-a-short-lived` (token de 5s, para testar expiração) e `no-roles` (token válido sem papéis, para testar 403). Ambas com nome e descrição que indicam o uso; em produção não existiriam.

### Permissões por rota

| Rota | `wallet-internal` | provedor |
| --- | --- | --- |
| `POST /wallets`, `GET /wallets/{id}`, `GET /wallets/{id}/ledger`, `POST /wallets/{id}/reconciliation` | sim | **403** |
| `POST /wagering/transactions` | **403** (operações de jogo pertencem aos provedores) | sim, se `providerId` do corpo = `provider_id` do token; senão `403 PROVIDER_MISMATCH`, sem efeito |
| `GET /wagering/transactions/{id}` | qualquer | só as próprias; de outro provedor ou `OPENING` → **404** (não revela existência) |
| `GET /providers/{providerId}/wagering/transactions/{extId}` | qualquer | só se `{providerId}` = token; senão **403** |
| `GET /health/live`, `GET /health/ready` | público | público |

**Interpretação: provedores e carteiras.** O enunciado não associa carteiras a provedores. Adotamos o modelo de agregador de cassino: a carteira é da plataforma, e qualquer provedor autenticado pode movimentar a carteira de qualquer jogador em seus jogos. O que o provedor **não** pode é ler ou reverter operações de outro provedor. Consequência de segurança: a operação precisa informar `walletId` **e** o `playerId` correto; se não baterem, a rejeição `WALLET_PLAYER_MISMATCH` **não devolve saldo**, para que um provedor não descubra o saldo de uma carteira alheia testando IDs. Uma associação jogador↔provedor (ex.: sessão de jogo) seria o próximo passo em produção e fica registrada como limitação.

Autorização acontece **antes** de qualquer consulta de idempotência, de modo que um replay também não revela dados de outro provedor: como a chave de idempotência tem escopo por provedor do token, o provedor B nunca encontra a operação do provedor A.

### Mensageria

O SQS não carrega identidade do provedor que o Go possa validar. Decisão:

- A fila `wager-transactions.fifo` é uma **fronteira interna**: só a identidade `wager-producer` (um gateway confiável que já autenticou o provedor) tem permissão de envio; só `wallet-service` pode receber, apagar e mudar visibilidade; a DLQ só recebe pela política de redrive. As políticas são aplicadas por identidade e recurso: wallet-service consome a entrada e publica na saída, sem as permissões inversas.
- O consumidor mantém todas as validações de domínio e recusa `providerId` fora da lista de provedores configurados.
- **Corrigido por Codex em 2026-10-02 (VULN-007):** a imagem local verifica assinatura SigV4, corpo, cabeçalhos e validade temporal antes de encaminhar ao MiniStack; bloqueia aliases root e rotas administrativas. O perfil suportado está em `deploy/sqs/strict/README.md`. Políticas por recurso são verificadas pelo broker; testes positivos e negativos usam SDK e HTTP reais. As identidades estão em `deploy/sqs/init.sh`, que preserva chaves ativas. Arquivo agregado de credenciais destina-se às ferramentas locais; aplicação recebe apenas wallet-service em volume readonly.

## 13. Consumidor SQS — implementado na etapa8

HTTP ProcessWager e ConsumeWager compartilham processWagerSession; só a aplicação controla a pgx.Tx. Inbox e efeitos financeiros/eventos confirmam juntos. Envelope/messageId/hash são duráveis, hash divergente é conflito. Uma colisão na inserção da inbox desfaz a tentativa financeira inteira antes de reler o vencedor. Não há I/O SQS dentro da transação SQL.

Execução: até CONSUMER_CONCURRENCY lanes (padrão4), cada uma recebe1 mensagem com long polling20s. Não usa lote que aguarda em memória. FIFO mantém ordem por carteira; grupos distintos avançam em paralelo. MessageGroupId deve ser walletId canônico; deduplicationId recomendado é messageId, apenas otimização. Payload64KiB estrito compartilhado com HTTP, provider allowlist e OPENING recusado.

Processamento10s, visibilidade30s, ACK3s, liberação2s, HTTP do SDK25s/uma tentativa para long poll. PostgreSQL é verificado antes de cada busca; indisponível pausa polling. Confirmada operação, rejeição, replay ou pendência: ACK. Transitória/ACK incerto: sem delete e retry2/4/8/16/32/60s. Permanente: visibilidade0 até redrive padrão10 para wager-transactions-dlq.fifo. Nunca descarta origem antes do tratamento durável. Não precisa heartbeat para operações abortadas em10s.

Stop interrompe polling e drena; deadline cancela processamento e libera receipts conhecidos, sem afirmar término antes do canal done. Receipts perdidos/desconhecidos por cancelamento do Receive reaparecem por expiração natural. Fx encerra HTTP→consumer→transport próprio→reference-worker→recursos. Role consumer sozinho não abre HTTP; startup ainda tem probes compartilhados de OIDC/SQS.

Hash da inbox inclui envelope normalizado e chave; hash financeiro exclui metadados/chave. CONSUMER_NAME deve ser estável entre instâncias/reinícios. wallet-service só recebe seu perfil em volume readonly, não credenciais administrativas. Produtor é gateway interno confiável, não um provedor arbitrário.

Provas e comandos: docs/ROTEIRO-DE-AVALIACAO.md, tests/integration/consumer*.go. Filas de integração temporárias isoladas com políticas adicionais por ARN e cleanup próprio, sem alterar permissões runtime padrão. Logs do consumidor usam IDs disponíveis e mensagens sanitizadas; métricas implementadas e testadas na etapa10.
## 14. Reconciliação

`POST /wallets/{id}/reconciliation` (só interno), em uma única instrução SQL com snapshot MVCC: lê o saldo armazenado, soma créditos menos débitos do ledger (soma em `NUMERIC` no SQL, sem overflow intermediário; o resultado é convertido de volta com verificação de limite) e conta os lançamentos. `difference = armazenado − reconstruído`. Divergência → `consistent: false` na resposta, log de nível `ERROR` e métrica wallet_events_total{event="reconciliation_divergence"}. **Nunca corrige o saldo.**

## 15. Uber Fx e ciclo de vida

### Onde o Fx entra

- **Só `internal/bootstrap` e `cmd` importam Fx** (imposto por `internal/archtest`). Domínio, casos de uso e adaptadores são construtores Go comuns (`func NewX(deps...) (*X, error)`), testáveis sem Fx.
- **RuntimeModule:** fx.Module("runtime") reúne fx.Provide dos construtores reais e fx.Invoke de startReferenceWorker/startConsumer/startPublisher/startServer. Hooks OnStart/OnStop ficam no bootstrap; os adaptadores expõem Start/Stop. Papéis são habilitados por configuração, com a validação comum de dependências preservada.
- **Configuração:** carregada e validada **antes** de montar o grafo e entregue com `fx.Supply`. Motivo: ela decide a própria composição — quais módulos entram (`APP_ROLES`) e os prazos de início e fim (`fx.StartTimeout`/`fx.StopTimeout`), que o Fx precisa conhecer antes de o grafo existir. Configuração inválida impede o processo de subir, com todos os problemas listados de uma vez.
- **Papéis por configuração:** as funções Invoke registram hooks apenas para papéis habilitados. NewInstance popula um Server em todas as combinações, mas o listener só abre com api; workers inativos não são iniciados. Configuração e validação da composição são testadas; recursos compartilhados sustentam startup/probes/diagnóstico.

### Início

- `OnStart` com prazo total `START_TIMEOUT` (padrão 30s; o padrão do Fx, 15s, é curto para um Keycloak recém-iniciado): banco faz `ping`; auth busca o JWKS; SQS resolve as URLs das filas; só então HTTP começa a aceitar conexões e os workers começam a buscar trabalho. Qualquer falha aborta o início e o processo sai com código 1 — nada fica meio iniciado.
- **Regra para workers:** o `ctx` recebido no `OnStart` vale só durante o início. Cada worker cria seu próprio contexto de longa duração, cancelado no `OnStop`. Usar o `ctx` do `OnStart` faria o worker morrer logo depois de subir — erro comum com Fx.
- Falha fatal do listener HTTP chama fx.Shutdowner com exit1. Erros de trabalho dos workers são registrados/reagendados; um panic inesperado em goroutine de worker encerra o processo pelo runtime Go. Não há recuperação genérica de panic dos workers que mantenha o processo vivo; as pendências confirmadas permitem retomada por outra instância.

### Encerramento

- **Ordem garantida pelo Fx:** os `OnStop` rodam na ordem inversa dos `OnStart`, e um componente só é construído depois das suas dependências. Logo, quem usa o pool do PostgreSQL para antes de o pool fechar — sem configuração manual de ordem.
- Sequência resultante: HTTP para de aceitar conexões e espera requisições em curso (http.Server.Shutdown); publisher drena/interrompe e fecha seu transporte; consumidor para de buscar, conclui as mensagens em andamento ou zera a visibilidade das restantes; workers terminam o ciclo atual; por fim pool e clientes são fechados.
- Cada worker tem um canal de término; o `OnStop` cancela o contexto e espera o canal até o prazo `SHUTDOWN_TIMEOUT` (padrão 20s). O término é observável e testado com `goleak` (nenhuma goroutine pode sobreviver ao `Stop`).
- **Prazo do orquestrador:** `SHUTDOWN_TIMEOUT` precisa ser menor que o tempo que o Docker espera antes do `SIGKILL` (`stop_grace_period`, padrão de apenas 10s). O serviço da aplicação no Compose usa `stop_grace_period: 30s`.

### Readiness × início

O Fx garante que o processo só conclui o início com as dependências respondendo; depois disso, `GET /health/ready` reflete o estado atual (banco ou SQS fora do ar → 503), e o consumidor pausa a busca enquanto o banco estiver indisponível (seção 13).

## 16. Observabilidade

- Logs JSON (`slog`) com `correlationId`, `messageId`, `transactionId`, `walletId`, `providerId` quando disponíveis. Nunca tokens, segredos ou payload financeiro completo.
- **Erros do banco:** as mensagens das proteções do schema contêm IDs e saldos. Elas nunca vão para a resposta HTTP (o cliente recebe `INTERNAL_ERROR` genérico) e, nos logs, registra-se o SQLSTATE e o nome da regra (`ConstraintName`), não a mensagem completa.
- `correlationId`: header `X-Correlation-Id` (validado) ou gerado; no SQS, o `messageId`. Propagado aos eventos.
- Métricas na mesma porta HTTP, GET /metrics restrito ao serviço interno: resultados por source/status, replays, retries, conflitos, backlog/idade da outbox, latência, divergências e profundidade aproximada da DLQ. Tentativas elegíveis ao redrive não representam chegada confirmada; esta é observada por atributos da DLQ. Sem IDs como labels. Semântica e nomes em internal/adapters/observability/diagnostics.go e no roteiro de avaliação.
- Health: `live` = processo responde; `ready` = `ping` no PostgreSQL e `GetQueueUrl` nas filas SQS efetivamente configuradas. `GetQueueAttributes` é usado nas métricas de profundidade da DLQ.

## 17. Estratégia de testes

| Nível | Como roda | O que prova |
| --- | --- | --- |
| Unitário | `go test ./...` | Money, domínio, estados, regras dos tipos, hash |
| Integração | build tag `integration`, contra PostgreSQL, Keycloak e SQS reais do Compose de testes | constraints, ledger, atomicidade, inbox/outbox, auth real |
| Múltiplas instâncias | build tags `integration,distributed`, três processos independentes da composição Fx | concorrência entre processos, recuperação |
| Falhas | pontos de falha injetáveis **apenas em binário de teste** (ex.: encerrar após commit e antes de `DeleteMessage`) | reentrega, retomada de pendências e outbox |
| Race | `go test -race` em container Linux com compilador C | ausência de condições de corrida no Go |

O ambiente de testes usa um projeto Compose separado (`docker compose --env-file tests/test.env ...`, projeto `jungle-gaming-test`, portas 25432/28080/28081) e a limpeza (`down -v`) apaga apenas os volumes desse projeto — verificado na etapa 2.

**Verificação única:** `docker compose run --rm check` executa `gofmt`, `go mod verify`, `go mod tidy -diff`, `go vet`, `go test -race` e `govulncheck` (vulnerabilidades conhecidas nas dependências). Todo o toolchain roda num container Linux fixado por digest, para que o resultado não dependa da máquina.

## 18. Segurança da cadeia de build

- Imagens base fixadas por digest (`golang` no build e no toolchain, `distroless/static:nonroot` no runtime). A tag documenta a versão; o digest garante que é exatamente a mesma imagem.
- Imagem final sem shell nem gerenciador de pacotes, executando como usuário sem privilégios; binário estático (`CGO_ENABLED=0`).
- `go.sum` versionado e verificado (`go mod verify`) no build e no `check`.
- `govulncheck` no `check`; dependências atualizadas quando ele aponta vulnerabilidade, mesmo que não alcançável.
- `.env` fora do Git e do contexto do Docker; `.env.example` contém apenas valores locais de exemplo.

## Limitações e trabalho não concluído

- Implementados: configuração, domínio, schema, repositórios pgx, UnitOfWork, composição real Fx, autenticação HTTP, API, consultas, inbox/consumidor, worker de referências, publisher, reconciliação e diagnóstico. Concorrência entre três processos e recuperação comprovadas; durabilidade sob morte abrupta do broker local adaptado comprovada nos cenários REV-01, com os limites abaixo. Throughput não medido. A [conferência integral do enunciado](docs/CONFORMIDADE.md) registra todos os itens e interpretações.
- **Emulador SQS (VULN-007):** autenticação corrigida na imagem local, no perfil restrito documentado. REV-01 corrigido na imagem local: handlers SQS/IAM persistem com fsync e substituição atômica antes da resposta, carregamento inválido impede startup. Testes SIGKILL preservam eventos aceitos, reentrega e Delete. Snapshots completos e filesystem/volume são limites explícitos; ver deploy/sqs/strict/README.md.
- **Ambiente local sem TLS:** Keycloak em `start-dev` (HTTP, banco H2 interno), PostgreSQL com `sslmode=disable`, SQS em HTTP — tudo restrito a `127.0.0.1` ou à rede interna do Compose. Em produção: Keycloak em modo `start` com TLS e banco próprio, PostgreSQL e AWS com TLS.
- **Credenciais locais:** valores de exemplo com padrão no `compose.yaml` (identificados como `local-example-*`); as chaves SQS são preservadas entre provisionamentos e o arquivo agregado é substituído atomicamente; montá-lo apenas nas ferramentas locais. Um único job de init por ambiente; rotação administrativa separada.
- **Crescimento de tabelas:** `inbox_messages` e `outbox_events` crescem indefinidamente. Em produção haveria limpeza periódica: eventos publicados há mais de N dias, e registros de inbox mais antigos que a retenção máxima da fila (14 dias no SQS) — antes disso, apagar a inbox abriria espaço para reprocessar uma reentrega tardia. Fora do escopo do desafio.
- **`FAILED` só no processamento assíncrono:** no caminho síncrono (HTTP/SQS) um erro permanente desfaz a transação e nada é gravado; o registro de auditoria é o log, a métrica e, no SQS, a mensagem na DLQ. Gravar `FAILED` nesse caminho travaria para sempre uma operação válida que falhou por um erro classificado errado ou por um bug corrigido depois.
- **Provedor × carteira:** não há vínculo jogador↔provedor (seção 12).
- **Moedas:** apenas moedas ISO com 2 casas decimais. Suportar `JPY` (0 casas) ou `BHD` (3 casas) exigiria escala por moeda no `Money` e no contrato.
- Opcionais fora do escopo inicial: ledger de partidas dobradas, tracing, dashboards, testes de carga.


## Precisões de implementação

A auditoria de erros permanentes só contabiliza FAILED depois do commit confirmado. O publisher registra separadamente messageId original e causationId. Money.UnmarshalJSON exige fim do conteúdo. Readiness verifica as filas efetivamente configuradas. A composição usa dependências explícitas de diagnóstico, fábrica SQS comum com perfil runtime e mapeamento de carteira que preserva SQLSTATE para retries.
