-- Schema inicial. Invariantes financeiras impostas pelo banco, independentemente
-- da aplicação (enunciado §5.3 e §5.8; ARCHITECTURE.md §10).
--
-- Executado como wallet_migrator (dono do schema "wallet"). A aplicação usa
-- wallet_app, que recebe no fim deste arquivo apenas os privilégios mínimos,
-- inclusive por coluna. Erros das proteções usam SQLSTATE 23000
-- (integrity_constraint_violation) com o nome da regra em CONSTRAINT.
BEGIN;

SET LOCAL search_path = wallet;

-- ---------------------------------------------------------------------------
-- Carteiras
-- ---------------------------------------------------------------------------
CREATE TABLE wallet.wallets (
    id             uuid        PRIMARY KEY,
    player_id      uuid        NOT NULL,
    currency       char(3)     NOT NULL CONSTRAINT wallets_currency_supported CHECK (currency IN ('BRL', 'USD', 'EUR')),
    -- Saldo em unidades mínimas (centavos). Nunca negativo, mesmo com bug na aplicação.
    balance_amount bigint      NOT NULL CONSTRAINT wallets_balance_non_negative CHECK (balance_amount >= 0),
    version        bigint      NOT NULL CONSTRAINT wallets_version_positive CHECK (version >= 1),
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    CONSTRAINT wallets_timestamps CHECK (updated_at >= created_at),
    -- Uma carteira por jogador e moeda (enunciado §6.2).
    CONSTRAINT wallets_player_currency_unique UNIQUE (player_id, currency),
    -- Alvo da FK do ledger: garante que o lançamento tem a moeda da carteira.
    CONSTRAINT wallets_id_currency_unique UNIQUE (id, currency)
);

