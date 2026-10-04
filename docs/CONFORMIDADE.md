# Conformidade integral com o enunciado

Referência: [enunciado original](DESAFIO-ORIGINAL.md). 215 verificações, agrupadas nas 15 seções. Resultados de execução em [VALIDACAO-FINAL.md](VALIDACAO-FINAL.md). Condicionais e opcionais são identificados explicitamente. Uma matriz não substitui as provas executadas.

## Cláusulas

### 1. Objetivo

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 1-3-01 | Serviço em Go composto com Uber Fx | Atendido no escopo verificado | C, F | fx |
| 1-3-02 | API HTTP e consumidor com efeitos financeiros equivalentes; mesmo caso de uso | Atendido no escopo verificado | H, Q, S | consumer |
| 1-3-03 | Resultado correto entre instâncias independentes e falhas | Atendido no escopo verificado | U, D, O, R | distributed |
| 1-3-04 | Precisão, ledger, idempotência, concorrência e recuperação avaliáveis por evidências | Atendido no escopo verificado | M, L, D, S | execution |
### 2. Autenticação e autorização

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 2-11-01 | IdP externo OAuth/OIDC efetivo; Keycloak no Compose | Atendido no escopo verificado | A, C | auth |
| 2-11-02 | client_credentials; escolha, validação e permissões justificadas; sem cadastro/emissão própria | Atendido no escopo verificado | A, DOC | auth |
| 2-11-03 | Token determina providerId; body/path divergentes recusados | Atendido no escopo verificado | A, H | auth |
| 2-11-04 | Provedor consulta apenas suas transações, também por ID externo e em replay | Atendido no escopo verificado | H, S | auth |
| 2-11-05 | Operações de carteira e métricas restritas à identidade interna | Atendido no escopo verificado | H, A | auth |
| 2-11-06 | Mensageria controlada por SigV4 e políticas do broker; consumidor valida domínio | Atendido no escopo verificado | B, Q, P | broker |
### 3. Ambiente e falhas

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 3-21-01 | Operação externa exige jogador, carteira, jogo, provedor e rodada; cinco tipos externos | Atendido no escopo verificado | P, T | rules |
| 3-21-02 | Entrega at-least-once e repetição HTTP/SQS sem duplicação financeira | Atendido no escopo verificado | S, Q, D | consumer, distributed |
| 3-21-03 | Reversão anterior à referência persiste e retoma | Atendido no escopo verificado | R, P | reference, refRecovery |
| 3-21-04 | Operações simultâneas da mesma carteira conservam saldo | Atendido no escopo verificado | U, D | concurrency, distributed |
| 3-21-05 | Morte antes/depois do commit não aplica efeito parcial ou duplicado | Atendido no escopo verificado | S, U, Q | atomic, distributed |
| 3-21-06 | Republicação preserva eventId e snapshot | Atendido no escopo verificado | O, E | outbox, distributed |
| 3-21-07 | Indisponibilidade temporária de PostgreSQL e SQS permite recuperação | Atendido no escopo verificado | U, Q, O | distributed |
| 3-21-08 | Saldo não negativo e evento confirmado preservado nos cenários especificados, incluindo crash do broker adaptado | Atendido no escopo verificado | D, B, O | external |
### 4. Stack e lifecycle

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 4-36-01 | Go 1.27.1 coincide em go.mod, Dockerfile e toolchain | Atendido no escopo verificado | C | execution |
| 4-36-02 | Go Modules com go.mod/go.sum presentes e verificados; imagens por digest | Atendido no escopo verificado | C | execution |
| 4-36-03 | Uber Fx efetivo na aplicação, não apenas importado | Atendido no escopo verificado | F | fx |
| 4-36-04 | HTTP net/http; IdP externo; PostgreSQL; SQS local MiniStack; Docker Compose | Atendido no escopo verificado | C, H, A, B | execution |
| 4-36-05 | Migrations numeradas up/down com comandos documentados | Atendido no escopo verificado | D, DOC | migrations |
| 4-36-06 | testing/go test e execução com -race | Atendido no escopo verificado | DOC, C | execution |
| 4-36-07 | pgx/v5 com SQL, locks, constraints e fronteira transacional explícitos; sqlc opcional ausente | Atendido no escopo verificado | U, D, DOC | static |
| 4-36-08 | Money mapeado BIGINT mais moeda e uma pgx.Tx compartilhada entre repositórios | Atendido no escopo verificado | U, D, M | atomic |
| 4-36-09 | Configuração, conexões, repositórios, casos de uso, handlers e workers injetados por construtores | Atendido no escopo verificado | F | fx |
| 4-36-10 | fx.Module/Provide/Invoke/Lifecycle; configuração validada antes do grafo e fx.Supply justificado | Atendido no escopo verificado | F, DOC | fx |
| 4-36-11 | Startup valida configuração e dependências antes de aceitar trabalho | Atendido no escopo verificado | F, A, V | fx, execution |
| 4-36-12 | Workers têm contexto próprio, deadlines e canal observável de término | Atendido no escopo verificado | F, Q, O, R | fx |
| 4-36-13 | Shutdown para novas entradas, drena ou cancela/libera trabalho e só depois fecha dependências | Atendido no escopo verificado | F, H, Q, O | fx, execution |
| 4-36-14 | Domínio independente de Fx, HTTP, SQS e persistência | Atendido no escopo verificado | M, W, T, P, L, E | domain |
### 5. Oito garantias obrigatórias

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 5-72-01 | 1: Nenhum float em parsing, cálculo, JSON, hash ou persistência monetária | Atendido no escopo verificado | M, P, D | money, static |
| 5-72-02 | 2: Idempotência persistente sobrevive ao reinício dos processos | Atendido no escopo verificado | D, S | distributed, external |
| 5-72-03 | 3: Invariantes no banco independem de mutex e dedup FIFO | Atendido no escopo verificado | D, U | schema, concurrency |
| 5-72-04 | 4: Eventos publicados somente após commit financeiro | Atendido no escopo verificado | O, S | outbox, distributed |
| 5-72-05 | 5: Ledger append-only; reversão é lançamento novo | Atendido no escopo verificado | L, D, P | schema, reference |
| 5-72-06 | 6: Coordenação por carteira, sem lock global; carteiras independentes avançam | Atendido no escopo verificado | U, R | concurrency |
| 5-72-07 | 7: Versionamento e SQL impedem lost updates | Atendido no escopo verificado | W, D, U | concurrency |
| 5-72-08 | 8: Unicidade, não negatividade e imutabilidade impostas pelo schema | Atendido no escopo verificado | D | schema, concurrency |
### 6. Encapsulamento e erros

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 6-83-01 | Entidades encapsuladas; construtores e transições públicas preservam invariantes | Atendido no escopo verificado | W, T, L, P | domain, wallet |
| 6-83-02 | Criação e reidratação separadas, sem reaplicar dinheiro nem emitir eventos na leitura | Atendido no escopo verificado | W, T, L | domain, wallet |
| 6-83-03 | Valores não inicializados ou inválidos rejeitados | Atendido no escopo verificado | M, W, T, P | money, domain |
| 6-83-04 | Erros classificáveis por errors.Is/As; rejeição de negócio sem panic | Atendido no escopo verificado | P, T, U | rules, static |
| 6-83-05 | I/O com context.Context, cancelamento e limites de tempo | Atendido no escopo verificado | U, H, Q, O, R, A | fx, concurrency |
### 6.1. Money

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 6-95-01 | Value object com estado privado, valor e moeda; operações retornam novos valores | Atendido no escopo verificado | M | money |
| 6-95-02 | Criação decimal, zero por moeda, soma, subtração, negação, comparação e JSON | Atendido no escopo verificado | M | money |
| 6-95-03 | int64 em centavos; limites de representação documentados | Atendido no escopo verificado | M, DOC | money |
| 6-95-04 | amount sempre string decimal e currency ISO; escala fixa duas casas | Atendido no escopo verificado | M, H, Q | money, http |
| 6-95-05 | Vazio, NaN, Infinity, científico, escala extra e entrada financeira negativa recusados | Atendido no escopo verificado | M, P | money, http, external |
| 6-95-06 | Sem arredondamento; formato monetário único e normalização documentada antes do hash | Atendido no escopo verificado | M, P, DOC | rules |
| 6-95-07 | Soma, subtração e comparação recusam moedas diferentes | Atendido no escopo verificado | M | money |
| 6-95-08 | Overflow tratado no parsing, soma, subtração e negação, inclusive MinInt64 | Atendido no escopo verificado | M | money |
| 6-95-09 | Diferenças internas podem ser negativas; carteira nunca negativa | Atendido no escopo verificado | M, W, X | money, reconcile |
| 6-95-10 | Persistência preserva centavos BIGINT e moeda; reconciliação usa soma NUMERIC exata | Atendido no escopo verificado | D, U, X | reconcile |
| 6-95-11 | BRL nos fluxos principais; incompatibilidade de moedas testada | Atendido no escopo verificado | M, P | money, rules |
| 6-95-12 | Método UnmarshalJSON direto exige EOF; JSON estrito nos transportes | Atendido no escopo verificado | M, H, Q | money, http |
### 6.2. Wallet

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 6-112-01 | Raiz do agregado contém identidade, jogador, moeda, saldo, versão e timestamps | Atendido no escopo verificado | W, D | wallet, schema |
| 6-112-02 | Criação/reidratação/débito/crédito explícitos; transação SQL controla persistência | Atendido no escopo verificado | W, S, U | wallet, atomic |
| 6-112-03 | Uma carteira por playerId/currency | Atendido no escopo verificado | D, S | schema, opening |
| 6-112-04 | Débito preserva zero ou positivo; moeda da movimentação coincide | Atendido no escopo verificado | W, P, D | wallet, rules |
| 6-112-05 | Toda alteração de saldo exige ledger no mesmo commit | Atendido no escopo verificado | D, S, U | atomic, schema |
| 6-112-06 | Versão inicial 1; aumento exatamente 1 apenas em mudança financeira | Atendido no escopo verificado | W, D, G | wallet, opening, schema |
| 6-112-07 | Concorrência preserva atualização confirmada; estratégia documentada | Atendido no escopo verificado | U, D, DOC | concurrency, distributed |
### 6.3. WagerTransaction

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 6-127-01 | OPENING mais os cinco tipos externos; origem interna/externa diferenciada | Atendido no escopo verificado | T, P, D | rules, opening |
| 6-127-02 | Identificadores, provedor, chave, hash, carteira, jogador, rodada, jogo, tipo, Money e referência externa persistidos | Atendido no escopo verificado | T, D, U | schema, rules |
| 6-127-03 | Referência interna resolvida, failureCode e resultado financeiro persistidos quando aplicáveis | Atendido no escopo verificado | T, D, R | reference, http |
| 6-127-04 | Criação começa PENDING; transições validadas pelo domínio | Atendido no escopo verificado | T | domain |
| 6-127-05 | Cinco estados implementados, incluindo PENDING_REFERENCE e FAILED terminal | Atendido no escopo verificado | T, R | domain, refRecovery |
| 6-127-06 | Terminais imutáveis; replay recupera resultado original | Atendido no escopo verificado | T, D, S | schema, distributed |
| 6-127-07 | Transitório versus permanente e máquina de estados documentados | Atendido no escopo verificado | U, R, DOC | refRecovery |
| 6-127-08 | PENDING sem dependência concluído sincronicamente, sem commit intermediário de aceite | Atendido no escopo verificado | S, U, DOC | atomic, distributed |
| 6-127-09 | Retomada do PENDING assíncrono: condicional não aplicável; pendência real é PENDING_REFERENCE durável | Atendido no escopo verificado | S, R, DOC | refRecovery |
| 6-127-10 | OPENING recusado por HTTP e SQS | Atendido no escopo verificado | P, H, Q | rules, http, consumer |
| 6-127-11 | OPENING tem identidade estável, carteira, jogador, moeda, valor, estado e timestamps | Atendido no escopo verificado | G, T, D | opening, schema |
| 6-127-12 | Metadados externos inaplicáveis ausentes no OPENING; schema impede crédito inicial duplicado | Atendido no escopo verificado | D, G | schema, opening |
### 6.4. WalletLedgerEntry

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 6-151-01 | Todos os campos exigidos, direção e instantes; walletVersion adicional | Atendido no escopo verificado | L, D | schema |
| 6-151-02 | Construtor valida equação balanceAfter=balanceBefore±money, sem overflow | Atendido no escopo verificado | L, M | domain, money |
| 6-151-03 | Estado imutável; unicidade walletId/transactionId e walletId/version | Atendido no escopo verificado | D, L | schema |
| 6-151-04 | Banco bloqueia edição, exclusão e truncate pelo papel da aplicação | Atendido no escopo verificado | D | schema |
| 6-151-05 | LOSS e rejeições não produzem ledger; correções são novos lançamentos | Atendido no escopo verificado | P, D | rules, schema |
| 6-151-06 | Partidas dobradas opcionais ausentes | Opcional ausente | DOC | static |
### 6.5. Inbox e outbox

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 6-159-01 | Inbox contém consumidor, messageId, hash, recebimento, conclusão e unicidade composta | Atendido no escopo verificado | D, Q | schema, consumer |
| 6-159-02 | Outbox contém eventId estável, agregado, tipo, payload, ocorrência, tentativas, próximo envio e publicação | Atendido no escopo verificado | D, O | schema, outbox |
| 6-159-03 | Retry com backoff e dados duráveis de reserva/publicação | Atendido no escopo verificado | O, D | outbox |
| 6-159-04 | Inbox, conclusão e efeitos de domínio/ledger/eventos no mesmo commit | Atendido no escopo verificado | S, Q, U | consumer, atomic |
| 6-159-05 | PENDING_REFERENCE confirmado permite ACK; worker durável continua tratamento | Atendido no escopo verificado | S, Q, R | consumer, refRecovery |
### 7. Operações e referências

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 7-168-01 | BET debita valor positivo com saldo suficiente | Atendido no escopo verificado | P, W | rules, distributed |
| 7-168-02 | WIN credita positivo e permite BET da mesma rodada como referência opcional | Atendido no escopo verificado | P | rules, reference |
| 7-168-03 | LOSS exige 0.00, moeda correta, sem ledger nem incremento da versão | Atendido no escopo verificado | P, W | rules, consumer |
| 7-168-04 | REFUND devolve integralmente BET processada | Atendido no escopo verificado | P | rules, reference |
| 7-168-05 | ROLLBACK faz movimento inverso de BET, WIN ou REFUND processada | Atendido no escopo verificado | P | rules, reference |
| 7-168-06 | REFUND/ROLLBACK exigem referência e lookup providerId/externalTransactionId | Atendido no escopo verificado | P, R, U | reference |
| 7-168-07 | Referência concorda em provedor, jogador, carteira, moeda e rodada | Atendido no escopo verificado | P, D | reference |
| 7-168-08 | Reversão parcial proibida; valor idêntico ao referenciado | Atendido no escopo verificado | P, D | reference |
| 7-168-09 | Reversão duplicada do mesmo tipo impedida também no banco | Atendido no escopo verificado | D, P | concurrency, reference |
| 7-168-10 | REFUND e ROLLBACK diretos combinados sobre BET não devolvem débito duas vezes; política documentada | Atendido no escopo verificado | P, D, DOC | reference |
| 7-168-11 | Rollback de crédito sem saldo rejeitado e auditado com código distinto de BET insuficiente | Atendido no escopo verificado | P, T | reference, external |
| 7-168-12 | Referência ausente persiste PENDING_REFERENCE | Atendido no escopo verificado | P, R, D | reference |
| 7-168-13 | Worker retomável após reinício, com backoff exponencial e prazo persistido | Atendido no escopo verificado | R, D | refRecovery |
| 7-168-14 | TTL padrão 10min; atrasos 1/2/4 até 60s configuráveis; expiração vira REJECTED/REFERENCE_NOT_FOUND com evento | Atendido no escopo verificado | R, P, DOC | reference, refRecovery |
| 7-168-15 | Referência existente pendente aguarda; REJECTED/FAILED produz REFERENCE_NOT_PROCESSED | Atendido no escopo verificado | P, R | reference, refRecovery |
| 7-168-16 | Última consulta de expiração requer banco respondendo; transitório não finaliza operação | Atendido no escopo verificado | R, U | refRecovery |
| 7-168-17 | failureCode estável e distinção de entrada corrigível, conflito e resultado definitivo documentados | Atendido no escopo verificado | P, T, DOC | rules, http |
### 8. Concorrência

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 8-196-01 | Lock pessimista por carteira mais verificação de versão e retries SQL limitados, justificados | Atendido no escopo verificado | U, D, DOC | concurrency |
| 8-196-02 | Pelo menos três processos com memória e conexões independentes | Atendido no escopo verificado | F, U | distributed, external |
| 8-196-03 | 100.00 com duas BET 80.00: uma processada, outra INSUFFICIENT_FUNDS, saldo 20.00 e único débito | Atendido no escopo verificado | S, U, D | distributed, external |
| 8-196-04 | Reenvios não alteram resultado original nem saldo | Atendido no escopo verificado | S, D | distributed, external |
| 8-196-05 | Carteiras diferentes continuam simultaneamente, inclusive quando uma está bloqueada | Atendido no escopo verificado | U, R | concurrency |
### 9. Contratos HTTP

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 9-206-01 | POST /wallets aceita playerId e initialBalance no formato do exemplo | Atendido no escopo verificado | H, G, S | opening, http, external |
| 9-206-02 | Abertura positiva: carteira versão1, OPENING PROCESSED, crédito e dois eventos no mesmo commit | Atendido no escopo verificado | G, S, D, E | opening, atomic |
| 9-206-03 | Abertura zero: só carteira versão1, sem OPENING/ledger/eventos financeiros | Atendido no escopo verificado | G, S | opening |
| 9-206-04 | Reabertura mesmo jogador/moeda retorna conflito | Atendido no escopo verificado | S, H, D | opening, http |
| 9-206-05 | GET carteira e GET ledger existentes e internos | Atendido no escopo verificado | H | auth, http |
| 9-206-06 | Ledger com cursor opaco, ordem estável, limit e validação de cursor por carteira | Atendido no escopo verificado | H | http |
| 9-206-07 | GET transação por ID interno e por provider/ID externo, com estado pendente e códigos | Atendido no escopo verificado | H | auth, reference |
| 9-206-08 | POST wagering implementa campos, Money e resposta exemplificados | Atendido no escopo verificado | H, S, P | http, external |
| 9-206-09 | referenceExternalTransactionId obrigatório nas reversões | Atendido no escopo verificado | P, H | rules, http |
| 9-206-10 | Idempotency-Key obrigatório, recebido preservado; não substituído por chave calculada | Atendido no escopo verificado | H, P, S | http, rules |
| 9-206-11 | Hash financeiro SHA256 de JSON com chaves ordenadas, campos de negócio e normalizações documentados | Atendido no escopo verificado | P, DOC | rules |
| 9-206-12 | Hash financeiro exclui chave e metadados de transporte; equivalente HTTP/SQS | Atendido no escopo verificado | P, Q, S | consumer, distributed |
| 9-206-13 | Mesma chave/conteúdo retorna replay persistido com idempotentReplay=true | Atendido no escopo verificado | S, H | distributed, external |
| 9-206-14 | Mesma chave/outro conteúdo retorna conflito | Atendido no escopo verificado | P, S, H | rules, http |
| 9-206-15 | Mesmo provider/ID externo com outra chave não reaplica | Atendido no escopo verificado | D, S, H | concurrency, distributed |
| 9-206-16 | Replay concluído devolve saldo original após outras operações | Atendido no escopo verificado | T, S, H | distributed, external |
| 9-206-17 | Códigos e corpos distinguem entrada inválida, conflito, REJECTED, PENDING_REFERENCE e indisponibilidade | Atendido no escopo verificado | H, DOC | http, reference |
| 9-206-18 | POST reconciliation retorna todos os seis campos do exemplo; difference=stored-calculated | Atendido no escopo verificado | X, H | reconcile, external |
| 9-206-19 | Reconciliação inclui OPENING e usa visão consistente MVCC; não modifica saldo | Atendido no escopo verificado | X | reconcile |
| 9-206-20 | Divergência aparece na resposta, log e métrica | Atendido no escopo verificado | H, V | reconcile |
| 9-206-21 | Health live e ready públicos; ready consulta PostgreSQL e filas SQS efetivamente configuradas | Atendido no escopo verificado | V, H, F | distributed, external |
### 10. Consumidor SQS

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 10-323-01 | Filas wager-transactions.fifo e wager-transactions-dlq.fifo provisionadas com redrive | Atendido no escopo verificado | B, C | broker |
| 10-323-02 | Envelope messageId/type/occurredAt/data e todos os campos do exemplo | Atendido no escopo verificado | Q | consumer |
| 10-323-03 | Mesmo caso de uso HTTP/SQS; data.idempotencyKey financeira mais dedup inbox | Atendido no escopo verificado | S, Q | consumer, distributed |
| 10-323-04 | messageId do envelope, não do broker, identifica inbox; hash verificado na reentrega | Atendido no escopo verificado | Q, S, D | consumer |
| 10-323-05 | DeleteMessage somente depois de commit durável | Atendido no escopo verificado | Q, S | consumer, distributed |
| 10-323-06 | Rejeição de negócio confirmada é terminal e permite ACK | Atendido no escopo verificado | Q, S | consumer |
| 10-323-07 | Transitório usa retry/backoff; permanente ou tentativas esgotadas chegam à DLQ | Atendido no escopo verificado | Q, B | consumer |
| 10-323-08 | Limites documentados: redrive10, visibility30s, processamento10s, ACK3s, polling20s, backoff2–60s | Atendido no escopo verificado | Q, DOC | consumer |
| 10-323-09 | Mensagem inválida é recusada, sem efeito financeiro, e redriven | Atendido no escopo verificado | Q, P | consumer |
| 10-323-10 | SIGTERM para polling, drena ou cancela/libera visibilidade; receipt desconhecido expira naturalmente | Atendido no escopo verificado | Q, F | fx |
| 10-323-11 | MessageGroupId=walletId canônico e MessageDeduplicationId=messageId documentados | Atendido no escopo verificado | Q, DOC | consumer |
| 10-323-12 | Concorrência HTTP/SQS para mesma operação com recebimentos repetidos reais | Atendido no escopo verificado | S, Q, D | consumer, distributed |
### 11. Transactional outbox e eventos

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 11-359-01 | Estado, saldo, ledger, inbox e eventos atômicos conforme aplicável | Atendido no escopo verificado | S, U, D | atomic, consumer |
| 11-359-02 | Worker publisher separado do processamento financeiro | Atendido no escopo verificado | O, F | outbox |
| 11-359-03 | Múltiplos publishers disputam registros por claim/lease; backoff e retomada abandonada | Atendido no escopo verificado | O, D | outbox, distributed |
| 11-359-04 | Interrupção commit→publish recuperada por outra instância | Atendido no escopo verificado | O | distributed |
| 11-359-05 | Interrupção publish→confirmação recuperada com mesmo eventId, aceitando duplicidade externa | Atendido no escopo verificado | O, E | distributed |
| 11-359-06 | Destino wallet-events.fifo provisionado; roteamento e consumo documentados | Atendido no escopo verificado | B, DOC | broker, outbox |
| 11-359-07 | WagerTransactionProcessed concreto para sucesso, incluindo LOSS e OPENING positivo | Atendido no escopo verificado | E, P, G | events, opening |
| 11-359-08 | WagerTransactionRejected concreto para rejeição definitiva | Atendido no escopo verificado | E, P | events, reference |
| 11-359-09 | WalletBalanceChanged concreto apenas para mudança efetiva de saldo | Atendido no escopo verificado | E, P, G | events, rules |
| 11-359-10 | WagerTransactionPendingReference concreto na espera | Atendido no escopo verificado | E, P | events, reference |
| 11-359-11 | Envelope tem eventId/eventType/aggregateId/correlationId/causationId opcional/occurredAt/version/data tipado | Atendido no escopo verificado | E | events |
| 11-359-12 | WalletBalanceChanged tem walletId/transactionId/direction/money/balanceBefore/balanceAfter/walletVersion | Atendido no escopo verificado | E | events |
| 11-359-13 | Tipo/versão fixados no construtor; timestamps UTC RFC3339; Money string decimal | Atendido no escopo verificado | E, M | events, money |
| 11-359-14 | Payload imutável no domínio e banco; publisher não consulta saldo atual para recompor evento | Atendido no escopo verificado | E, D, O | events, schema, outbox |
| 11-359-15 | Ordem entre publishers não promete ordem de commit; consumidores deduplicam eventId e usam walletVersion | Atendido no escopo verificado | O, DOC | outbox |
### 12. Observabilidade

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 12-384-01 | Logs JSON rastreiam correlationId/messageId/transactionId/walletId/providerId quando disponíveis | Atendido no escopo verificado | V, F, S, O, R | observability, external |
| 12-384-02 | Logs não expõem tokens, segredos, payload financeiro completo ou mensagem SQL contendo saldo | Atendido no escopo verificado | V, U, H | observability |
| 12-384-03 | Métricas de resultados por status e source, inclusive FAILED só após commit de auditoria | Atendido no escopo verificado | V, R | observability |
| 12-384-04 | Métricas de duplicatas, retries e conflitos de concorrência | Atendido no escopo verificado | V | observability, reconcile |
| 12-384-05 | DLQ aproximada, backlog/atraso outbox, latência e divergências de reconciliação | Atendido no escopo verificado | V | reconcile |
| 12-384-06 | Health conforme API; métricas internas autenticadas e labels sem IDs | Atendido no escopo verificado | H, V | auth, reconcile |
| 12-384-07 | OpenTelemetry e dashboards são opcionais e não implementados | Opcional ausente | DOC | static |
### 13. Verificação obrigatória

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 13-392-01 | Unitários Money: parsing/arithmetic/escala/limites/inválidos/incompatibilidade | Atendido no escopo verificado | M | money |
| 13-392-02 | Unitários carteira, transições e cinco tipos externos | Atendido no escopo verificado | W, T, P | wallet, domain, rules |
| 13-392-03 | Unitários conflito de payload por chave e zero por tipo | Atendido no escopo verificado | P | rules |
| 13-392-04 | Unitários OPENING interno, metadados e eventos | Atendido no escopo verificado | G, E | opening, events |
| 13-392-05 | Integração usa PostgreSQL/Keycloak/MiniStack em containers reais; infraestrutura não substituída por mocks | Atendido no escopo verificado | C | execution |
| 13-392-06 | Integração migrations/constraints/imutabilidade/atomicidade | Atendido no escopo verificado | D, U | migrations, schema, atomic |
| 13-392-07 | Integração inbox/redelivery/outbox concorrente/retry/DLQ/reinício | Atendido no escopo verificado | Q, O, R | consumer, outbox, distributed |
| 13-392-08 | Composição Fx com startup, shutdown e liberação observável de recursos | Atendido no escopo verificado | F, H, Q, O, R | fx, execution |
| 13-392-09 | IdP real rejeita ausência, invalidez e expiração de credenciais | Atendido no escopo verificado | A, H | auth, external |
| 13-392-10 | Isolamento entre provedores inclusive consultas/replays e operações internas | Atendido no escopo verificado | A, H, S | auth, external |
| 13-392-11 | Acesso não autorizado não muda finanças nem revela dados protegidos | Atendido no escopo verificado | H, S | auth, external |
| 13-392-12 | Cenário1: 50 envios paralelos da mesma BET resultam em único débito | Atendido no escopo verificado | S, D | distributed, external |
| 13-392-13 | Cenário2: duas BET 80.00 sobre 100.00 com resultado obrigatório | Atendido no escopo verificado | U, D | distributed, external |
| 13-392-14 | Cenário3: carteiras diferentes processadas simultaneamente | Atendido no escopo verificado | U | concurrency, distributed |
| 13-392-15 | Cenário4: cenários relevantes repetidos em três processos independentes | Atendido no escopo verificado | F, U | distributed, external |
| 13-392-16 | Cenário5: consumidor morto pós-commit/pré-DeleteMessage; reentrega comprovada | Atendido no escopo verificado | Q, S | distributed |
| 13-392-17 | Cenário6: dois publishers na mesma outbox e recuperação da publicação | Atendido no escopo verificado | O | outbox, distributed |
| 13-392-18 | Cenário7: reversão antes da referência, resolução posterior e expiração | Atendido no escopo verificado | R | reference, refRecovery |
| 13-392-19 | Cenário8: reinício preserva idempotência, pendências e consistência; PENDING assíncrono não usado | Atendido no escopo verificado | S, R, D | distributed, refRecovery |
| 13-392-20 | Saldo final conferido com créditos menos débitos incluindo abertura | Atendido no escopo verificado | X, D | reconcile, external |
| 13-392-21 | HTTP/SQS cruzados e dedup da aplicação, não só FIFO; recebimentos repetidos comprovados | Atendido no escopo verificado | S, Q | consumer, distributed |
| 13-392-22 | go test -race nos unitários e integration,distributed; execução atual registrada no relatório final | Atendido no escopo verificado | C, DOC | execution |
### 14. Avaliação e eliminatórios

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 14-427-01 | Integridade financeira (20), concorrência (20), idempotência (15) têm evidências rastreáveis | Atendido no escopo verificado | M, D, U, S | execution |
| 14-427-02 | Mensageria (15), arquitetura (10), testes (10), observabilidade (5), documentação (5) têm evidências | Atendido no escopo verificado | Q, O, F, V, DOC | execution |
| 14-427-03 | Nota de 100 pontos pertence ao avaliador; não é resultado de teste automatizado | Avaliação externa | DOC | static |
| 14-427-04 | E1: autenticação efetiva nos endpoints de negócio | Atendido no escopo verificado | A, H | auth |
| 14-427-05 | E2: sem acesso não autorizado a operações/transações nos testes | Atendido no escopo verificado | A, H, S | auth, external |
| 14-427-06 | E3: nenhuma aritmética monetária com float | Atendido no escopo verificado | M, P, D, X | money, static |
| 14-427-07 | E4: sem saldo negativo por concorrência nos cenários executados | Atendido no escopo verificado | D, U | concurrency, distributed |
| 14-427-08 | E5: sem movimentação duplicada nos cenários executados | Atendido no escopo verificado | D, S | consumer, distributed |
| 14-427-09 | E6: idempotência persistida, não limitada à memória | Atendido no escopo verificado | D, S | distributed, external |
| 14-427-10 | E7: correção financeira funciona com três instâncias independentes | Atendido no escopo verificado | U, D, F | distributed, external |
| 14-427-11 | E8: publicação somente pós-commit | Atendido no escopo verificado | O, S | outbox, distributed |
| 14-427-12 | E9: ledger auditável e protegido | Atendido no escopo verificado | D, L | schema, reconcile |
| 14-427-13 | E10: PostgreSQL/SQS/IdP reais nos testes | Atendido no escopo verificado | C | execution |
| 14-427-14 | Partidas dobradas, tracing e carga opcionais ausentes; não há meta mínima de RPS | Opcional ausente | DOC | static |
| 14-427-15 | Metodologia/throughput/p50/p95/p99/erros/conflitos/outbox em teste de carga: condicional sem aplicação, pois não se apresenta benchmark | Condicional não aplicável | DOC | static |
### 15. Material e reprodução

