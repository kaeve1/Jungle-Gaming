//go:build integration

package integration

import "testing"

// Transações são revertidas pela fixture, inclusive quando revelam uma falha.
func TestCodexReviewSchemaRejectsInvalidHistory(t *testing.T) {
	t.Run("wrong replay balance", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		wid, pid := w.wallet(10000)
		id := w.mustOp(op{Wallet: wid, Player: pid, Kind: "BET", Amount: 2500, Result: ptr64(9900)})
		if err := w.entry(wid, id, "DEBIT", 2500, 10000, 7500, 2); err != nil {
			t.Fatal(err)
		}
		if err := w.setBalance(wid, 7500, 2); err != nil {
			t.Fatal(err)
		}
		wantViolation(t, w.commitChecks(), "23000", "tx_result_matches_ledger")
	})
	t.Run("refund different round", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		wid, pid := w.wallet(10000)
		bet := w.processedBet(wid, pid, 2500, 10000, 2)
		id := uuid()
		w.mustExec(`INSERT INTO wallet.wager_transactions (id, origin, kind, status, wallet_id, player_id, currency, amount,
		 provider_id, external_transaction_id, idempotency_key, request_hash, request_hash_version, round_id, game_id,
		 reference_external_transaction_id, reference_transaction_id, result_balance_amount, result_balance_currency,
		 correlation_id, created_at, updated_at)
		 VALUES ($1,'EXTERNAL','REFUND','PROCESSED',$2,$3,'BRL',2500,'provider-a',$4,$4,repeat('a',64),1,'another-round','game-1',
		 $5,$6,10000,'BRL','review',now(),now())`, id, wid, pid, "review-"+id, "ext-"+bet[:8], bet)
		if err := w.entry(wid, id, "CREDIT", 2500, 7500, 10000, 3); err != nil {
			t.Fatal(err)
		}
		if err := w.setBalance(wid, 10000, 3); err != nil {
			t.Fatal(err)
		}
		wantViolation(t, w.commitChecks(), "23000", "tx_reference_round_matches")
	})
	t.Run("late opening", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		wid, pid := w.wallet(0)
		win := w.mustOp(op{Wallet: wid, Player: pid, Kind: "WIN", Amount: 1000, Result: ptr64(1000)})
		if err := w.entry(wid, win, "CREDIT", 1000, 0, 1000, 2); err != nil {
			t.Fatal(err)
		}
		if err := w.setBalance(wid, 1000, 2); err != nil {
			t.Fatal(err)
		}
		id := uuid()
		w.mustExec(`INSERT INTO wallet.wager_transactions (id, origin, kind, status, wallet_id, player_id, currency, amount,
		 result_balance_amount,result_balance_currency,correlation_id,created_at,updated_at)
		 VALUES ($1,'INTERNAL','OPENING','PROCESSED',$2,$3,'BRL',500,1500,'BRL','review',now(),now())`, id, wid, pid)
		if err := w.entry(wid, id, "CREDIT", 500, 1000, 1500, 3); err != nil {
			t.Fatal(err)
		}
		if err := w.setBalance(wid, 1500, 3); err != nil {
			t.Fatal(err)
		}
		wantViolation(t, w.commitChecks(), "23000", "opening_at_creation_only")
	})
	t.Run("external credit cannot replace opening", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		wid, pid := uuid(), uuid()
		w.mustExec(`INSERT INTO wallet.wallets (id,player_id,currency,balance_amount,version,created_at,updated_at)
		 VALUES ($1,$2,'BRL',1000,1,now(),now())`, wid, pid)
		id := w.mustOp(op{Wallet: wid, Player: pid, Kind: "WIN", Amount: 1000, Result: ptr64(1000)})
		if err := w.entry(wid, id, "CREDIT", 1000, 0, 1000, 1); err != nil {
			t.Fatal(err)
		}
		wantViolation(t, w.commitChecks(), "23000", "ledger_first_version_opening")
	})
	t.Run("app cannot forge creation metadata", func(t *testing.T) {
		w := newWorld(t, "wallet_app")
		wid, _ := w.wallet(0)
		wantViolation(t, w.exec(`UPDATE wallet.wallets SET opening_amount=1000 WHERE id=$1`, wid), "42501", "")
	})
}