-- ---------------------------------------------------------------------------
-- Transações (operações externas e abertura interna)
-- ---------------------------------------------------------------------------
CREATE TABLE wallet.wager_transactions (
    id                                uuid        PRIMARY KEY,
    origin                            text        NOT NULL CONSTRAINT tx_origin_valid CHECK (origin IN ('INTERNAL', 'EXTERNAL')),
    kind                              text        NOT NULL CONSTRAINT tx_kind_valid CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    status                            text        NOT NULL CONSTRAINT tx_status_valid CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    wallet_id                         uuid        NOT NULL REFERENCES wallet.wallets (id),
    player_id                         uuid        NOT NULL,
    currency                          char(3)     NOT NULL CONSTRAINT tx_currency_supported CHECK (currency IN ('BRL', 'USD', 'EUR')),
    amount                            bigint      NOT NULL,
    -- Metadados externos: obrigatórios em EXTERNAL, proibidos em INTERNAL.
    provider_id                       text,
    external_transaction_id           text,
    idempotency_key                   text,
    request_hash                      text,
    request_hash_version              integer,
    round_id                          text,
    game_id                           text,
    reference_external_transaction_id text,
    -- Resultado e referência resolvida.
    reference_transaction_id          uuid        REFERENCES wallet.wager_transactions (id),
    failure_code                      text,
    result_balance_amount             bigint,
    result_balance_currency           char(3),
    -- Espera por referência (PENDING_REFERENCE).
    reference_attempts                integer     NOT NULL DEFAULT 0 CONSTRAINT tx_attempts_non_negative CHECK (reference_attempts >= 0),
    next_reference_attempt_at         timestamptz,
    reference_expires_at              timestamptz,
    -- Rastreamento propagado aos eventos (inclusive os emitidos depois pelo worker).
    correlation_id                    text        NOT NULL CONSTRAINT tx_correlation_format CHECK (correlation_id ~ '^[A-Za-z0-9._:-]{1,128}$'),
    causation_id                      text        CONSTRAINT tx_causation_format CHECK (causation_id ~ '^[A-Za-z0-9._:-]{1,128}$'),
    created_at                        timestamptz NOT NULL,
    updated_at                        timestamptz NOT NULL,

    CONSTRAINT tx_timestamps CHECK (updated_at >= created_at),
    -- OPENING é a única operação interna, e só ela (enunciado §6.3).
    CONSTRAINT tx_opening_is_internal CHECK ((origin = 'INTERNAL') = (kind = 'OPENING')),
    -- COALESCE: num CHECK, resultado NULL conta como aprovado; "NULL ~ padrão" é NULL.
    -- Sem ele, uma operação externa com provider_id nulo passaria.
    CONSTRAINT tx_external_metadata CHECK (COALESCE(
        (origin = 'EXTERNAL'
            AND provider_id ~ '^[A-Za-z0-9._:-]{1,128}$'
            AND external_transaction_id ~ '^[A-Za-z0-9._:-]{1,128}$'
            AND idempotency_key ~ '^[!-~]{1,255}$'
            AND request_hash ~ '^[0-9a-f]{64}$'
            AND request_hash_version >= 1
            AND round_id ~ '^[A-Za-z0-9._:-]{1,128}$'
            AND game_id ~ '^[A-Za-z0-9._:-]{1,128}$'
            AND (reference_external_transaction_id IS NULL OR reference_external_transaction_id ~ '^[A-Za-z0-9._:-]{1,128}$'))
        OR (origin = 'INTERNAL'
            AND provider_id IS NULL AND external_transaction_id IS NULL AND idempotency_key IS NULL
            AND request_hash IS NULL AND request_hash_version IS NULL AND round_id IS NULL
            AND game_id IS NULL AND reference_external_transaction_id IS NULL), FALSE)),
    -- Política de valores por tipo (enunciado §7): LOSS exige zero; os demais, valor positivo.
    CONSTRAINT tx_amount_policy CHECK ((kind = 'LOSS' AND amount = 0) OR (kind <> 'LOSS' AND amount > 0)),
    -- Referências por tipo: obrigatória em REFUND/ROLLBACK, opcional em WIN, proibida no resto.
    CONSTRAINT tx_reference_policy CHECK (
        CASE kind
            WHEN 'REFUND' THEN reference_external_transaction_id IS NOT NULL
            WHEN 'ROLLBACK' THEN reference_external_transaction_id IS NOT NULL
            WHEN 'WIN' THEN TRUE
            ELSE reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL
        END),
    CONSTRAINT tx_no_self_reference CHECK (reference_transaction_id IS DISTINCT FROM id),
    -- Código de falha existe se, e somente se, a operação foi rejeitada ou falhou.
    CONSTRAINT tx_failure_code_presence CHECK ((status IN ('REJECTED', 'FAILED')) = (failure_code IS NOT NULL)),
    CONSTRAINT tx_failure_code_valid CHECK (
        failure_code IS NULL
        OR (status = 'REJECTED' AND failure_code IN (
            'INSUFFICIENT_FUNDS', 'REVERSAL_INSUFFICIENT_FUNDS', 'WALLET_PLAYER_MISMATCH', 'CURRENCY_MISMATCH',
            'BALANCE_LIMIT_EXCEEDED', 'REFERENCE_NOT_FOUND', 'REFERENCE_NOT_PROCESSED', 'REFERENCE_KIND_NOT_ALLOWED',
            'REFERENCE_MISMATCH', 'REFERENCE_AMOUNT_MISMATCH', 'REFERENCE_ALREADY_REVERSED'))
        OR (status = 'FAILED' AND failure_code IN ('INTERNAL_PROCESSING_FAILED'))),
    -- Saldo devolvido ao provedor: obrigatório em PROCESSED (replays devolvem o original).
    CONSTRAINT tx_result_balance_pair CHECK ((result_balance_amount IS NULL) = (result_balance_currency IS NULL)),
    CONSTRAINT tx_result_balance_non_negative CHECK (result_balance_amount IS NULL OR result_balance_amount >= 0),
    CONSTRAINT tx_processed_has_result CHECK (status <> 'PROCESSED' OR result_balance_amount IS NOT NULL),
    CONSTRAINT tx_pending_reference_schedule CHECK (
        status <> 'PENDING_REFERENCE' OR (next_reference_attempt_at IS NOT NULL AND reference_expires_at IS NOT NULL)),

    -- Idempotência persistente (ARCHITECTURE.md §6): a chave tem escopo por provedor,
    -- e a mesma operação externa não pode existir duas vezes, nem com outra chave.
    CONSTRAINT tx_provider_idempotency_key_unique UNIQUE (provider_id, idempotency_key),
    CONSTRAINT tx_provider_external_id_unique UNIQUE (provider_id, external_transaction_id),
    -- Alvo da FK do ledger: o lançamento pertence à carteira da transação.
    CONSTRAINT tx_id_wallet_unique UNIQUE (id, wallet_id)
);