| ID | Cláusula | Estado | Fontes | Provas |
| --- | --- | --- | --- | --- |
| 15-445-01 | Código, migrations e Docker Compose presentes; reprodução limpa com fontes e novos volumes | Atendido no escopo verificado | C, D, DOC | execution |
| 15-445-02 | README inclui pré-requisitos e variáveis de ambiente | Atendido no escopo verificado | DOC, C | static |
| 15-445-03 | README inclui inicialização de filas, migrations up/down e execução da aplicação | Atendido no escopo verificado | DOC, B, D | migrations, execution |
| 15-445-04 | README inclui chamadas autenticadas e comandos de teste | Atendido no escopo verificado | DOC, A | auth, execution |
| 15-445-05 | .env.example contém valores locais identificados, sem segredos reais de produção | Atendido no escopo verificado | C, DOC | static |
| 15-445-06 | IdP provisionado automaticamente com identidades de teste e fluxo client_credentials documentado | Atendido no escopo verificado | A, C, DOC | auth, execution |
| 15-445-07 | ARCHITECTURE explica Money/transações/idempotência/locks/referências/reversões/inbox/outbox | Atendido no escopo verificado | DOC | static |
| 15-445-08 | ARCHITECTURE explica autenticação/autorização/Fx/shutdown/limitações/interpretações e opcionais ausentes | Atendido no escopo verificado | DOC | static |
| 15-445-09 | docker compose up --build ou equivalente documentado e ensaiado | Atendido no escopo verificado | C, DOC | execution |
| 15-445-10 | go test ./..., go test -race ./... e go vet ./... ou equivalentes Docker documentados e executados | Atendido no escopo verificado | DOC, C | execution |
| 15-445-11 | Dependências de testes, integração, múltiplas instâncias e simulações de falha documentadas separadamente | Atendido no escopo verificado | DOC | static |
| 15-445-12 | Build tags integration e integration,distributed com comandos explícitos | Atendido no escopo verificado | DOC, C | execution |
| 15-445-13 | gofmt, módulos verificados/tidy e builds por imagens fixadas; dependências reproduzíveis | Atendido no escopo verificado | C, DOC | execution |
| 15-445-14 | Material local de entrega preparado; publicação e envio serão feitos pelo candidato | Preparação autorizada; sem publicação ou envio automático | DOC | static |

