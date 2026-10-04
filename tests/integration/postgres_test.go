//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func connect(t *testing.T, user, passwordVar, db string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dsn := fmt.Sprintf("postgres://%s:%s@%s:5432/%s?sslmode=disable",
		user, url.QueryEscape(env(t, passwordVar)), env(t, "TEST_PG_HOST"), db)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as %s: %v", user, err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// O usuário da aplicação não tem poderes administrativos: não é superusuário,
// não ignora RLS, não cria objetos e não é dono de nada (ARCHITECTURE.md §10).
// É o que permitirá, na etapa 4, que triggers e REVOKE protejam o ledger
// mesmo contra a própria aplicação.
func TestAppRoleIsLimited(t *testing.T) {
	db := env(t, "TEST_PG_DB")
	conn := connect(t, "wallet_app", "TEST_PG_APP_PASSWORD", db)
	ctx := t.Context()

	var user, searchPath string
	var super, bypass, createRole, createDB bool
	err := conn.QueryRow(ctx, `SELECT current_user, current_setting('search_path'),
		rolsuper, rolbypassrls, rolcreaterole, rolcreatedb FROM pg_roles WHERE rolname = current_user`).
		Scan(&user, &searchPath, &super, &bypass, &createRole, &createDB)
	if err != nil {
		t.Fatal(err)
	}
	if user != "wallet_app" || searchPath != "wallet" {
		t.Errorf("user %q, search_path %q", user, searchPath)
	}
	if super || bypass || createRole || createDB {
		t.Errorf("wallet_app has administrative attributes: super=%v bypassrls=%v createrole=%v createdb=%v", super, bypass, createRole, createDB)
	}

	for _, ddl := range []string{
		"CREATE TABLE wallet.probe (id int)",
		"CREATE TABLE public.probe (id int)",
		"CREATE SCHEMA probe",
	} {
		_, err := conn.Exec(ctx, ddl)
		if sqlState(err) != "42501" { // insufficient_privilege
			t.Errorf("%s: want permission denied (42501), got %v", ddl, err)
		}
	}
}

// O dono do schema consegue criar objetos (as migrations rodam com ele).
func TestMigratorOwnsSchema(t *testing.T) {
	conn := connect(t, "wallet_migrator", "TEST_PG_MIGRATOR_PASSWORD", env(t, "TEST_PG_DB"))
	ctx := t.Context()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) // nada fica no banco
	if _, err := tx.Exec(ctx, "CREATE TABLE wallet.probe (id int)"); err != nil {
		t.Fatalf("migrator cannot create in schema wallet: %v", err)
	}
	var owner string
	if err := tx.QueryRow(ctx, "SELECT nspowner::regrole::text FROM pg_namespace WHERE nspname = 'wallet'").Scan(&owner); err != nil || owner != "wallet_migrator" {
		t.Errorf("schema owner %q, %v", owner, err)
	}
}

// Senha errada é recusada (credenciais efetivamente verificadas).
func TestWrongPasswordIsRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dsn := fmt.Sprintf("postgres://wallet_app:wrong@%s:5432/%s?sslmode=disable", env(t, "TEST_PG_HOST"), env(t, "TEST_PG_DB"))
	conn, err := pgx.Connect(ctx, dsn)
	if err == nil {
		conn.Close(ctx)
		t.Fatal("connection with wrong password succeeded")
	}
	if sqlState(err) != "28P01" { // invalid_password
		t.Errorf("want 28P01, got %v", err)
	}
}