-- Uma única abertura por carteira: impede crédito inicial duplicado (enunciado §6.3).
CREATE UNIQUE INDEX tx_one_opening_per_wallet ON wallet.wager_transactions (wallet_id) WHERE kind = 'OPENING';

-- No máximo UMA reversão bem-sucedida por transação, seja REFUND ou ROLLBACK
-- (ARCHITECTURE.md §8). Duas reversões concorrentes não passam, mesmo sem lock.
CREATE UNIQUE INDEX tx_one_successful_reversal ON wallet.wager_transactions (reference_transaction_id)
    WHERE kind IN ('REFUND', 'ROLLBACK') AND status = 'PROCESSED';

-- Worker de referências: pendências vencidas.
CREATE INDEX tx_pending_reference_due ON wallet.wager_transactions (next_reference_attempt_at)
    WHERE status = 'PENDING_REFERENCE';
-- Antecipação de pendências quando a referência chega.
CREATE INDEX tx_pending_reference_lookup ON wallet.wager_transactions (provider_id, reference_external_transaction_id)
    WHERE status = 'PENDING_REFERENCE';

-- ---------------------------------------------------------------------------
-- Ledger (append-only)
-- ---------------------------------------------------------------------------
CREATE TABLE wallet.ledger_entries (
    id             uuid        PRIMARY KEY,
    wallet_id      uuid        NOT NULL,
    currency       char(3)     NOT NULL,
    transaction_id uuid        NOT NULL,
    direction      text        NOT NULL CONSTRAINT ledger_direction_valid CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount         bigint      NOT NULL CONSTRAINT ledger_amount_positive CHECK (amount > 0),
    balance_before bigint      NOT NULL CONSTRAINT ledger_before_non_negative CHECK (balance_before >= 0),
    balance_after  bigint      NOT NULL CONSTRAINT ledger_after_non_negative CHECK (balance_after >= 0),
    wallet_version bigint      NOT NULL CONSTRAINT ledger_version_positive CHECK (wallet_version >= 1),
    created_at     timestamptz NOT NULL,
    -- balanceAfter = balanceBefore ± amount (enunciado §6.4).
    CONSTRAINT ledger_equation CHECK (
        (direction = 'CREDIT' AND balance_after = balance_before + amount)
        OR (direction = 'DEBIT' AND balance_after = balance_before - amount)),
    -- Mesma moeda da carteira e mesma carteira da transação.
    CONSTRAINT ledger_wallet_currency_fk FOREIGN KEY (wallet_id, currency) REFERENCES wallet.wallets (id, currency),
    CONSTRAINT ledger_transaction_wallet_fk FOREIGN KEY (transaction_id, wallet_id) REFERENCES wallet.wager_transactions (id, wallet_id),
    -- Um lançamento por transação e carteira (enunciado §6.4).
    CONSTRAINT ledger_wallet_transaction_unique UNIQUE (wallet_id, transaction_id),
    -- Encadeamento e ordem estável da paginação.
    CONSTRAINT ledger_wallet_version_unique UNIQUE (wallet_id, wallet_version)
);
CREATE INDEX ledger_transaction ON wallet.ledger_entries (transaction_id);