## Fontes

- **M**: [internal/domain/money/money.go](../internal/domain/money/money.go)
- **W**: [internal/domain/wallet.go](../internal/domain/wallet.go)
- **T**: [internal/domain/transaction.go](../internal/domain/transaction.go)
- **P**: [internal/domain/processing.go](../internal/domain/processing.go)
- **P**: [internal/domain/request.go](../internal/domain/request.go)
- **L**: [internal/domain/ledger.go](../internal/domain/ledger.go)
- **E**: [internal/domain/events.go](../internal/domain/events.go)
- **G**: [internal/domain/opening.go](../internal/domain/opening.go)
- **S**: [internal/application/service.go](../internal/application/service.go)
- **S**: [internal/application/inbox.go](../internal/application/inbox.go)
- **D**: [migrations/000001_initial_schema.up.sql](../migrations/000001_initial_schema.up.sql)
- **D**: [migrations/000002_review_integrity.up.sql](../migrations/000002_review_integrity.up.sql)
- **D**: [migrations/000003_reference_failures.up.sql](../migrations/000003_reference_failures.up.sql)
- **U**: [internal/adapters/postgres/uow.go](../internal/adapters/postgres/uow.go)
- **U**: [internal/adapters/postgres/repositories.go](../internal/adapters/postgres/repositories.go)
- **R**: [internal/application/references.go](../internal/application/references.go)
- **R**: [internal/adapters/postgres/references.go](../internal/adapters/postgres/references.go)
- **R**: [internal/adapters/referenceworker/worker.go](../internal/adapters/referenceworker/worker.go)
- **O**: [internal/application/outbox.go](../internal/application/outbox.go)
- **O**: [internal/adapters/postgres/outbox.go](../internal/adapters/postgres/outbox.go)
- **O**: [internal/adapters/outboxpublisher/worker.go](../internal/adapters/outboxpublisher/worker.go)
- **Q**: [internal/adapters/sqsconsumer/message.go](../internal/adapters/sqsconsumer/message.go)
- **Q**: [internal/adapters/sqsconsumer/worker.go](../internal/adapters/sqsconsumer/worker.go)
- **Q**: [internal/adapters/postgres/inbox.go](../internal/adapters/postgres/inbox.go)
- **A**: [internal/adapters/auth/verifier.go](../internal/adapters/auth/verifier.go)
- **A**: [deploy/keycloak/realm-jungle-gaming.json](../deploy/keycloak/realm-jungle-gaming.json)
- **H**: [internal/adapters/http/handler.go](../internal/adapters/http/handler.go)
- **H**: [internal/adapters/http/server.go](../internal/adapters/http/server.go)
- **H**: [internal/adapters/postgres/reader.go](../internal/adapters/postgres/reader.go)
- **F**: [internal/bootstrap/bootstrap.go](../internal/bootstrap/bootstrap.go)
- **F**: [internal/bootstrap/runtime.go](../internal/bootstrap/runtime.go)
- **F**: [internal/config/config.go](../internal/config/config.go)
- **X**: [internal/adapters/postgres/reconciliation.go](../internal/adapters/postgres/reconciliation.go)
- **V**: [internal/adapters/observability/diagnostics.go](../internal/adapters/observability/diagnostics.go)
- **V**: [internal/adapters/health/health.go](../internal/adapters/health/health.go)
- **B**: [deploy/sqs/init.sh](../deploy/sqs/init.sh)
- **B**: [deploy/sqs/strict/strict_auth.py](../deploy/sqs/strict/strict_auth.py)
- **B**: [deploy/sqs/strict/durable_state.py](../deploy/sqs/strict/durable_state.py)
- **B**: [deploy/sqs/strict/README.md](../deploy/sqs/strict/README.md)
- **C**: [compose.yaml](../compose.yaml)
- **C**: [Dockerfile](../Dockerfile)
- **C**: [go.mod](../go.mod)
- **C**: [go.sum](../go.sum)
- **C**: [.env.example](../.env.example)
- **DOC**: [README.md](../README.md)
- **DOC**: [ARCHITECTURE.md](../ARCHITECTURE.md)
- **DOC**: [docs/CONTRATOS.md](../docs/CONTRATOS.md)
- **DOC**: [docs/TESTES.md](../docs/TESTES.md)
- **DOC**: [scripts/validate-clean.ps1](../scripts/validate-clean.ps1)

