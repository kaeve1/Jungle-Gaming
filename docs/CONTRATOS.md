# Contratos

Contratos externos da solução: HTTP, mensagem SQS de entrada, eventos de saída e códigos de falha. As decisões que os justificam estão em [ARCHITECTURE.md](../ARCHITECTURE.md).

**Estado:** HTTP, consumidor/inbox, publisher e reconciliação implementados, com provas distribuídas e revisão final concluídas. A [conferência integral do enunciado](VALIDACAO-FINAL.md) cruza contratos, código e evidências atuais. Mudanças de comportamento exigem atualizar os testes correspondentes.

## 1. Convenções gerais

- JSON UTF-8. Campos desconhecidos são recusados (`400`). Corpo máximo: 64 KiB (413 acima do limite).
- Dinheiro sempre como `{"amount":"25.00","currency":"BRL"}` — `amount` é string com exatamente duas casas; número JSON é recusado.
- Instantes de saída em UTC, RFC 3339 com milissegundos (`2026-09-08T12:00:00.000Z`). `occurredAt` da entrada SQS aceita RFC 3339 com precisão até nanossegundos e offset; o hash da inbox normaliza esse instante para UTC com RFC3339Nano.
- IDs internos (`walletId`, `transactionId`, `eventId`) e `playerId`: UUID em minúsculas.
- Identificadores externos (`providerId`, `externalTransactionId`, `roundId`, `gameId`, `referenceExternalTransactionId`): 1 a 128 caracteres de `[A-Za-z0-9._:-]`.
- `Idempotency-Key`: 1 a 255 caracteres ASCII visíveis (`0x21`–`0x7E`).
- Autenticação: `Authorization: Bearer <token>` em todas as rotas, exceto health checks.
- Correlação: header opcional `X-Correlation-Id` (1–128 caracteres de `[A-Za-z0-9._:-]`); se ausente, é gerado. Sempre devolvido na resposta.

### Corpo de erro (tudo que não é resultado de operação)

```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "money.amount must have exactly two decimal places"
  }
}
```

`message` é texto para humanos e pode mudar; clientes devem usar `code`. Erros genéricos não incluem detalhes por campo. `details` é opcional e aparece no conflito de carteira existente com seu `walletId`.

## 2. Rotas

### `POST /wallets` — interno

Requisição:

```json
{
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "initialBalance": { "amount": "1000.00", "currency": "BRL" }
}
```

| Status | Quando | Corpo |
| --- | --- | --- |
| `201` | criada | carteira (abaixo) |
| `400` | entrada inválida (`VALIDATION_ERROR`, `UNSUPPORTED_CURRENCY`) | erro |
| `401` / `403` | token ausente/inválido/expirado; identidade sem `wallet:admin` | erro |
| `409` | já existe carteira para `(playerId, currency)` — `WALLET_ALREADY_EXISTS` | erro com `"walletId"` existente em `details` |
| `503` | dependência indisponível — `SERVICE_UNAVAILABLE`, header `Retry-After` | erro |

Carteira:

```json
{
  "id": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "balance": { "amount": "1000.00", "currency": "BRL" },
  "version": 1,
  "createdAt": "2026-09-08T12:00:00.000Z",
  "updatedAt": "2026-09-08T12:00:00.000Z"
}
```

### `GET /wallets/{walletId}` — interno

`200` carteira; `404 WALLET_NOT_FOUND`.

### `GET /wallets/{walletId}/ledger?cursor=...&limit=50` — interno

- Ordenação estável por `walletVersion` crescente (cada lançamento tem versão única por carteira).
- `limit`: padrão 50, mínimo 1, máximo 200; fora disso `400`.
- `cursor`: opaco (base64url). Cursor inválido ou de outra carteira → `400 INVALID_CURSOR`.

```json
{
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "entries": [
    {
      "id": "0192f291-27de-7aaa-8000-000000000001",
      "transactionId": "0192f291-27de-7aaa-8000-000000000000",
      "direction": "CREDIT",
      "money": { "amount": "1000.00", "currency": "BRL" },
      "balanceBefore": { "amount": "0.00", "currency": "BRL" },
      "balanceAfter": { "amount": "1000.00", "currency": "BRL" },
      "walletVersion": 1,
      "createdAt": "2026-09-08T12:00:00.000Z"
    }
  ],
  "nextCursor": null
}
```

