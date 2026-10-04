# Estrutura do repositório

| Caminho | Responsabilidade |
| --- | --- |
| `cmd/service/` | Entrada do serviço e composição da execução |
| `cmd/migrate/` | Entrada do comando de migrations |
| `internal/domain/` | Dinheiro exato, entidades, estados e regras financeiras |
| `internal/application/` | Casos de uso, portas e coordenação das transações |
| `internal/adapters/` | HTTP, autenticação, PostgreSQL, SQS, workers e diagnóstico |
| `internal/bootstrap/` | Composição Uber Fx e lifecycle |
| `internal/config/` | Leitura e validação da configuração |
| `internal/dbmigrate/` | Execução das migrations |
| `internal/archtest/` | Testes das fronteiras entre camadas |
| `migrations/` | SQL de subida/descida e incorporação ao binário |
| `deploy/` | Provisionamento local de PostgreSQL, Keycloak e SQS |
| `scripts/` | Verificação, demonstração e supervisão de testes |
| `tests/integration/` | Integração real e cenários distribuídos |
| `tests/reviewer/` | Probes externos HTTP e operacionais |
| `docs/` | Documentação técnica e requisitos |

Na raiz ficam `README.md`, `ARCHITECTURE.md`, `compose.yaml`, `Dockerfile`, `go.mod`, `go.sum`, `.env.example` e arquivos de configuração do repositório. Esses caminhos são usados pelos comandos e pelo enunciado.

Os testes unitários permanecem junto dos pacotes Go. `migrations/embed.go` precisa permanecer junto dos SQL incorporados. Não mover essas fontes só para agrupar extensões.

Os scripts podem gerar novas pastas `tests/.stage11-*/` durante a execução. São artefatos temporários ignorados pelo Git e pelo contexto Docker. A validação limpa grava fontes e evidências em uma pasta irmã `jungle-gaming-validation-<id>/`; essas provas anteriores foram preservadas no local original.

