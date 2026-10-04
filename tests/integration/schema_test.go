//go:build integration

package integration

import "testing"

// Todos os testes deste arquivo usam SQL direto contra o schema real, como o
// usuário da aplicação (wallet_app) — ou como o dono do schema, quando o
// objetivo é provar que a proteção não depende só de permissões. Cada caso roda
// em sua própria transação desfeita no fim.

// Controle positivo: um fluxo coerente passa por todas as verificações,
// inclusive as adiadas para o COMMIT. Sem ele, um teste negativo poderia passar
// por um erro na própria montagem dos dados.
func TestSchemaAcceptsCoherentHistory(t *testing.T) {
	w := newWorld(t, "wallet_app")
	walletID, playerID := w.wallet(10000) // 100.00
	bet := w.processedBet(walletID, playerID, 2500, 10000, 2)

	// LOSS processada: sem lançamento, sem mudança de versão.
	w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "LOSS", Amount: 0, Result: ptr64(7500)})
	// Rejeição: sem lançamento; jogador diferente é aceito (rejeição auditável).
	w.mustOp(op{Wallet: walletID, Player: uuid(), Kind: "BET", Amount: 1, Status: "REJECTED", Failure: "WALLET_PLAYER_MISMATCH"})
	// Pendência de referência.
	w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "REFUND", Amount: 100, Status: "PENDING_REFERENCE", RefExtID: "not-yet"})
	// REFUND processado da BET.
	refund := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "REFUND", Amount: 2500,
		RefExtID: "ext-" + bet[:8], RefTxID: bet, Result: ptr64(10000)})
	if err := w.entry(walletID, refund, "CREDIT", 2500, 7500, 10000, 3); err != nil {
		t.Fatal(err)
	}
	if err := w.setBalance(walletID, 10000, 3); err != nil {
		t.Fatal(err)
	}
	// Carteira aberta com zero: primeira movimentação na versão 2.
	zeroWallet, zeroPlayer := w.wallet(0)
	win := w.mustOp(op{Wallet: zeroWallet, Player: zeroPlayer, Kind: "WIN", Amount: 500, Result: ptr64(500)})
	if err := w.entry(zeroWallet, win, "CREDIT", 500, 0, 500, 2); err != nil {
		t.Fatal(err)
	}
	if err := w.setBalance(zeroWallet, 500, 2); err != nil {
		t.Fatal(err)
	}
	if err := w.commitChecks(); err != nil {
		t.Fatalf("coherent history rejected: %v", err)
	}
}

func TestWalletProtections(t *testing.T) {
	t.Run("negative balance", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		err := w.exec(`INSERT INTO wallet.wallets VALUES ($1, $2, 'BRL', -1, 1, now(), now())`, uuid(), uuid())
		wantViolation(t, err, "23514", "wallets_balance_non_negative")
	})
	t.Run("one wallet per player and currency", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		_, player := w.wallet(0)
		err := w.exec(`INSERT INTO wallet.wallets VALUES ($1, $2, 'BRL', 0, 1, now(), now())`, uuid(), player)
		wantViolation(t, err, "23505", "wallets_player_currency_unique")
	})
	t.Run("unsupported currency", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		err := w.exec(`INSERT INTO wallet.wallets VALUES ($1, $2, 'JPY', 0, 1, now(), now())`, uuid(), uuid())
		wantViolation(t, err, "23514", "wallets_currency_supported")
	})
	t.Run("balance without opening entry", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		w.mustExec(`INSERT INTO wallet.wallets VALUES ($1, $2, 'BRL', 5000, 1, now(), now())`, uuid(), uuid())
		wantViolation(t, w.commitChecks(), "23000", "wallet_matches_ledger")
	})
	t.Run("balance change without ledger entry", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, _ := w.wallet(10000)
		w.mustExec(`UPDATE wallet.wallets SET balance_amount = 999999, version = 2 WHERE id = $1`, walletID)
		wantViolation(t, w.commitChecks(), "23000", "wallet_matches_ledger")
	})
	t.Run("version without balance change", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, _ := w.wallet(10000)
		wantViolation(t, w.exec(`UPDATE wallet.wallets SET version = 2 WHERE id = $1`, walletID), "23000", "wallets_version_rule")
	})
	t.Run("version must increase by exactly one", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, _ := w.wallet(10000)
		wantViolation(t, w.setBalance(walletID, 9000, 3), "23000", "wallets_version_rule")
	})
	t.Run("app cannot change identity columns", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, _ := w.wallet(0)
		wantViolation(t, w.exec(`UPDATE wallet.wallets SET player_id = $2 WHERE id = $1`, walletID, uuid()), "42501", "")
	})
	t.Run("owner cannot change identity either", func(t *testing.T) {
		w := newWorld(t, "wallet_migrator")
		walletID, _ := w.wallet(0)
		wantViolation(t, w.exec(`UPDATE wallet.wallets SET currency = 'USD' WHERE id = $1`, walletID), "23000", "wallets_identity_immutable")
	})
	t.Run("no delete", func(t *testing.T) {
		app := newWorld(t, "wallet_app")
		walletID, _ := app.wallet(0)
		wantViolation(t, app.exec(`DELETE FROM wallet.wallets WHERE id = $1`, walletID), "42501", "")
		owner := newWorld(t, "wallet_migrator")
		ownerWallet, _ := owner.wallet(0)
		wantViolation(t, owner.exec(`DELETE FROM wallet.wallets WHERE id = $1`, ownerWallet), "23000", "wallets_no_delete")
	})
}

