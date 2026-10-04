#!/bin/sh
# Verificação completa do código. Roda dentro do container Linux:
#   docker compose run --rm check
set -eu

step() { printf '\n== %s\n' "$1"; }

step "gofmt"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then echo "$unformatted"; exit 1; fi

step "go mod verify / tidy"
go mod verify
GOFLAGS= go mod tidy -diff

step "go vet (inclui testes de integração)"
go vet ./...
go vet -tags integration,distributed ./...

step "go test -race"
go test -race -count=1 ./...

step "govulncheck"
GOFLAGS= go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

printf '\nOK: todas as verificações passaram\n'
