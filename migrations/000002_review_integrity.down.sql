BEGIN;
DROP TRIGGER transactions_financial_history ON wallet.wager_transactions;
DROP FUNCTION wallet.validate_financial_history();
DROP TRIGGER wallets_capture_creation ON wallet.wallets;
DROP FUNCTION wallet.capture_wallet_creation();
ALTER TABLE wallet.wallets DROP COLUMN creation_xid, DROP COLUMN opening_amount;
GRANT INSERT ON wallet.wallets TO wallet_app;
COMMIT;
