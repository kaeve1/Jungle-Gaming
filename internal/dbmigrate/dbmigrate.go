// Package dbmigrate aplica as migrations embutidas (pacote migrations) com
// golang-migrate. Usado por cmd/migrate e pelos testes de integração.
package dbmigrate

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // driver pgx5://
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"jungle-gaming-backend/migrations"
)

// Migrator encapsula golang-migrate. A tabela de controle fica no schema da aplicação.
type Migrator struct{ m *migrate.Migrate }

// New conecta usando uma URL postgres:// ou postgresql://.
func New(dsn string) (*Migrator, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, errors.New("database URL must use the postgres:// scheme")
	}
	u.Scheme = "pgx5"
	q := u.Query()
	q.Set("search_path", "wallet")
	q.Set("x-migrations-table", "schema_migrations")
	u.RawQuery = q.Encode()

	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, u.String())
	if err != nil {
		// A mensagem do driver pode conter a URL; não a repetimos (tem senha).
		return nil, fmt.Errorf("connect for migrations: %w", redact(err, dsn))
	}
	return &Migrator{m: m}, nil
}

// Up aplica todas as migrations pendentes. "Nada a fazer" não é erro.
func (x *Migrator) Up() error { return ignoreNoChange(x.m.Up()) }

// Down reverte as n últimas migrations.
func (x *Migrator) Down(n int) error { return ignoreNoChange(x.m.Steps(-n)) }

// Force marca a versão como aplicada, sem executar SQL.
func (x *Migrator) Force(v int) error { return x.m.Force(v) }

// Version devolve a versão atual e se a última execução ficou incompleta.
func (x *Migrator) Version() (uint, bool, error) {
	v, dirty, err := x.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return v, dirty, err
}

// Close libera as conexões.
func (x *Migrator) Close() {
	_, _ = x.m.Close()
}

func ignoreNoChange(err error) error {
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}

// redact remove a senha da mensagem de erro do driver, que pode conter a URL.
func redact(err error, dsn string) error {
	u, perr := url.Parse(dsn)
	if perr != nil || u.User == nil {
		return err
	}
	if pw, ok := u.User.Password(); ok && pw != "" {
		return errors.New(strings.ReplaceAll(err.Error(), pw, "***"))
	}
	return err
}