func TestLedgerProtections(t *testing.T) {
	setup := func(t *testing.T, role string) (world, string, string, string) {
		w := newWorld(t, role)
		walletID, playerID := w.wallet(10000)
		bet := w.processedBet(walletID, playerID, 2500, 10000, 2)
		return w, walletID, playerID, bet
	}

	t.Run("app cannot update, delete or truncate", func(t *testing.T) {
		for _, sql := range []string{
			`UPDATE wallet.ledger_entries SET amount = 1`,
			`DELETE FROM wallet.ledger_entries`,
			`TRUNCATE wallet.ledger_entries`,
		} {
			w, _, _, _ := setup(t, "wallet_app")
			wantViolation(t, w.exec(sql), "42501", "")
		}
	})
	t.Run("owner cannot update, delete or truncate either", func(t *testing.T) {
		for _, sql := range []string{
			`UPDATE wallet.ledger_entries SET created_at = now()`,
			`DELETE FROM wallet.ledger_entries`,
		} {
			w, _, _, _ := setup(t, "wallet_migrator")
			wantViolation(t, w.exec(sql), "23000", "ledger_entries_append_only")
		}
		// TRUNCATE numa transação limpa: com eventos adiados pendentes o PostgreSQL
		// recusaria por outro motivo (55006) e o teste não provaria o trigger.
		wantViolation(t, newWorld(t, "wallet_migrator").exec(`TRUNCATE wallet.ledger_entries`), "23000", "ledger_entries_append_only")
	})
	t.Run("equation", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(10000)
		bet := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "BET", Amount: 2500, Result: ptr64(7500)})
		wantViolation(t, w.entry(walletID, bet, "DEBIT", 2500, 10000, 8000, 2), "23514", "ledger_equation")
	})
	t.Run("chain: balance_before must be the previous balance_after", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(10000)
		bet := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "BET", Amount: 2500, Result: ptr64(97500)})
		wantViolation(t, w.entry(walletID, bet, "DEBIT", 2500, 100000, 97500, 2), "23000", "ledger_chain")
	})
	t.Run("chain: versions are consecutive", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(10000)
		bet := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "BET", Amount: 2500, Result: ptr64(7500)})
		wantViolation(t, w.entry(walletID, bet, "DEBIT", 2500, 10000, 7500, 5), "23000", "ledger_chain")
	})
	t.Run("only processed transactions", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(10000)
		rejected := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "BET", Amount: 2500, Status: "REJECTED", Failure: "INSUFFICIENT_FUNDS"})
		wantViolation(t, w.entry(walletID, rejected, "DEBIT", 2500, 10000, 7500, 2), "23000", "ledger_requires_processed")
	})
	t.Run("direction follows the kind", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(10000)
		bet := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "BET", Amount: 2500, Result: ptr64(12500)})
		wantViolation(t, w.entry(walletID, bet, "CREDIT", 2500, 10000, 12500, 2), "23000", "ledger_direction_matches_kind")
	})
	t.Run("amount equals the transaction", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(10000)
		bet := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "BET", Amount: 2500, Result: ptr64(9000)})
		wantViolation(t, w.entry(walletID, bet, "DEBIT", 1000, 10000, 9000, 2), "23000", "ledger_matches_transaction")
	})
	t.Run("one entry per transaction", func(t *testing.T) {
		w, walletID, _, bet := setup(t, "wallet_app")
		wantViolation(t, w.entry(walletID, bet, "DEBIT", 2500, 7500, 5000, 3), "23505", "ledger_wallet_transaction_unique")
	})
	t.Run("entry without balance update", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(10000)
		bet := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "BET", Amount: 2500, Result: ptr64(7500)})
		if err := w.entry(walletID, bet, "DEBIT", 2500, 10000, 7500, 2); err != nil {
			t.Fatal(err)
		}
		wantViolation(t, w.commitChecks(), "23000", "wallet_matches_ledger")
	})
}