-- ---------------------------------------------------------------------------
-- Inbox (entrada SQS) e outbox (eventos de saída)
-- ---------------------------------------------------------------------------
CREATE TABLE wallet.inbox_messages (
    consumer_name  text        NOT NULL CONSTRAINT inbox_consumer_format CHECK (consumer_name ~ '^[A-Za-z0-9._:-]{1,64}$'),
    message_id     text        NOT NULL CONSTRAINT inbox_message_id_format CHECK (message_id ~ '^[A-Za-z0-9._:-]{1,128}$'),
    message_hash   text        NOT NULL CONSTRAINT inbox_hash_format CHECK (message_hash ~ '^[0-9a-f]{64}$'),
    transaction_id uuid        NOT NULL REFERENCES wallet.wager_transactions (id),
    received_at    timestamptz NOT NULL,
    processed_at   timestamptz NOT NULL,
    CONSTRAINT inbox_timestamps CHECK (processed_at >= received_at),
    -- Uma mensagem é tratada uma vez por consumidor (enunciado §6.5).
    PRIMARY KEY (consumer_name, message_id)
);

CREATE TABLE wallet.outbox_events (
    event_id         uuid        PRIMARY KEY,
    aggregate_type   text        NOT NULL CONSTRAINT outbox_aggregate_type_valid CHECK (aggregate_type IN ('WagerTransaction', 'Wallet')),
    aggregate_id     uuid        NOT NULL,
    -- Carteira: MessageGroupId na publicação (ordem por carteira).
    partition_key    uuid        NOT NULL,
    event_type       text        NOT NULL CONSTRAINT outbox_event_type_valid CHECK (event_type IN (
        'WagerTransactionProcessed', 'WagerTransactionRejected', 'WalletBalanceChanged', 'WagerTransactionPendingReference')),
    event_version    integer     NOT NULL CONSTRAINT outbox_event_version_positive CHECK (event_version >= 1),
    -- Envelope completo, gerado uma vez; imutável (snapshot).
    payload          jsonb       NOT NULL CONSTRAINT outbox_payload_object CHECK (jsonb_typeof(payload) = 'object'),
    occurred_at      timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    attempts         integer     NOT NULL DEFAULT 0 CONSTRAINT outbox_attempts_non_negative CHECK (attempts >= 0),
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    claimed_by       text,
    claim_expires_at timestamptz,
    published_at     timestamptz,
    last_error       text        CONSTRAINT outbox_last_error_size CHECK (char_length(last_error) <= 1000),
    CONSTRAINT outbox_claim_pair CHECK ((claimed_by IS NULL) = (claim_expires_at IS NULL))
);
-- Publisher: eventos ainda não publicados, por próxima tentativa.
CREATE INDEX outbox_unpublished ON wallet.outbox_events (next_attempt_at) WHERE published_at IS NULL;

-- ---------------------------------------------------------------------------
-- Proteções por trigger
-- ---------------------------------------------------------------------------

-- Tabelas append-only: nenhuma alteração ou exclusão, nem pelo dono do schema.
CREATE FUNCTION wallet.reject_modification() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % is not allowed', TG_TABLE_NAME, TG_OP
        USING ERRCODE = '23000', CONSTRAINT = TG_TABLE_NAME || '_append_only';
END;
$$;

CREATE TRIGGER ledger_entries_append_only BEFORE UPDATE OR DELETE ON wallet.ledger_entries
    FOR EACH ROW EXECUTE FUNCTION wallet.reject_modification();
CREATE TRIGGER ledger_entries_no_truncate BEFORE TRUNCATE ON wallet.ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION wallet.reject_modification();
CREATE TRIGGER inbox_messages_append_only BEFORE UPDATE OR DELETE ON wallet.inbox_messages
    FOR EACH ROW EXECUTE FUNCTION wallet.reject_modification();
CREATE TRIGGER inbox_messages_no_truncate BEFORE TRUNCATE ON wallet.inbox_messages
    FOR EACH STATEMENT EXECUTE FUNCTION wallet.reject_modification();

