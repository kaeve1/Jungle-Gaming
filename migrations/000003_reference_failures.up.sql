ALTER TABLE wallet.wager_transactions ADD COLUMN reference_failure_attempts integer NOT NULL DEFAULT 0
 CONSTRAINT tx_reference_failure_attempts_valid CHECK (reference_failure_attempts BETWEEN 0 AND 3);
GRANT UPDATE (reference_failure_attempts) ON wallet.wager_transactions TO wallet_app;
