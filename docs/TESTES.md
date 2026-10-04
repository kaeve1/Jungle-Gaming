# Testes

O [roteiro de avaliação](ROTEIRO-DE-AVALIACAO.md) contém a preparação, todos os comandos e resultados esperados. A [conformidade](CONFORMIDADE.md) relaciona cada cláusula às fontes e aos testes. A [validação final](VALIDACAO-FINAL.md) registra a execução da versão entregue.

Unitários ficam junto dos pacotes; integração real fica em `tests/integration`; probes externos ficam em `tests/reviewer`. Sem tags, `go test ./...` não executa integração. A suíte completa usa `integration,distributed` e `-race` com supervisor para as pausas de dependências.

```powershell
docker compose run --rm --no-deps check
pwsh -NoProfile -File scripts/validate-clean.ps1
```

Para a versão integral não use `-SkipDistributed`. Preserve JSON e logs da execução. Complemento externo com três containers reais: seção 8 do roteiro. Testes e probes não são benchmark e não garantem ausência universal de bugs.