-- Carteiras e transações não são apagadas (auditoria).
CREATE FUNCTION wallet.reject_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% rows cannot be deleted', TG_TABLE_NAME
        USING ERRCODE = '23000', CONSTRAINT = TG_TABLE_NAME || '_no_delete';
END;
$$;
CREATE TRIGGER wallets_no_delete BEFORE DELETE ON wallet.wallets
    FOR EACH ROW EXECUTE FUNCTION wallet.reject_delete();
CREATE TRIGGER wallets_no_truncate BEFORE TRUNCATE ON wallet.wallets
    FOR EACH STATEMENT EXECUTE FUNCTION wallet.reject_delete();
CREATE TRIGGER wager_transactions_no_delete BEFORE DELETE ON wallet.wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wallet.reject_delete();
CREATE TRIGGER wager_transactions_no_truncate BEFORE TRUNCATE ON wallet.wager_transactions
    FOR EACH STATEMENT EXECUTE FUNCTION wallet.reject_delete();
CREATE TRIGGER outbox_events_no_delete BEFORE DELETE ON wallet.outbox_events
    FOR EACH ROW EXECUTE FUNCTION wallet.reject_delete();
CREATE TRIGGER outbox_events_no_truncate BEFORE TRUNCATE ON wallet.outbox_events
    FOR EACH STATEMENT EXECUTE FUNCTION wallet.reject_delete();

-- Carteira: identidade imutável; versão sobe exatamente 1 a cada mudança de saldo
-- e não muda sem mudança de saldo (enunciado §6.2).
CREATE FUNCTION wallet.wallets_before_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.player_id, NEW.currency, NEW.created_at) IS DISTINCT FROM (OLD.id, OLD.player_id, OLD.currency, OLD.created_at) THEN
        RAISE EXCEPTION 'wallet identity is immutable'
            USING ERRCODE = '23000', CONSTRAINT = 'wallets_identity_immutable';
    END IF;
    IF NEW.balance_amount <> OLD.balance_amount AND NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'a balance change must increment the version by exactly 1'
            USING ERRCODE = '23000', CONSTRAINT = 'wallets_version_rule';
    END IF;
    IF NEW.balance_amount = OLD.balance_amount AND NEW.version <> OLD.version THEN
        RAISE EXCEPTION 'the version changes only with the balance'
            USING ERRCODE = '23000', CONSTRAINT = 'wallets_version_rule';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER wallets_before_update BEFORE UPDATE ON wallet.wallets
    FOR EACH ROW EXECUTE FUNCTION wallet.wallets_before_update();

-- Coerência saldo × ledger, verificada no COMMIT: o saldo e a versão da carteira
-- são exatamente os do último lançamento; sem lançamentos, saldo 0 e versão 1.
-- Impede alterar o saldo sem lançamento e lançar sem alterar o saldo.
-- Uma única função de trigger para as duas tabelas: funções de trigger não exigem
-- EXECUTE de quem grava, mas uma função auxiliar chamada por elas exigiria — e
-- a aplicação não tem EXECUTE em nada (falha encontrada pelo teste de controle positivo).
CREATE FUNCTION wallet.wallet_matches_ledger() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    v_wallet_id uuid;
    w record;
    e record;
BEGIN
    IF TG_TABLE_NAME = 'wallets' THEN
        v_wallet_id := NEW.id;
    ELSE
        v_wallet_id := NEW.wallet_id;
    END IF;
    SELECT balance_amount, version INTO w FROM wallet.wallets WHERE id = v_wallet_id;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    SELECT wallet_version, balance_after INTO e
      FROM wallet.ledger_entries WHERE wallet_id = v_wallet_id
     ORDER BY wallet_version DESC LIMIT 1;
    IF NOT FOUND THEN
        IF w.balance_amount <> 0 OR w.version <> 1 THEN
            RAISE EXCEPTION 'wallet % has balance/version without ledger entries', v_wallet_id
                USING ERRCODE = '23000', CONSTRAINT = 'wallet_matches_ledger';
        END IF;
    ELSIF w.version <> e.wallet_version OR w.balance_amount <> e.balance_after THEN
        RAISE EXCEPTION 'wallet % (balance %, version %) differs from its last ledger entry (balance %, version %)',
            v_wallet_id, w.balance_amount, w.version, e.balance_after, e.wallet_version
            USING ERRCODE = '23000', CONSTRAINT = 'wallet_matches_ledger';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER wallets_match_ledger AFTER INSERT OR UPDATE ON wallet.wallets
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION wallet.wallet_matches_ledger();
CREATE CONSTRAINT TRIGGER ledger_entries_match_wallet AFTER INSERT ON wallet.ledger_entries
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION wallet.wallet_matches_ledger();

