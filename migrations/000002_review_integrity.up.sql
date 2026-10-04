-- Correções da revisão Codex: resultado original, rodada e origem da abertura.
BEGIN;

ALTER TABLE wallet.wallets ADD COLUMN creation_xid xid8 NOT NULL DEFAULT pg_current_xact_id();
ALTER TABLE wallet.wallets ADD COLUMN opening_amount bigint NOT NULL DEFAULT 0;
UPDATE wallet.wallets w SET opening_amount = t.amount FROM wallet.wager_transactions t
 WHERE t.wallet_id = w.id AND t.kind = 'OPENING';

CREATE FUNCTION wallet.capture_wallet_creation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.creation_xid := pg_current_xact_id();
    NEW.opening_amount := NEW.balance_amount;
    RETURN NEW;
END;
$$;
CREATE TRIGGER wallets_capture_creation BEFORE INSERT ON wallet.wallets
 FOR EACH ROW EXECUTE FUNCTION wallet.capture_wallet_creation();

CREATE FUNCTION wallet.validate_financial_history() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    t record;
    w record;
    e record;
    reference_round text;
BEGIN
    -- Relê o estado final: uma mesma operação pode ser inserida PENDING e atualizada
    -- antes do commit; eventos de trigger não devem validar um snapshot intermediário.
    SELECT * INTO t FROM wallet.wager_transactions WHERE id = NEW.id;
    IF t.status <> 'PROCESSED' THEN RETURN NULL; END IF;
    IF t.kind <> 'LOSS' THEN
        SELECT * INTO e FROM wallet.ledger_entries WHERE transaction_id = t.id;
        IF FOUND AND (t.result_balance_amount IS DISTINCT FROM e.balance_after
                      OR t.result_balance_currency IS DISTINCT FROM e.currency) THEN
            RAISE EXCEPTION 'result must match its own ledger entry'
                USING ERRCODE = '23000', CONSTRAINT = 'tx_result_matches_ledger';
        END IF;
        IF t.kind = 'OPENING' THEN
            SELECT * INTO w FROM wallet.wallets WHERE id = t.wallet_id;
            IF e.wallet_version IS DISTINCT FROM 1::bigint OR e.balance_before IS DISTINCT FROM 0::bigint
               OR w.creation_xid <> pg_current_xact_id() OR w.opening_amount <= 0
               OR w.opening_amount <> t.amount THEN
                RAISE EXCEPTION 'OPENING is only valid during positive wallet creation'
                    USING ERRCODE = '23000', CONSTRAINT = 'opening_at_creation_only';
            END IF;
        ELSIF e.wallet_version = 1 THEN
            RAISE EXCEPTION 'only OPENING may use ledger version 1'
                USING ERRCODE = '23000', CONSTRAINT = 'ledger_first_version_opening';
        END IF;
    END IF;
    IF t.reference_transaction_id IS NOT NULL THEN
        SELECT round_id INTO reference_round FROM wallet.wager_transactions WHERE id = t.reference_transaction_id;
        IF reference_round IS DISTINCT FROM t.round_id THEN
            RAISE EXCEPTION 'reference must belong to the same round'
                USING ERRCODE = '23000', CONSTRAINT = 'tx_reference_round_matches';
        END IF;
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER transactions_financial_history AFTER INSERT OR UPDATE ON wallet.wager_transactions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION wallet.validate_financial_history();

-- Campos internos são preenchidos pelo trigger e nunca informados pela aplicação.
REVOKE INSERT ON wallet.wallets FROM wallet_app;
GRANT INSERT (id, player_id, currency, balance_amount, version, created_at, updated_at)
 ON wallet.wallets TO wallet_app;
REVOKE ALL ON FUNCTION wallet.capture_wallet_creation() FROM PUBLIC;
REVOKE ALL ON FUNCTION wallet.validate_financial_history() FROM PUBLIC;
COMMIT;
