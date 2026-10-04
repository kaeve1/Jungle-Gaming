// Package migrations embute os arquivos SQL versionados (golang-migrate:
// NNNNNN_nome.up.sql / .down.sql) no binário cmd/migrate e nos testes.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