### `POST /wallets/{walletId}/reconciliation` — interno

Estado atual: autorização e cálculo implementados; interno recebe200 com snapshot consistente. Divergência é reportada sem alterar saldo.

```json
{
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "storedBalance": { "amount": "975.00", "currency": "BRL" },
  "calculatedBalance": { "amount": "975.00", "currency": "BRL" },
  "difference": { "amount": "0.00", "currency": "BRL" },
  "consistent": true,
  "checkedEntries": 2
}
```

`200` sempre que a verificação roda (inclusive com divergência: `consistent: false`). `404` carteira inexistente.

### `POST /wagering/transactions` — provedor

Headers: `Idempotency-Key` obrigatório.

```json
{
  "providerId": "provider-a",
  "externalTransactionId": "transaction-123",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "roundId": "round-987",
  "gameId": "fortune-chimp",
  "kind": "BET",
  "money": { "amount": "25.00", "currency": "BRL" }
}
```

`referenceExternalTransactionId`: obrigatório em `REFUND`/`ROLLBACK`, opcional em `WIN`, proibido em `BET`/`LOSS`.

**Resultado de operação** (usado em `201`, `200`, `202`, `422` e no `500` de `FAILED`):

```json
{
  "transactionId": "0192f298-345e-7e38-af88-e43f851a819d",
  "status": "PROCESSED",
  "balance": { "amount": "975.00", "currency": "BRL" },
  "failureCode": null,
  "idempotentReplay": false
}
```

`balance` é o saldo observado quando a operação terminou (persistido; em replay **não** é o saldo atual). É `null` em `PENDING_REFERENCE`, em `FAILED` e na rejeição `WALLET_PLAYER_MISMATCH` (para não revelar o saldo de uma carteira que não é do jogador informado).

| Status HTTP | Situação | `code` / `status` |
| --- | --- | --- |
| `201` | processada agora | `PROCESSED`, `idempotentReplay: false` |
| `200` | replay de operação processada | `PROCESSED`, `idempotentReplay: true` |
| `202` | aguardando referência (novo ou replay) | `PENDING_REFERENCE` |
| `422` | rejeição de negócio definitiva (novo ou replay) | `REJECTED` + `failureCode` |
| `500` | operação terminou em `FAILED` (consulta/replay) | `FAILED` + `failureCode` |
| `400` | entrada inválida, nada persistido | erro `VALIDATION_ERROR`, `MISSING_IDEMPOTENCY_KEY`, `INVALID_KIND`, `INVALID_AMOUNT`, `UNSUPPORTED_CURRENCY` |
| `401` | token ausente, inválido ou expirado | erro `UNAUTHENTICATED` |
| `403` | identidade sem permissão; `providerId` diferente do token | erro `FORBIDDEN`, `PROVIDER_MISMATCH` |
| `404` | carteira inexistente, nada persistido | erro `WALLET_NOT_FOUND` |
| `409` | chave reutilizada com outro conteúdo; ID externo já usado com outra chave | erro `IDEMPOTENCY_KEY_REUSED`, `EXTERNAL_TRANSACTION_CONFLICT` |
| `503` | indisponibilidade transitória; commit pode ser ambíguo, repetir com a mesma chave | erro `SERVICE_UNAVAILABLE` + `Retry-After` |
| `500` | erro inesperado; resposta não confirma ao cliente o resultado da tentativa | erro `INTERNAL_ERROR`; consultar a operação ou repetir com a mesma chave |

### `GET /wagering/transactions/{transactionId}` e `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}`

`200` com o detalhe; `404 TRANSACTION_NOT_FOUND` quando não existe **ou** pertence a outro provedor. Rota com `{providerId}` diferente do token de provedor → `403 PROVIDER_MISMATCH`.