-- Lançamento: encadeado ao anterior e coerente com a transação que o originou
-- (PROCESSED, mesmo valor e moeda, direção determinada pelo tipo).
CREATE FUNCTION wallet.ledger_before_insert() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    prev record;
    tx record;
    expected text;
BEGIN
    SELECT wallet_version, balance_after INTO prev
      FROM wallet.ledger_entries WHERE wallet_id = NEW.wallet_id
     ORDER BY wallet_version DESC LIMIT 1;
    IF FOUND THEN
        IF NEW.wallet_version <> prev.wallet_version + 1 OR NEW.balance_before <> prev.balance_after THEN
            RAISE EXCEPTION 'ledger chain broken for wallet %: expected version % and balance_before %',
                NEW.wallet_id, prev.wallet_version + 1, prev.balance_after
                USING ERRCODE = '23000', CONSTRAINT = 'ledger_chain';
        END IF;
    -- Primeiro lançamento: abertura com saldo (versão 1) ou primeira movimentação
    -- de carteira aberta com zero (versão 2); sempre a partir de saldo 0.
    ELSIF NEW.balance_before <> 0 OR NEW.wallet_version NOT IN (1, 2) THEN
        RAISE EXCEPTION 'first ledger entry of wallet % must start from balance 0 at version 1 or 2', NEW.wallet_id
            USING ERRCODE = '23000', CONSTRAINT = 'ledger_chain';
    END IF;

    SELECT t.kind, t.status, t.amount, t.currency, r.kind AS ref_kind INTO tx
      FROM wallet.wager_transactions t
      LEFT JOIN wallet.wager_transactions r ON r.id = t.reference_transaction_id
     WHERE t.id = NEW.transaction_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'transaction % does not exist', NEW.transaction_id
            USING ERRCODE = '23503', CONSTRAINT = 'ledger_transaction_wallet_fk';
    END IF;
    IF tx.status <> 'PROCESSED' THEN
        RAISE EXCEPTION 'ledger entries are only allowed for PROCESSED transactions (got %)', tx.status
            USING ERRCODE = '23000', CONSTRAINT = 'ledger_requires_processed';
    END IF;
    IF tx.amount <> NEW.amount OR tx.currency <> NEW.currency THEN
        RAISE EXCEPTION 'ledger amount/currency differ from the transaction'
            USING ERRCODE = '23000', CONSTRAINT = 'ledger_matches_transaction';
    END IF;
    expected := CASE tx.kind
        WHEN 'BET' THEN 'DEBIT'
        WHEN 'WIN' THEN 'CREDIT'
        WHEN 'REFUND' THEN 'CREDIT'
        WHEN 'OPENING' THEN 'CREDIT'
        WHEN 'ROLLBACK' THEN CASE tx.ref_kind WHEN 'BET' THEN 'CREDIT' WHEN 'WIN' THEN 'DEBIT' WHEN 'REFUND' THEN 'DEBIT' END
    END; -- LOSS → NULL: nunca tem lançamento
    IF expected IS NULL OR expected <> NEW.direction THEN
        RAISE EXCEPTION '% % cannot produce a % entry', tx.kind, COALESCE(tx.ref_kind, ''), NEW.direction
            USING ERRCODE = '23000', CONSTRAINT = 'ledger_direction_matches_kind';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER ledger_entries_before_insert BEFORE INSERT ON wallet.ledger_entries
    FOR EACH ROW EXECUTE FUNCTION wallet.ledger_before_insert();