## Grupos de testes

### money

- `TestParseValid` em [internal/domain/money/money_test.go](../internal/domain/money/money_test.go)
- `TestParseInvalid` em [internal/domain/money/money_test.go](../internal/domain/money/money_test.go)
- `TestArithmetic` em [internal/domain/money/money_test.go](../internal/domain/money/money_test.go)
- `TestOverflow` em [internal/domain/money/money_test.go](../internal/domain/money/money_test.go)
- `TestCurrencyMismatch` em [internal/domain/money/money_test.go](../internal/domain/money/money_test.go)
- `TestJSON` em [internal/domain/money/money_test.go](../internal/domain/money/money_test.go)
- `TestUnmarshalRejects` em [internal/domain/money/money_test.go](../internal/domain/money/money_test.go)

### wallet

- `TestWalletDebitCredit` em [internal/domain/wallet_test.go](../internal/domain/wallet_test.go)
- `TestWalletRejectsInvalidMovementsWithoutChangingState` em [internal/domain/wallet_test.go](../internal/domain/wallet_test.go)
- `TestRehydrateWallet` em [internal/domain/wallet_test.go](../internal/domain/wallet_test.go)

### domain

- `TestCodexReviewPublicInvariants` em [internal/domain/codex_review_test.go](../internal/domain/codex_review_test.go)
- `TestLayerBoundaries` em [internal/archtest/layers_test.go](../internal/archtest/layers_test.go)
- `TestFxOnlyInBootstrap` em [internal/archtest/layers_test.go](../internal/archtest/layers_test.go)
- `TestStateMachine` em [internal/domain/transaction_test.go](../internal/domain/transaction_test.go)
- `TestTransitionGuards` em [internal/domain/transaction_test.go](../internal/domain/transaction_test.go)
- `TestRehydrateTransactionRoundTrip` em [internal/domain/transaction_test.go](../internal/domain/transaction_test.go)
- `TestLedgerEntryEquation` em [internal/domain/ledger_test.go](../internal/domain/ledger_test.go)
- `TestLedgerEntryIsReadOnly` em [internal/domain/ledger_test.go](../internal/domain/ledger_test.go)

