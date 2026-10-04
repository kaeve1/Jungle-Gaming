-- Reverte 000001: remove todo o schema da aplicação (DADOS SÃO PERDIDOS).
-- Uso previsto: ambientes descartáveis e o teste de ida e volta das migrations.
BEGIN;

DROP TABLE IF EXISTS wallet.outbox_events;
DROP TABLE IF EXISTS wallet.inbox_messages;
DROP TABLE IF EXISTS wallet.ledger_entries;
DROP TABLE IF EXISTS wallet.wager_transactions;
DROP TABLE IF EXISTS wallet.wallets;

DROP FUNCTION IF EXISTS wallet.outbox_events_before_update();
DROP FUNCTION IF EXISTS wallet.wager_transactions_check_processed();
DROP FUNCTION IF EXISTS wallet.wager_transactions_before_update();
DROP FUNCTION IF EXISTS wallet.ledger_before_insert();
DROP FUNCTION IF EXISTS wallet.wallet_matches_ledger();
DROP FUNCTION IF EXISTS wallet.wallets_before_update();
DROP FUNCTION IF EXISTS wallet.reject_delete();
DROP FUNCTION IF EXISTS wallet.reject_modification();

COMMIT;