func TestTransactionProtections(t *testing.T) {
	t.Run("idempotency key unique per provider", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(0)
		w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "LOSS", Key: "k-1", Result: ptr64(0)})
		_, err := w.insertOp(op{Wallet: walletID, Player: playerID, Kind: "LOSS", Key: "k-1", Result: ptr64(0)})
		wantViolation(t, err, "23505", "tx_provider_idempotency_key_unique")
	})
	t.Run("same key for another provider is independent", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(0)
		w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "LOSS", Key: "k-1", Result: ptr64(0)})
		w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "LOSS", Key: "k-1", Provider: "provider-b", Result: ptr64(0)})
	})
	t.Run("external id cannot be reused with another key", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(0)
		w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "LOSS", ExtID: "tx-1", Key: "a", Result: ptr64(0)})
		_, err := w.insertOp(op{Wallet: walletID, Player: playerID, Kind: "LOSS", ExtID: "tx-1", Key: "b", Result: ptr64(0)})
		wantViolation(t, err, "23505", "tx_provider_external_id_unique")
	})
	t.Run("one opening per wallet", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(10000)
		err := w.exec(`INSERT INTO wallet.wager_transactions (id, origin, kind, status, wallet_id, player_id, currency, amount,
			result_balance_amount, result_balance_currency, correlation_id, created_at, updated_at)
			VALUES ($1, 'INTERNAL', 'OPENING', 'PROCESSED', $2, $3, 'BRL', 100, 100, 'BRL', 'test', now(), now())`, uuid(), walletID, playerID)
		wantViolation(t, err, "23505", "tx_one_opening_per_wallet")
	})
	t.Run("one successful reversal per transaction", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(10000)
		bet := w.processedBet(walletID, playerID, 2500, 10000, 2)
		ref := "ext-" + bet[:8]
		refund := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "REFUND", Amount: 2500, RefExtID: ref, RefTxID: bet, Result: ptr64(10000)})
		if err := w.entry(walletID, refund, "CREDIT", 2500, 7500, 10000, 3); err != nil {
			t.Fatal(err)
		}
		_, err := w.insertOp(op{Wallet: walletID, Player: playerID, Kind: "ROLLBACK", Amount: 2500, RefExtID: ref, RefTxID: bet, Result: ptr64(12500)})
		wantViolation(t, err, "23505", "tx_one_successful_reversal")
	})
	t.Run("rejected reversal does not consume the reference", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(10000)
		bet := w.processedBet(walletID, playerID, 2500, 10000, 2)
		ref := "ext-" + bet[:8]
		w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "ROLLBACK", Amount: 2500, RefExtID: ref, RefTxID: bet,
			Status: "REJECTED", Failure: "REFERENCE_AMOUNT_MISMATCH", Result: ptr64(7500)})
		w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "REFUND", Amount: 2500, RefExtID: ref, RefTxID: bet, Result: ptr64(10000)})
	})
	t.Run("terminal state is immutable", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(10000)
		bet := w.processedBet(walletID, playerID, 2500, 10000, 2)
		err := w.exec(`UPDATE wallet.wager_transactions SET status = 'REJECTED', failure_code = 'INSUFFICIENT_FUNDS' WHERE id = $1`, bet)
		wantViolation(t, err, "23000", "tx_terminal_immutable")
	})
	t.Run("financial data immutable: app lacks the column privilege", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(0)
		pending := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "REFUND", Amount: 100, Status: "PENDING_REFERENCE", RefExtID: "x"})
		wantViolation(t, w.exec(`UPDATE wallet.wager_transactions SET amount = 1 WHERE id = $1`, pending), "42501", "")
	})
	t.Run("financial data immutable: owner blocked by trigger", func(t *testing.T) {
		w := newWorld(t, "wallet_migrator")
		walletID, playerID := w.wallet(0)
		pending := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "REFUND", Amount: 100, Status: "PENDING_REFERENCE", RefExtID: "x"})
		wantViolation(t, w.exec(`UPDATE wallet.wager_transactions SET amount = 1 WHERE id = $1`, pending), "23000", "tx_identity_immutable")
	})
	t.Run("pending reference cannot go back to pending", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(0)
		pending := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "REFUND", Amount: 100, Status: "PENDING_REFERENCE", RefExtID: "x"})
		wantViolation(t, w.exec(`UPDATE wallet.wager_transactions SET status = 'PENDING' WHERE id = $1`, pending), "23000", "tx_status_transition")
	})
	t.Run("no delete", func(t *testing.T) {
		w := newWorld(t, "wallet_migrator")
		walletID, playerID := w.wallet(0)
		id := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "LOSS", Result: ptr64(0)})
		wantViolation(t, w.exec(`DELETE FROM wallet.wager_transactions WHERE id = $1`, id), "23000", "wager_transactions_no_delete")
	})

	checks := []struct {
		name, constraint string
		o                op
	}{
		{"LOSS must be zero", "tx_amount_policy", op{Kind: "LOSS", Amount: 1, Result: ptr64(0)}},
		{"BET must be positive", "tx_amount_policy", op{Kind: "BET", Amount: 0, Status: "REJECTED", Failure: "INSUFFICIENT_FUNDS"}},
		{"BET cannot have a reference", "tx_reference_policy", op{Kind: "BET", Amount: 1, RefExtID: "x", Status: "REJECTED", Failure: "INSUFFICIENT_FUNDS"}},
		{"REFUND requires a reference", "tx_reference_policy", op{Kind: "REFUND", Amount: 1, Status: "REJECTED", Failure: "REFERENCE_NOT_FOUND"}},
		{"rejected requires a failure code", "tx_failure_code_presence", op{Kind: "BET", Amount: 1, Status: "REJECTED"}},
		{"rejected cannot use a FAILED code", "tx_failure_code_valid", op{Kind: "BET", Amount: 1, Status: "REJECTED", Failure: "INTERNAL_PROCESSING_FAILED"}},
		{"processed requires result balance", "tx_processed_has_result", op{Kind: "LOSS", Amount: 0}},
		{"external metadata format", "tx_external_metadata", op{Kind: "LOSS", Amount: 0, Provider: "bad provider", Result: ptr64(0)}},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, "wallet_app")
			walletID, playerID := w.wallet(0)
			c.o.Wallet, c.o.Player = walletID, playerID
			_, err := w.insertOp(c.o)
			wantViolation(t, err, "23514", c.constraint)
		})
	}

	t.Run("OPENING cannot be external", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(0)
		_, err := w.insertOp(op{Wallet: walletID, Player: playerID, Kind: "OPENING", Amount: 100, Result: ptr64(100)})
		wantViolation(t, err, "23514", "tx_opening_is_internal")
	})
	t.Run("external operation without provider (NULL must not pass the CHECK)", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(0)
		err := w.exec(`INSERT INTO wallet.wager_transactions (id, origin, kind, status, wallet_id, player_id, currency, amount,
			provider_id, external_transaction_id, idempotency_key, request_hash, request_hash_version, round_id, game_id,
			result_balance_amount, result_balance_currency, correlation_id, created_at, updated_at)
			VALUES ($1, 'EXTERNAL', 'LOSS', 'PROCESSED', $2, $3, 'BRL', 0, NULL, 'x', 'k', repeat('a', 64), 1, 'r', 'g',
			0, 'BRL', 'test', now(), now())`, uuid(), walletID, playerID)
		wantViolation(t, err, "23514", "tx_external_metadata")
	})
	t.Run("processed operation needs its ledger entry", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(10000)
		w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "BET", Amount: 2500, Result: ptr64(7500)})
		wantViolation(t, w.commitChecks(), "23000", "tx_processed_has_ledger_entry")
	})
	t.Run("processed operation matches the wallet currency", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(0)
		w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "LOSS", Amount: 0, Currency: "USD", Result: ptr64(0)})
		wantViolation(t, w.commitChecks(), "23000", "tx_processed_matches_wallet")
	})
	t.Run("processed reversal must reference the same wallet", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletA, playerA := w.wallet(10000)
		bet := w.processedBet(walletA, playerA, 2500, 10000, 2)
		walletB, playerB := w.wallet(0)
		refund := w.mustOp(op{Wallet: walletB, Player: playerB, Kind: "REFUND", Amount: 2500,
			RefExtID: "ext-" + bet[:8], RefTxID: bet, Result: ptr64(2500)})
		if err := w.entry(walletB, refund, "CREDIT", 2500, 0, 2500, 2); err != nil {
			t.Fatal(err)
		}
		if err := w.setBalance(walletB, 2500, 2); err != nil {
			t.Fatal(err)
		}
		wantViolation(t, w.commitChecks(), "23000", "tx_processed_reference_valid")
	})
}