### rules

- `TestProcessExternalKinds` em [internal/domain/processing_test.go](../internal/domain/processing_test.go)
- `TestReversals` em [internal/domain/processing_test.go](../internal/domain/processing_test.go)
- `TestReferenceValidation` em [internal/domain/processing_test.go](../internal/domain/processing_test.go)
- `TestRequestZeroPolicyPerKind` em [internal/domain/request_test.go](../internal/domain/request_test.go)
- `TestFingerprint` em [internal/domain/request_test.go](../internal/domain/request_test.go)
- `TestClassifyDuplicate` em [internal/domain/request_test.go](../internal/domain/request_test.go)

### opening

- `TestOpenWalletWithPositiveBalance` em [internal/domain/opening_test.go](../internal/domain/opening_test.go)
- `TestOpenWalletWithZeroBalance` em [internal/domain/opening_test.go](../internal/domain/opening_test.go)
- `TestOpenWalletValidation` em [internal/domain/opening_test.go](../internal/domain/opening_test.go)
- `TestStage5OpeningAndBusinessResults` em [tests/integration/application_test.go](../tests/integration/application_test.go)

### schema

- `TestWalletProtections` em [tests/integration/schema_test.go](../tests/integration/schema_test.go)
- `TestLedgerProtections` em [tests/integration/schema_test.go](../tests/integration/schema_test.go)
- `TestTransactionProtections` em [tests/integration/schema_test.go](../tests/integration/schema_test.go)
- `TestInboxAndOutboxProtections` em [tests/integration/schema_test.go](../tests/integration/schema_test.go)
- `TestCodexReviewSchemaRejectsInvalidHistory` em [tests/integration/codex_review_test.go](../tests/integration/codex_review_test.go)

