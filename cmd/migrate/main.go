// Command migrate aplica e reverte as migrations do banco com o papel dono do
// schema (wallet_migrator). Uso:
//
//	migrate up            aplica todas as pendentes
//	migrate down N        reverte as N últimas (N obrigatório: evita apagar tudo por engano)
//	migrate version       mostra a versão atual
//	migrate force V       marca a versão V como aplicada (recuperação manual após falha)
//
// A conexão vem de MIGRATE_DATABASE_URL (ex.: postgres://wallet_migrator:...@postgres:5432/wallet).
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"jungle-gaming-backend/internal/dbmigrate"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	dsn := os.Getenv("MIGRATE_DATABASE_URL")
	if dsn == "" {
		return errors.New("MIGRATE_DATABASE_URL is required")
	}
	if len(args) == 0 {
		return errors.New("usage: migrate up | down N | version | force V")
	}
	m, err := dbmigrate.New(dsn)
	if err != nil {
		return err
	}
	defer m.Close()

	switch args[0] {
	case "up":
		return m.Up()
	case "down", "force":
		if len(args) != 2 {
			return fmt.Errorf("%s requires a number", args[0])
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 || (args[0] == "down" && n == 0) {
			return fmt.Errorf("invalid number %q", args[1])
		}
		if args[0] == "down" {
			return m.Down(n)
		}
		return m.Force(n)
	case "version":
		v, dirty, err := m.Version()
		if err != nil {
			return err
		}
		fmt.Printf("version=%d dirty=%v\n", v, dirty)
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}