-- Transação: estados terminais imutáveis; identidade e dados financeiros
-- imutáveis em qualquer estado; PENDING_REFERENCE não volta a PENDING.
CREATE FUNCTION wallet.wager_transactions_before_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
        RAISE EXCEPTION 'transaction % is in terminal state %', OLD.id, OLD.status
            USING ERRCODE = '23000', CONSTRAINT = 'tx_terminal_immutable';
    END IF;
    IF (NEW.id, NEW.origin, NEW.kind, NEW.wallet_id, NEW.player_id, NEW.currency, NEW.amount, NEW.provider_id,
        NEW.external_transaction_id, NEW.idempotency_key, NEW.request_hash, NEW.request_hash_version, NEW.round_id,
        NEW.game_id, NEW.reference_external_transaction_id, NEW.correlation_id, NEW.causation_id, NEW.created_at)
       IS DISTINCT FROM
       (OLD.id, OLD.origin, OLD.kind, OLD.wallet_id, OLD.player_id, OLD.currency, OLD.amount, OLD.provider_id,
        OLD.external_transaction_id, OLD.idempotency_key, OLD.request_hash, OLD.request_hash_version, OLD.round_id,
        OLD.game_id, OLD.reference_external_transaction_id, OLD.correlation_id, OLD.causation_id, OLD.created_at) THEN
        RAISE EXCEPTION 'transaction identity and financial data are immutable'
            USING ERRCODE = '23000', CONSTRAINT = 'tx_identity_immutable';
    END IF;
    IF OLD.status = 'PENDING_REFERENCE' AND NEW.status = 'PENDING' THEN
        RAISE EXCEPTION 'PENDING_REFERENCE cannot return to PENDING'
            USING ERRCODE = '23000', CONSTRAINT = 'tx_status_transition';
    END IF;
    IF NEW.reference_attempts < OLD.reference_attempts THEN
        RAISE EXCEPTION 'reference attempts cannot decrease'
            USING ERRCODE = '23000', CONSTRAINT = 'tx_status_transition';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER wager_transactions_before_update BEFORE UPDATE ON wallet.wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wallet.wager_transactions_before_update();

-- Operação PROCESSED, verificada no COMMIT: mesma moeda e jogador da carteira;
-- exatamente um lançamento (nenhum para LOSS); referência resolvida válida.
CREATE FUNCTION wallet.wager_transactions_check_processed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    w record;
    r record;
    entries integer;
