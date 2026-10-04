//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"jungle-gaming-backend/internal/dbmigrate"
)

// Ida e volta das migrations (enunciado §4: aplicação e reversão) num banco
// DESCARTÁVEL criado só para este teste e apagado no fim. O banco de
// desenvolvimento nunca é revertido por testes.
func TestMigrationsRoundTrip(t *testing.T) {
	ctx := t.Context()
	db := scratchDatabase(t, false)
	scratch := connect(t, "postgres", "TEST_PG_SUPERUSER_PASSWORD", db)

	dsn := fmt.Sprintf("postgres://wallet_migrator:%s@%s:5432/%s?sslmode=disable",
		url.QueryEscape(env(t, "TEST_PG_MIGRATOR_PASSWORD")), env(t, "TEST_PG_HOST"), db)
	m, err := dbmigrate.New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	objects := func() (tables, functions int) {
		t.Helper()
		err := scratch.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM pg_tables WHERE schemaname = 'wallet' AND tablename <> 'schema_migrations'),
			(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'wallet')`).
			Scan(&tables, &functions)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	version := func() uint {
		t.Helper()
		v, dirty, err := m.Version()
		if err != nil || dirty {
			t.Fatalf("version %d dirty=%v err=%v", v, dirty, err)
		}
		return v
	}

	if err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}
	tablesUp, functionsUp := objects()
	if version() != 3 || tablesUp != 5 || functionsUp == 0 {
		t.Fatalf("after up: version %d, %d tables, %d functions", version(), tablesUp, functionsUp)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("second up must be a no-op: %v", err)
	}

	if err := m.Down(3); err != nil {
		t.Fatalf("down: %v", err)
	}
	if tables, functions := objects(); version() != 0 || tables != 0 || functions != 0 {
		t.Fatalf("after down: version %d, %d tables, %d functions left", version(), tables, functions)
	}

	if err := m.Up(); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if tables, functions := objects(); version() != 3 || tables != tablesUp || functions != functionsUp {
		t.Fatalf("after up again: %d tables, %d functions (want %d, %d)", tables, functions, tablesUp, functionsUp)
	}
}

// A URL com senha nunca aparece em erros de conexão das migrations.
func TestIntegrityMigrationPreservesExistingHistory(t *testing.T) {
	db := scratchDatabase(t, true)
	m, err := dbmigrate.New(fmt.Sprintf("postgres://wallet_migrator:%s@%s:5432/%s?sslmode=disable",
		url.QueryEscape(env(t, "TEST_PG_MIGRATOR_PASSWORD")), env(t, "TEST_PG_HOST"), db))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Down(2); err != nil {
		t.Fatal(err)
	}
	old := newWorldIn(t, "wallet_app", db)
	wid, pid := old.wallet(1000)
	if err := old.commit(); err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	current := newWorldIn(t, "wallet_app", db)
	var balance int64
	if err := current.tx.QueryRow(t.Context(), `SELECT balance_amount FROM wallet.wallets WHERE id=$1`, wid).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 1000 {
		t.Fatal("migration changed existing balance")
	}
	current.processedBet(wid, pid, 250, 1000, 2)
	if err := current.commit(); err != nil {
		t.Fatal(err)
	}
}

// A URL com senha nunca aparece em erros de conexão das migrations.
func TestMigrationErrorsDoNotLeakPassword(t *testing.T) {
	const secret = "s3cr3t-that-must-not-leak"
	_, err := dbmigrate.New(fmt.Sprintf("postgres://wallet_migrator:%s@%s:5432/does_not_exist?sslmode=disable", secret, env(t, "TEST_PG_HOST")))
	if err == nil {
		t.Fatal("expected connection error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("password leaked in error: %v", err)
	}
}

// scratchDatabase cria um banco descartável com o mesmo preparo do script de
// inicialização (schema "wallet" do migrator) e, se migrated, aplica as
// migrations. É apagado no fim do teste. Usado por testes que precisam de
// COMMIT: o schema proíbe apagar dados, então eles não podem rodar no banco
// de desenvolvimento.
func scratchDatabase(t *testing.T, migrated bool) string {
	t.Helper()
	ctx := t.Context()
	admin := connect(t, "postgres", "TEST_PG_SUPERUSER_PASSWORD", "postgres")
	db := "wallet_scratch_" + strings.ToLower(rand.Text()[:10])
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s OWNER wallet_migrator`, db)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, db)); err != nil {
			t.Errorf("cleanup of %s failed: %v", db, err)
		}
	})
	setup := connect(t, "postgres", "TEST_PG_SUPERUSER_PASSWORD", db)
	for _, sql := range []string{
		`REVOKE ALL ON DATABASE ` + db + ` FROM PUBLIC`,
		`GRANT CONNECT ON DATABASE ` + db + ` TO wallet_app`,
		`REVOKE ALL ON SCHEMA public FROM PUBLIC`,
		`CREATE SCHEMA wallet AUTHORIZATION wallet_migrator`,
		`GRANT USAGE ON SCHEMA wallet TO wallet_app`,
	} {
		if _, err := setup.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	if migrated {
		m, err := dbmigrate.New(fmt.Sprintf("postgres://wallet_migrator:%s@%s:5432/%s?sslmode=disable",
			url.QueryEscape(env(t, "TEST_PG_MIGRATOR_PASSWORD")), env(t, "TEST_PG_HOST"), db))
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		if err := m.Up(); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
