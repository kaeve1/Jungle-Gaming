//go:build integration

package integration

import (
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Estes testes provam que o BANCO, sozinho, impede atualização perdida e
// reversão dupla entre escritores concorrentes — mesmo que a aplicação esqueça
// o lock da carteira (enunciado §5.3: invariantes no banco, independentes de
// locks locais). A aplicação usará SELECT ... FOR UPDATE (etapa 5); aqui o lock
// é omitido de propósito.
//
// Rodam num banco descartável porque precisam de COMMIT.

// race executa as duas funções em transações separadas, liberando os COMMITs ao
// mesmo tempo depois que ambas fizeram suas gravações. Devolve o erro de cada uma
// (da gravação ou do COMMIT).
func race(t *testing.T, a, b world, writeA, writeB func(world) error) (errA, errB error) {
	t.Helper()
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	run := func(w world, write func(world) error, out *error) {
		defer done.Done()
		err := write(w)
		ready.Done()
		<-start
		if err == nil {
			err = w.commit()
		}
		*out = err
	}
	done.Add(2)
	// A gravação de A termina antes de B começar; B então disputa as mesmas
	// linhas e fica bloqueado no banco até A confirmar. Só A é aguardado aqui:
	// B não consegue sinalizar enquanto está bloqueado.
	ready.Add(1)
	go run(a, writeA, &errA)
	ready.Wait()
	ready.Add(1) // B sinaliza depois; ninguém espera por isso
	go run(b, writeB, &errB)
	close(start)
	done.Wait()
	return errA, errB
}

// Duas transações leem o mesmo saldo (100.00, versão 1) e cada uma debita 80.00
// sem lock. Sem proteção no banco, as duas confirmariam e uma atualização seria
// perdida (saldo 20.00 com dois débitos). Com as proteções, a segunda falha.
func TestDatabasePreventsLostUpdateWithoutAppLock(t *testing.T) {
	db := scratchDatabase(t, true)
	setup := newWorldIn(t, "wallet_app", db)
	walletID, playerID := setup.wallet(10000)
	if err := setup.commit(); err != nil {
		t.Fatal(err)
	}

	debit := func(w world) error {
		// Ambas "leram" saldo 10000 na versão 1 e calculam o mesmo resultado.
		after := int64(2000)
		id, err := w.insertOp(op{Wallet: walletID, Player: playerID, Kind: "BET", Amount: 8000, Result: &after})
		if err != nil {
			return err
		}
		if err := w.entry(walletID, id, "DEBIT", 8000, 10000, 2000, 2); err != nil {
			return err
		}
		return w.exec(`UPDATE wallet.wallets SET balance_amount = 2000, version = 2, updated_at = now() WHERE id = $1`, walletID)
	}
	errA, errB := race(t, newWorldIn(t, "wallet_app", db), newWorldIn(t, "wallet_app", db), debit, debit)

	if (errA == nil) == (errB == nil) {
		t.Fatalf("exactly one writer must succeed: errA=%v errB=%v", errA, errB)
	}
	loser := errA
	if loser == nil {
		loser = errB
	}
	// O perdedor é barrado pelo ledger. Qual regra dispara depende do momento:
	// se o lançamento do vencedor já estava confirmado, o encadeamento (versão 3
	// esperada); se ainda não, o índice único espera o COMMIT e recusa a versão 2.
	var pgErr *pgconn.PgError
	if !errors.As(loser, &pgErr) ||
		!((pgErr.Code == "23000" && pgErr.ConstraintName == "ledger_chain") ||
			(pgErr.Code == "23505" && pgErr.ConstraintName == "ledger_wallet_version_unique")) {
		t.Fatalf("loser must be stopped by the ledger chain or version uniqueness, got %v", loser)
	}

	check := newWorldIn(t, "wallet_app", db)
	var balance, version, debits int64
	if err := check.tx.QueryRow(t.Context(), `SELECT w.balance_amount, w.version,
		(SELECT count(*) FROM wallet.ledger_entries WHERE wallet_id = w.id AND direction = 'DEBIT')
		FROM wallet.wallets w WHERE id = $1`, walletID).Scan(&balance, &version, &debits); err != nil {
		t.Fatal(err)
	}
	if balance != 2000 || version != 2 || debits != 1 {
		t.Errorf("balance %d, version %d, debits %d; want 2000, 2, 1", balance, version, debits)
	}
}

// Mesmo cenário, mas o perdedor tenta "pular" o ledger e só atualizar o saldo
// sobre a versão já confirmada pelo vencedor: o trigger de versão recusa.
func TestDatabaseRejectsBlindBalanceOverwrite(t *testing.T) {
	db := scratchDatabase(t, true)
	setup := newWorldIn(t, "wallet_app", db)
	walletID, playerID := setup.wallet(10000)
	if err := setup.commit(); err != nil {
		t.Fatal(err)
	}
	winner := newWorldIn(t, "wallet_app", db)
	winner.processedBet(walletID, playerID, 8000, 10000, 2)
	if err := winner.commit(); err != nil {
		t.Fatal(err)
	}
	// Escritor com dados antigos: leu saldo 100.00 na versão 1 e debita 30.00,
	// gravando 70.00 na versão 2 por cima do 20.00 já confirmado.
	stale := newWorldIn(t, "wallet_app", db)
	err := stale.exec(`UPDATE wallet.wallets SET balance_amount = 7000, version = 2 WHERE id = $1`, walletID)
	wantViolation(t, err, "23000", "wallets_version_rule")
}

// Um REFUND e um ROLLBACK da mesma BET, em transações concorrentes: só uma
// reversão pode ser confirmada (índice único parcial), então o débito nunca é
// devolvido duas vezes.
func TestDatabasePreventsConcurrentDoubleReversal(t *testing.T) {
	db := scratchDatabase(t, true)
	setup := newWorldIn(t, "wallet_app", db)
	walletID, playerID := setup.wallet(10000)
	bet := setup.processedBet(walletID, playerID, 2500, 10000, 2)
	if err := setup.commit(); err != nil {
		t.Fatal(err)
	}
	ref := "ext-" + bet[:8]

	// Cada reversão é completa e coerente (operação, crédito no ledger, saldo):
	// sozinha, qualquer uma delas seria confirmada.
	reverse := func(kind string) func(world) error {
		return func(w world) error {
			id, err := w.insertOp(op{Wallet: walletID, Player: playerID, Kind: kind, Amount: 2500,
				RefExtID: ref, RefTxID: bet, Result: ptr64(10000)})
			if err != nil {
				return err
			}
			if err := w.entry(walletID, id, "CREDIT", 2500, 7500, 10000, 3); err != nil {
				return err
			}
			return w.setBalance(walletID, 10000, 3)
		}
	}
	errA, errB := race(t, newWorldIn(t, "wallet_app", db), newWorldIn(t, "wallet_app", db), reverse("REFUND"), reverse("ROLLBACK"))
	if errA != nil {
		t.Fatalf("first reversal must commit: %v", errA)
	}
	// O segundo espera no índice único até o COMMIT do primeiro e então é recusado.
	wantViolation(t, errB, "23505", "tx_one_successful_reversal")

	check := newWorldIn(t, "wallet_app", db)
	var balance, credits int64
	if err := check.tx.QueryRow(t.Context(), `SELECT w.balance_amount,
		(SELECT count(*) FROM wallet.ledger_entries WHERE wallet_id = w.id AND direction = 'CREDIT')
		FROM wallet.wallets w WHERE id = $1`, walletID).Scan(&balance, &credits); err != nil {
		t.Fatal(err)
	}
	// Abertura + uma única devolução.
	if balance != 10000 || credits != 2 {
		t.Errorf("balance %d, credits %d; want 10000 and 2 (opening + one reversal)", balance, credits)
	}
}