```json
{
  "transactionId": "0192f298-345e-7e38-af88-e43f851a819d",
  "origin": "EXTERNAL",
  "providerId": "provider-a",
  "externalTransactionId": "transaction-123",
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "roundId": "round-987",
  "gameId": "fortune-chimp",
  "kind": "BET",
  "money": { "amount": "25.00", "currency": "BRL" },
  "referenceExternalTransactionId": null,
  "referenceTransactionId": null,
  "status": "PROCESSED",
  "failureCode": null,
  "resultBalance": { "amount": "975.00", "currency": "BRL" },
  "referenceAttempts": 0,
  "nextReferenceAttemptAt": null,
  "createdAt": "2026-09-08T12:00:00.000Z",
  "updatedAt": "2026-09-08T12:00:00.000Z"
}
```

Em operações internas (`OPENING`, visíveis só para o serviço interno) os campos externos são `null`. `referenceTransactionId` é preenchido quando a referência foi encontrada — inclusive em rejeições como `REFERENCE_ALREADY_REVERSED`, para mostrar qual transação motivou a recusa.

### Health checks — públicos

- `GET /health/live` → `200 {"status":"ok"}` enquanto o processo responde.
- `GET /health/ready` → `200` com `{"status":"ok","checks":{"postgres":"ok","sqs":"ok"}}`, ou `503` com o componente que falhou.

## 3. Mensagem SQS de entrada (`wager-transactions.fifo`)

```json
{
  "messageId": "msg-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-08T12:00:00.000Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "transaction-123",
    "idempotencyKey": "provider-a:transaction-123",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" }
  }
}
```

- `messageId`: identidade durável na inbox (1–128 caracteres de `[A-Za-z0-9._:-]`). `type` deve ser `WagerTransactionRequested`.
- `data` segue as mesmas regras do corpo HTTP; `data.idempotencyKey` faz o papel do header.
- Atributos de envio: `MessageGroupId = data.walletId`; `MessageDeduplicationId = messageId`.
- Hash da inbox: SHA-256 do JSON canônico, com chaves ordenadas, de `{messageId, type, occurredAt, data, idempotencyKey}`. `data` é o objeto financeiro canônico usado pelo domínio, sem a chave; `idempotencyKey` contém a chave recebida em `data.idempotencyKey`. `occurredAt` é normalizado para UTC/RFC3339Nano. Esse objeto interno de cálculo não altera o envelope recebido. O hash financeiro é distinto: exclui a chave e todos os metadados de transporte.
- Tratamento: ver ARCHITECTURE.md, seção 13. Mensagens inválidas vão para `wager-transactions-dlq.fifo` via redrive.

## 4. Eventos de saída (`wallet-events.fifo`)

### Envelope

```json
{
  "eventId": "0192f298-3460-7000-8000-000000000001",
  "eventType": "WalletBalanceChanged",
  "aggregateId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "correlationId": "msg-123",
  "causationId": "msg-123",
  "occurredAt": "2026-09-08T12:00:00.123Z",
  "version": 1,
  "data": {}
}
```

- `eventType` e `version` são definidos pelo construtor de cada evento, nunca pelo chamador.
- `correlationId`: correlação da requisição/mensagem que originou a operação (mantida pelo worker de referências).
- `causationId`: `messageId` do SQS, `null` em HTTP; nos eventos de resolução de pendência, o `eventId` do `WagerTransactionPendingReference`.
- Atributos SQS: `MessageGroupId = walletId`, `MessageDeduplicationId = eventId`. eventType e version estão no envelope JSON; não são enviados como MessageAttributes SQS.
- Entrega at-least-once: o consumidor deduplica por `eventId`. Ordem por carteira é preferencial, não garantida entre publishers; use `walletVersion` para ordenar mudanças de saldo.

### `WagerTransactionProcessed` (v1) — aggregate: transação

```json
{
  "transactionId": "...", "origin": "EXTERNAL", "kind": "BET",
  "walletId": "...", "playerId": "...",
  "providerId": "provider-a", "externalTransactionId": "transaction-123",
  "roundId": "round-987", "gameId": "fortune-chimp",
  "referenceTransactionId": null,
  "money": { "amount": "25.00", "currency": "BRL" },
  "balanceAfter": { "amount": "975.00", "currency": "BRL" },
  "processedAt": "2026-09-08T12:00:00.123Z"
}
```