func TestInboxAndOutboxProtections(t *testing.T) {
	insertEvent := func(w world, id string) {
		w.mustExec(`INSERT INTO wallet.outbox_events (event_id, aggregate_type, aggregate_id, partition_key, event_type,
			event_version, payload, occurred_at) VALUES ($1, 'Wallet', $2, $2, 'WalletBalanceChanged', 1, '{"a":1}', now())`, id, uuid())
	}
	t.Run("inbox: one message per consumer", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		walletID, playerID := w.wallet(0)
		tx := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "LOSS", Result: ptr64(0)})
		insert := `INSERT INTO wallet.inbox_messages VALUES ('wager-consumer', 'msg-1', repeat('b', 64), $1, now(), now())`
		w.mustExec(insert, tx)
		wantViolation(t, w.exec(insert, tx), "23505", "inbox_messages_pkey")
	})
	t.Run("inbox is append-only", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		wantViolation(t, w.exec(`UPDATE wallet.inbox_messages SET message_hash = repeat('c', 64)`), "42501", "")
		wantViolation(t, newWorld(t, "wallet_app").exec(`DELETE FROM wallet.inbox_messages`), "42501", "")
	})
	t.Run("outbox payload is immutable", func(t *testing.T) {
		app := newWorld(t, "wallet_app")
		id := uuid()
		insertEvent(app, id)
		wantViolation(t, app.exec(`UPDATE wallet.outbox_events SET payload = '{"a":2}' WHERE event_id = $1`, id), "42501", "")
		owner := newWorld(t, "wallet_migrator")
		insertEvent(owner, id)
		wantViolation(t, owner.exec(`UPDATE wallet.outbox_events SET payload = '{"a":2}' WHERE event_id = $1`, id), "23000", "outbox_event_immutable")
	})
	t.Run("outbox publication is final", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		id := uuid()
		insertEvent(w, id)
		w.mustExec(`UPDATE wallet.outbox_events SET published_at = now() WHERE event_id = $1`, id)
		wantViolation(t, w.exec(`UPDATE wallet.outbox_events SET published_at = NULL WHERE event_id = $1`, id), "23000", "outbox_published_final")
	})
	t.Run("outbox events are not deleted", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		wantViolation(t, w.exec(`DELETE FROM wallet.outbox_events`), "42501", "")
	})
}

// A aplicação não chama funções internas nem lê o controle de migrations.
func TestAppHasNoExtraPrivileges(t *testing.T) {
	w := newWorld(t, "wallet_app")
	var executable int
	err := w.tx.QueryRow(t.Context(), `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'wallet' AND has_function_privilege('wallet_app', p.oid, 'EXECUTE')`).Scan(&executable)
	if err != nil || executable != 0 {
		t.Errorf("wallet_app can execute %d functions in schema wallet (%v)", executable, err)
	}
	wantViolation(t, newWorld(t, "wallet_app").exec(`SELECT * FROM wallet.schema_migrations`), "42501", "")
}
