//go:build integration

// Package integration contém testes contra PostgreSQL, Keycloak e SQS reais,
// em containers (enunciado §13: integração real, sem mocks). Rodam com:
//
//	docker compose up -d
//	docker compose run --rm integration
//
// As variáveis TEST_* são definidas pelo serviço "integration" do compose.yaml.
// Se faltarem, o teste FALHA (não é pulado): um teste de integração pulado em
// silêncio pareceria aprovado sem ter provado nada.
package integration

import (
	"os"
	"testing"
)

func env(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("variável %s ausente: rode via `docker compose run --rm integration`", key)
	}
	return v
}
