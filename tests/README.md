# Testes de integração e avaliação

- `integration/`: testes com PostgreSQL, Keycloak e SQS; os cenários distribuídos incluem processos independentes e falhas reais.
- [reviewer/](reviewer/README.md): probes externos que complementam a suíte.
- `test.env`: configuração local isolada para os testes; credenciais de exemplo.

Os testes unitários ficam junto dos pacotes em `internal/`. Veja os [comandos no README](../README.md) e [o catálogo de testes](../docs/TESTES.md). Não execute duas suítes simultaneamente no mesmo projeto Compose.