BEGIN
    IF NEW.status <> 'PROCESSED' THEN
        RETURN NULL;
    END IF;
    SELECT currency, player_id INTO w FROM wallet.wallets WHERE id = NEW.wallet_id;
    IF w.currency <> NEW.currency OR w.player_id <> NEW.player_id
       OR NEW.result_balance_currency <> NEW.currency THEN
        RAISE EXCEPTION 'processed transaction % must match wallet currency and player', NEW.id
            USING ERRCODE = '23000', CONSTRAINT = 'tx_processed_matches_wallet';
    END IF;
    SELECT count(*) INTO entries FROM wallet.ledger_entries WHERE transaction_id = NEW.id;
    IF (NEW.kind = 'LOSS' AND entries <> 0) OR (NEW.kind <> 'LOSS' AND entries <> 1) THEN
        RAISE EXCEPTION 'processed % transaction % has % ledger entries', NEW.kind, NEW.id, entries
            USING ERRCODE = '23000', CONSTRAINT = 'tx_processed_has_ledger_entry';
    END IF;
    IF NEW.reference_transaction_id IS NOT NULL THEN
        SELECT kind, status, wallet_id, amount, provider_id, external_transaction_id INTO r
          FROM wallet.wager_transactions WHERE id = NEW.reference_transaction_id;
        IF r.status <> 'PROCESSED' OR r.wallet_id <> NEW.wallet_id OR r.provider_id IS DISTINCT FROM NEW.provider_id
           OR r.external_transaction_id IS DISTINCT FROM NEW.reference_external_transaction_id
           OR NOT (CASE NEW.kind
                       WHEN 'REFUND' THEN r.kind = 'BET'
                       WHEN 'WIN' THEN r.kind = 'BET'
                       WHEN 'ROLLBACK' THEN r.kind IN ('BET', 'WIN', 'REFUND')
                       ELSE FALSE
                   END)
           OR (NEW.kind IN ('REFUND', 'ROLLBACK') AND r.amount <> NEW.amount) THEN
            RAISE EXCEPTION 'processed transaction % has an invalid reference', NEW.id
                USING ERRCODE = '23000', CONSTRAINT = 'tx_processed_reference_valid';
        END IF;
    ELSIF NEW.kind IN ('REFUND', 'ROLLBACK') OR (NEW.kind = 'WIN' AND NEW.reference_external_transaction_id IS NOT NULL) THEN
        RAISE EXCEPTION 'processed % transaction % has no resolved reference', NEW.kind, NEW.id
            USING ERRCODE = '23000', CONSTRAINT = 'tx_processed_reference_valid';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER wager_transactions_processed_consistency AFTER INSERT OR UPDATE ON wallet.wager_transactions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION wallet.wager_transactions_check_processed();

-- Outbox: o evento é imutável; só os campos de publicação mudam, e um evento
-- publicado não volta a "não publicado".
CREATE FUNCTION wallet.outbox_events_before_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.event_id, NEW.aggregate_type, NEW.aggregate_id, NEW.partition_key, NEW.event_type, NEW.event_version,
        NEW.payload, NEW.occurred_at, NEW.created_at)
       IS DISTINCT FROM
       (OLD.event_id, OLD.aggregate_type, OLD.aggregate_id, OLD.partition_key, OLD.event_type, OLD.event_version,
        OLD.payload, OLD.occurred_at, OLD.created_at) THEN
        RAISE EXCEPTION 'outbox event % is immutable', OLD.event_id
            USING ERRCODE = '23000', CONSTRAINT = 'outbox_event_immutable';
    END IF;
    IF OLD.published_at IS NOT NULL AND NEW.published_at IS DISTINCT FROM OLD.published_at THEN
        RAISE EXCEPTION 'outbox event % was already published', OLD.event_id
            USING ERRCODE = '23000', CONSTRAINT = 'outbox_published_final';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER outbox_events_before_update BEFORE UPDATE ON wallet.outbox_events
    FOR EACH ROW EXECUTE FUNCTION wallet.outbox_events_before_update();

-- ---------------------------------------------------------------------------
-- Privilégios mínimos da aplicação (inclusive por coluna)
-- ---------------------------------------------------------------------------
REVOKE ALL ON ALL TABLES IN SCHEMA wallet FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA wallet FROM PUBLIC;

GRANT SELECT, INSERT ON wallet.wallets TO wallet_app;
GRANT UPDATE (balance_amount, version, updated_at) ON wallet.wallets TO wallet_app;

GRANT SELECT, INSERT ON wallet.wager_transactions TO wallet_app;
GRANT UPDATE (status, failure_code, result_balance_amount, result_balance_currency, reference_transaction_id,
              reference_attempts, next_reference_attempt_at, reference_expires_at, updated_at)
    ON wallet.wager_transactions TO wallet_app;

GRANT SELECT, INSERT ON wallet.ledger_entries TO wallet_app;
GRANT SELECT, INSERT ON wallet.inbox_messages TO wallet_app;

GRANT SELECT, INSERT ON wallet.outbox_events TO wallet_app;
GRANT UPDATE (attempts, next_attempt_at, claimed_by, claim_expires_at, published_at, last_error)
    ON wallet.outbox_events TO wallet_app;

-- Nenhum EXECUTE: os triggers disparam sem que a aplicação possa chamar as funções.

COMMIT;