Inclui `LOSS` e `OPENING` (campos externos `null` no `OPENING`).

### `WagerTransactionRejected` (v1) — aggregate: transação

Mesmos campos de identificação + `failureCode`, `rejectedAt`; sem `balanceAfter`.

### `WagerTransactionPendingReference` (v1) — aggregate: transação

Campos de identificação + `referenceExternalTransactionId`, `expiresAt`.

### `WalletBalanceChanged` (v1) — aggregate: carteira

```json
{
  "walletId": "...", "transactionId": "...",
  "direction": "DEBIT",
  "money": { "amount": "25.00", "currency": "BRL" },
  "balanceBefore": { "amount": "1000.00", "currency": "BRL" },
  "balanceAfter": { "amount": "975.00", "currency": "BRL" },
  "walletVersion": 2
}
```

### Eventos por resultado

| Resultado | Eventos |
| --- | --- |
| `BET`/`WIN`/`REFUND`/`ROLLBACK` processados | `WagerTransactionProcessed` + `WalletBalanceChanged` |
| `LOSS` processado | `WagerTransactionProcessed` |
| Abertura com saldo > 0 | `WagerTransactionProcessed` + `WalletBalanceChanged` |
| Abertura com saldo 0 | nenhum |
| Rejeição de negócio | `WagerTransactionRejected` |
| Aguardando referência | `WagerTransactionPendingReference` (depois, um dos anteriores) |
| `FAILED` | nenhum evento de negócio (registro de auditoria e métrica) |

## 5. Códigos de falha

**Corrigível** = nada foi persistido; o cliente pode corrigir e reenviar (com a mesma chave). **Definitivo** = persistido; replays devolvem o mesmo resultado.

### Entrada (HTTP 4xx, não persistidos)

| Código | Classe | Significado |
| --- | --- | --- |
| `VALIDATION_ERROR` | corrigível | campo ausente, formato ou tamanho inválido, campo desconhecido |
| `MISSING_IDEMPOTENCY_KEY` | corrigível | header ausente ou vazio |
| `INVALID_KIND` | corrigível | tipo desconhecido ou `OPENING` |
| `INVALID_AMOUNT` | corrigível | formato monetário inválido, zero onde não é permitido, `LOSS` diferente de `"0.00"` |
| `UNSUPPORTED_CURRENCY` | corrigível | moeda fora da lista |
| `WALLET_NOT_FOUND` | corrigível | carteira não existe |
| `PROVIDER_MISMATCH` | corrigível | `providerId` diferente da identidade autenticada |
| `IDEMPOTENCY_KEY_REUSED` | conflito | chave já usada com outro conteúdo |
| `EXTERNAL_TRANSACTION_CONFLICT` | conflito | operação já registrada com outra chave |
| `WALLET_ALREADY_EXISTS` | conflito | carteira já existe para jogador e moeda |
| `INVALID_CURSOR` | corrigível | cursor de paginação inválido |

### Rejeições de negócio (status `REJECTED`, HTTP 422, persistidas, evento `WagerTransactionRejected`)

| Código | Quando |
| --- | --- |
| `INSUFFICIENT_FUNDS` | `BET` maior que o saldo |
| `REVERSAL_INSUFFICIENT_FUNDS` | `ROLLBACK` de `WIN`/`REFUND` maior que o saldo |
| `WALLET_PLAYER_MISMATCH` | carteira não pertence ao `playerId` |
| `CURRENCY_MISMATCH` | moeda da operação diferente da carteira |
| `BALANCE_LIMIT_EXCEEDED` | crédito excederia o limite representável |
| `REFERENCE_NOT_FOUND` | referência não chegou dentro do TTL (confirmado por uma última tentativa com o banco respondendo) |
| `REFERENCE_NOT_PROCESSED` | referência terminou `REJECTED` ou `FAILED` |
| `REFERENCE_KIND_NOT_ALLOWED` | tipo referenciado não permitido (ex.: `REFUND` de `WIN`, `ROLLBACK` de `ROLLBACK`, `WIN` referenciando algo que não é `BET`) |
| `REFERENCE_MISMATCH` | referência diverge em jogador, carteira, moeda ou rodada |
| `REFERENCE_AMOUNT_MISMATCH` | valor da reversão diferente do referenciado |
| `REFERENCE_ALREADY_REVERSED` | a referência já tem uma reversão bem-sucedida |