### concurrency

- `TestDatabasePreventsLostUpdateWithoutAppLock` em [tests/integration/schema_concurrency_test.go](../tests/integration/schema_concurrency_test.go)
- `TestDatabaseRejectsBlindBalanceOverwrite` em [tests/integration/schema_concurrency_test.go](../tests/integration/schema_concurrency_test.go)
- `TestDatabasePreventsConcurrentDoubleReversal` em [tests/integration/schema_concurrency_test.go](../tests/integration/schema_concurrency_test.go)
- `TestStage5IndependentWalletsAndCancellation` em [tests/integration/application_test.go](../tests/integration/application_test.go)
- `TestStage12ThreeProcessesContestReplays` em [tests/integration/final_review_test.go](../tests/integration/final_review_test.go)

### atomic

- `TestStage5RollbackAtEveryWrite` em [tests/integration/application_test.go](../tests/integration/application_test.go)
- `TestStage5CommitConstraintFailureRollsBack` em [tests/integration/application_limits_test.go](../tests/integration/application_limits_test.go)
- `TestStage8InboxRollbackAtEveryWrite` em [tests/integration/consumer_test.go](../tests/integration/consumer_test.go)

### auth

- `TestTokensForEachIdentity` em [tests/integration/keycloak_test.go](../tests/integration/keycloak_test.go)
- `TestInvalidClientCredentialsAreRejected` em [tests/integration/keycloak_test.go](../tests/integration/keycloak_test.go)
- `TestStage6AuthenticatedHTTPAndProviderIsolation` em [tests/integration/api_test.go](../tests/integration/api_test.go)
- `TestStage6ExpiredAndForgedKeycloakTokens` em [tests/integration/api_test.go](../tests/integration/api_test.go)
- `TestStage10RealAuthorizationMetricsAndDivergence` em [tests/integration/reconciliation_test.go](../tests/integration/reconciliation_test.go)

