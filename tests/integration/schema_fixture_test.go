//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// world é uma transação SQL de teste. Tudo é desfeito no fim (Rollback): os
// testes de schema não deixam dados no banco de desenvolvimento.
type world struct {
	t  *testing.T
	tx pgx.Tx
}

// newWorld abre uma transação como o usuário indicado ("wallet_app" ou "wallet_migrator").
func newWorld(t *testing.T, role string) world {
	t.Helper()
	return newWorldIn(t, role, env(t, "TEST_PG_DB"))
}

// newWorldIn abre a transação num banco específico (ex.: um scratchDatabase).
func newWorldIn(t *testing.T, role, db string) world {
	t.Helper()
	passwordVar := map[string]string{"wallet_app": "TEST_PG_APP_PASSWORD", "wallet_migrator": "TEST_PG_MIGRATOR_PASSWORD"}[role]
	conn := connect(t, role, passwordVar, db)
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return world{t: t, tx: tx}
}

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (w world) exec(sql string, args ...any) error {
	_, err := w.tx.Exec(w.t.Context(), sql, args...)
	return err
}

func (w world) mustExec(sql string, args ...any) {
	w.t.Helper()
	if err := w.exec(sql, args...); err != nil {
		w.t.Fatalf("fixture failed: %v\nSQL: %s", err, sql)
	}
}

// commitChecks executa agora as verificações adiadas para o COMMIT.
func (w world) commitChecks() error { return w.exec("SET CONSTRAINTS ALL IMMEDIATE") }

// walletRow cria uma carteira BRL coerente: com saldo positivo, inclui a
// abertura OPENING e o lançamento de crédito na versão 1.
func (w world) wallet(balance int64) (walletID, playerID string) {
	w.t.Helper()
	walletID, playerID = uuid(), uuid()
	w.mustExec(`INSERT INTO wallet.wallets (id, player_id, currency, balance_amount, version, created_at, updated_at)
		VALUES ($1, $2, 'BRL', $3, 1, now(), now())`, walletID, playerID, balance)
	if balance > 0 {
		opening := uuid()
		w.mustExec(`INSERT INTO wallet.wager_transactions (id, origin, kind, status, wallet_id, player_id, currency, amount,
			result_balance_amount, result_balance_currency, correlation_id, created_at, updated_at)
			VALUES ($1, 'INTERNAL', 'OPENING', 'PROCESSED', $2, $3, 'BRL', $4, $4, 'BRL', 'test', now(), now())`,
			opening, walletID, playerID, balance)
		w.mustExec(`INSERT INTO wallet.ledger_entries (id, wallet_id, currency, transaction_id, direction, amount,
			balance_before, balance_after, wallet_version, created_at)
			VALUES ($1, $2, 'BRL', $3, 'CREDIT', $4, 0, $4, 1, now())`, uuid(), walletID, opening, balance)
	}
	return walletID, playerID
}

// op descreve uma operação externa a inserir; campos vazios recebem padrões válidos.
type op struct {
	ID, Wallet, Player, Kind, Status, Currency string
	Amount                                     int64
	Provider, ExtID, Key                       string
	RefExtID, RefTxID, Failure                 string // vazio = NULL
	Result                                     *int64
}

func (w world) insertOp(o op) (string, error) {
	if o.ID == "" {
		o.ID = uuid()
	}
	if o.Currency == "" {
		o.Currency = "BRL"
	}
	if o.Provider == "" {
		o.Provider = "provider-a"
	}
	if o.ExtID == "" {
		o.ExtID = "ext-" + o.ID[:8]
	}
	if o.Key == "" {
		o.Key = o.Provider + ":" + o.ExtID
	}
	if o.Status == "" {
		o.Status = "PROCESSED"
	}
	var resultCurrency any
	if o.Result != nil {
		resultCurrency = o.Currency
	}
	nullIfEmpty := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	err := w.exec(`INSERT INTO wallet.wager_transactions (id, origin, kind, status, wallet_id, player_id, currency, amount,
		provider_id, external_transaction_id, idempotency_key, request_hash, request_hash_version, round_id, game_id,
		reference_external_transaction_id, reference_transaction_id, failure_code, result_balance_amount, result_balance_currency,
		next_reference_attempt_at, reference_expires_at, correlation_id, created_at, updated_at)
		VALUES ($1, 'EXTERNAL', $2, $3, $4, $5, $6, $7, $8, $9, $10, repeat('a', 64), 1, 'round-1', 'game-1',
		        $11, $12, $13, $14, $15,
		        CASE WHEN $3 = 'PENDING_REFERENCE' THEN now() END, CASE WHEN $3 = 'PENDING_REFERENCE' THEN now() + interval '10 minutes' END,
		        'test', now(), now())`,
		o.ID, o.Kind, o.Status, o.Wallet, o.Player, o.Currency, o.Amount, o.Provider, o.ExtID, o.Key,
		nullIfEmpty(o.RefExtID), nullIfEmpty(o.RefTxID), nullIfEmpty(o.Failure), o.Result, resultCurrency)
	return o.ID, err
}

func (w world) mustOp(o op) string {
	w.t.Helper()
	id, err := w.insertOp(o)
	if err != nil {
		w.t.Fatalf("fixture op %+v: %v", o, err)
	}
	return id
}

func (w world) entry(walletID, txID, direction string, amount, before, after, version int64) error {
	return w.exec(`INSERT INTO wallet.ledger_entries (id, wallet_id, currency, transaction_id, direction, amount,
		balance_before, balance_after, wallet_version, created_at) VALUES ($1, $2, 'BRL', $3, $4, $5, $6, $7, $8, now())`,
		uuid(), walletID, txID, direction, amount, before, after, version)
}

func (w world) setBalance(walletID string, balance, version int64) error {
	return w.exec(`UPDATE wallet.wallets SET balance_amount = $2, version = $3, updated_at = now() WHERE id = $1`,
		walletID, balance, version)
}

// processedBet aplica uma BET completa e coerente (operação, lançamento, saldo).
func (w world) processedBet(walletID, playerID string, amount, before, version int64) string {
	w.t.Helper()
	after := before - amount
	id := w.mustOp(op{Wallet: walletID, Player: playerID, Kind: "BET", Amount: amount, Result: &after})
	if err := w.entry(walletID, id, "DEBIT", amount, before, after, version); err != nil {
		w.t.Fatal(err)
	}
	if err := w.setBalance(walletID, after, version); err != nil {
		w.t.Fatal(err)
	}
	return id
}

func ptr64(v int64) *int64 { return &v }

// wantViolation exige erro com o SQLSTATE e, se informado, o nome da regra.
func wantViolation(t *testing.T, err error, state, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want SQLSTATE %s (%s), got %v", state, constraint, err)
	}
	if pgErr.Code != state || (constraint != "" && pgErr.ConstraintName != constraint) {
		t.Fatalf("want SQLSTATE %s constraint %q, got %s constraint %q: %s",
			state, constraint, pgErr.Code, pgErr.ConstraintName, strings.TrimSpace(pgErr.Message))
	}
}

// commit confirma a transação do mundo (só em bancos descartáveis).
func (w world) commit() error { return w.tx.Commit(w.t.Context()) }