### Falha permanente (status `FAILED`)

| Código | Quando |
| --- | --- |
| `INTERNAL_PROCESSING_FAILED` | pendência que esbarrou repetidamente em erro permanente não relacionado a regra de negócio |

### Erros de infraestrutura e autenticação

Esses corpos de erro não representam uma operação terminal persistida. Um resultado de commit incerto ou falha após o commit não permite concluir que a operação deixou de ser aplicada; consulte seu estado ou repita com a mesma chave. `FAILED` persistido tem o corpo de resultado de operação descrito acima.

| Código | HTTP | Significado |
| --- | --- | --- |
| `SERVICE_UNAVAILABLE` | 503 | banco/fila indisponível ou timeout; repetir com a mesma chave é seguro |
| `INTERNAL_ERROR` | 500 | erro inesperado; resultado não confirmado ao cliente, consultar ou repetir com a mesma chave |
| `UNAUTHENTICATED` | 401 | token ausente, inválido ou expirado |
| `FORBIDDEN` | 403 | identidade sem a permissão da rota |




## Detalhes de persistência e diagnóstico

Eventos de saída são at-least-once e podem chegar fora da ordem dos commits financeiros entre publishers. MessageGroupId=walletId agrupa e preserva ordem de chegada ao SQS; não impõe ordem de origem no PostgreSQL. Consumidores deduplicam eventId. Projeções de saldo usam walletVersion para evitar sobrescrever saldo mais recente com evento antigo; lacunas exigem recuperação/reconciliação. Auditoria deve guardar cada evento único, incluindo atrasados. Eventos de transação não prometem uma sequência global de entrega. Implementação e testes em tests/integration/outbox_test.go, incluindo atraso controlado do primeiro envio e inversão de walletVersion.

## Retomada de referências
Worker implementado: polling SQL com carteira antes da pendência, relógio PostgreSQL, backoff/TTL persistido; configuração REFERENCE_TTL/BASE_DELAY/MAX_DELAY. Três falhas permanentes confirmadas e auditadas após rollback finalizam FAILED/INTERNAL_PROCESSING_FAILED por transição do domínio, sem evento de negócio, conforme tabela acima. Contador durável separado das tentativas de referência ausente; transitórias não contam. Replays/consultas mantêm autorização e resultado persistido. Publicação dos eventos implementada na etapa9.


## Execução da entrada SQS

ConsumeWager e ProcessWager chamam o mesmo processWagerSession. Inbox confirma com resultado/saldo/ledger/outbox, por consumerName/messageId e hash de envelope normalizado incluindo data.idempotencyKey. Hash financeiro continua sem chave/metadados. Correlação/causação da mensagem usam o messageId do envelope; o ID do broker não determina a inbox. Mesmo envelope com hash diferente vai à DLQ, sem novo efeito. Pendência/rejeição confirmadas permitem ACK; falha transitória ou commit/ACK incerto não.

Consumer recebe uma mensagem por lane (padrão4), long polling20s, prazo de processamento10s/visibilidade30s, ACK3s/liberação2s. Grupo deve corresponder a walletId canônico; deduplicationId recomendado é messageId. Backoff2–60s; permanente libera0; redrive padrão10. Stop drena ou cancela/libera conhecidos; receipt desconhecido recupera por expiração natural. Provider allowlist não autentica produtor: wager-producer pertence a gateway interno confiável. Detalhes, provas e limites no roteiro de avaliação e no relatório final.


GET /metrics: token interno e wallet:admin, Prometheus text; sem IDs em labels. Fonte, cardinalidade e limites das métricas em ARCHITECTURE.md. Publicação outbox mantém eventId/payload, token por tentativa e lease DB30s; retry2–60s sem descarte. Complete/Retry expirados não alteram reservas novas.