### broker

- `TestQueuesAreProvisioned` em [tests/integration/sqs_test.go](../tests/integration/sqs_test.go)
- `TestLeastPrivilegePolicies` em [tests/integration/sqs_test.go](../tests/integration/sqs_test.go)
- `TestBrokerRejectsAuthenticationBypass` em [tests/integration/sqs_test.go](../tests/integration/sqs_test.go)
- `TestBrokerSignatureAndAdminBoundary` em [tests/integration/broker_security_test.go](../tests/integration/broker_security_test.go)

### http

- `TestStage6StrictJSONAndLedgerPagination` em [tests/integration/api_test.go](../tests/integration/api_test.go)
- `TestStage6AmbiguousHeadersRejected` em [tests/integration/api_test.go](../tests/integration/api_test.go)
- `TestStage5MoneyLimitsAndPrivateRejections` em [tests/integration/application_limits_test.go](../tests/integration/application_limits_test.go)
- `TestStage6TransientHTTPFailureAndLifecycle` em [tests/integration/api_test.go](../tests/integration/api_test.go)

### reference

- `TestStage7DurableResolutionAndCombinedReversals` em [tests/integration/references_test.go](../tests/integration/references_test.go)
- `TestStage7ExpiryTerminalInvalidAndInsufficient` em [tests/integration/references_test.go](../tests/integration/references_test.go)
- `TestStage7DirectRollbackDirections` em [tests/integration/references_test.go](../tests/integration/references_test.go)
- `TestStage7PendingChain` em [tests/integration/references_test.go](../tests/integration/references_test.go)
- `TestStage12ReferenceMetadataBoundaries` em [tests/integration/final_review_test.go](../tests/integration/final_review_test.go)

### refRecovery

- `TestStage7WorkersCompeteAndSkipLockedWallet` em [tests/integration/references_test.go](../tests/integration/references_test.go)
- `TestStage7RecoveryRollbackAtEveryWrite` em [tests/integration/references_test.go](../tests/integration/references_test.go)
- `TestStage7AbandonedDatabaseSession` em [tests/integration/references_test.go](../tests/integration/references_test.go)
- `TestStage7PermanentFailureIsDurableAndTerminal` em [tests/integration/references_test.go](../tests/integration/references_test.go)
- `TestStage7TransientFailureNeverFinalizes` em [tests/integration/references_test.go](../tests/integration/references_test.go)
- `TestStage11ReferencesExpireAndInvalidAcrossProcesses` em [tests/integration/distributed_recovery_test.go](../tests/integration/distributed_recovery_test.go)

### consumer

- `TestStage8InboxFinancialAtomicityAndRaces` em [tests/integration/consumer_test.go](../tests/integration/consumer_test.go)
- `TestStage8RealConsumerHTTPAndPendingReference` em [tests/integration/consumer_test.go](../tests/integration/consumer_test.go)
- `TestStage8MalformedMessagesReachRealDLQ` em [tests/integration/consumer_recovery_test.go](../tests/integration/consumer_recovery_test.go)
- `TestStage8BusinessRejectionAcknowledgedAndInboxConflictRedriven` em [tests/integration/consumer_recovery_test.go](../tests/integration/consumer_recovery_test.go)
- `TestStage8ExhaustedTransientRetryReachesDLQ` em [tests/integration/consumer_recovery_test.go](../tests/integration/consumer_recovery_test.go)
- `TestStage8GroupsParallelAndRetryKeepsFIFOOrder` em [tests/integration/consumer_recovery_test.go](../tests/integration/consumer_recovery_test.go)

### outbox

- `TestStage9ClaimsLeaseAndStaleConfirmation` em [tests/integration/outbox_test.go](../tests/integration/outbox_test.go)
- `TestStage9RealPublishersRetryRecoveryAndSnapshots` em [tests/integration/outbox_test.go](../tests/integration/outbox_test.go)
- `TestStage9UncommittedInvisibleAndNetworkOutsideWalletLock` em [tests/integration/outbox_test.go](../tests/integration/outbox_test.go)
- `TestStage9WalletEventsMayArriveOutOfCommitOrder` em [tests/integration/outbox_test.go](../tests/integration/outbox_test.go)

### events

- `TestEnvelopeJSON` em [internal/domain/events_test.go](../internal/domain/events_test.go)
- `TestEventsCarryFixedTypeVersionAndExactMoney` em [internal/domain/events_test.go](../internal/domain/events_test.go)
- `TestCodexReviewEnvelopeIsSnapshot` em [internal/domain/codex_review_test.go](../internal/domain/codex_review_test.go)

### fx

- `TestGraphIsValid` em [internal/bootstrap/bootstrap_test.go](../internal/bootstrap/bootstrap_test.go)
- `TestInvalidConfigPreventsStart` em [internal/bootstrap/bootstrap_test.go](../internal/bootstrap/bootstrap_test.go)
- `TestStopWaitsForActualTermination` em [internal/adapters/referenceworker/worker_test.go](../internal/adapters/referenceworker/worker_test.go)
- `TestStopCancelsIOAndIdleWait` em [internal/adapters/referenceworker/worker_test.go](../internal/adapters/referenceworker/worker_test.go)
- `TestShutdownDrainsOrCancelsAndNeverAcknowledgesEarly` em [internal/adapters/sqsconsumer/worker_test.go](../internal/adapters/sqsconsumer/worker_test.go)
- `TestShutdownDrainAndDeadline` em [internal/adapters/outboxpublisher/worker_test.go](../internal/adapters/outboxpublisher/worker_test.go)
- `TestStage10FxAllRolesPublishesAndShutsDown` em [tests/integration/reconciliation_test.go](../tests/integration/reconciliation_test.go)

### reconcile

- `TestStage10ReconciliationExactSnapshotAndLargeTurnover` em [tests/integration/reconciliation_test.go](../tests/integration/reconciliation_test.go)
- `TestStage10ReconciliationFinalOverflowIsExplicit` em [tests/integration/reconciliation_test.go](../tests/integration/reconciliation_test.go)
- `TestStage10RealAuthorizationMetricsAndDivergence` em [tests/integration/reconciliation_test.go](../tests/integration/reconciliation_test.go)

### observability

- `TestLogsAreSanitizedAndMetricLabelsBounded` em [internal/adapters/observability/diagnostics_test.go](../internal/adapters/observability/diagnostics_test.go)
- `TestStage12ReferenceLogKeepsInputMessageIdentity` em [tests/integration/final_review_test.go](../tests/integration/final_review_test.go)
- `TestStage12FailedMetricRequiresConfirmedAudit` em [tests/integration/final_review_test.go](../tests/integration/final_review_test.go)
- `TestEventFieldsDistinguishesInputMessageFromPendingEvent` em [internal/adapters/outboxpublisher/worker_test.go](../internal/adapters/outboxpublisher/worker_test.go)

### distributed

- `TestStage11ThreeProcessesConcurrencyRestartAndTransports` em [tests/integration/distributed_test.go](../tests/integration/distributed_test.go)
- `TestStage11HTTPKillBeforeAndAfterCommit` em [tests/integration/distributed_test.go](../tests/integration/distributed_test.go)
- `TestStage11FxConsumerKilledAfterCommitBeforeDelete` em [tests/integration/distributed_recovery_test.go](../tests/integration/distributed_recovery_test.go)
- `TestStage11FxPublishersKilledBeforeSendAndBeforeConfirmation` em [tests/integration/distributed_recovery_test.go](../tests/integration/distributed_recovery_test.go)
- `TestStage11RealContainerOutagesAndRecovery` em [tests/integration/distributed_outages_test.go](../tests/integration/distributed_outages_test.go)

### migrations

- `TestMigrationsRoundTrip` em [tests/integration/migrations_test.go](../tests/integration/migrations_test.go)
- `TestIntegrityMigrationPreservesExistingHistory` em [tests/integration/migrations_test.go](../tests/integration/migrations_test.go)
- `TestMigrationErrorsDoNotLeakPassword` em [tests/integration/migrations_test.go](../tests/integration/migrations_test.go)
- `TestAppRoleIsLimited` em [tests/integration/postgres_test.go](../tests/integration/postgres_test.go)
- `TestArchitectureReviewRejectsEffectiveAdministrativeUser` em [tests/integration/architecture_review_test.go](../tests/integration/architecture_review_test.go)

### execution


### static


### external


