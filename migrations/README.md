# Migrations

Os pares `*.up.sql` e `*.down.sql` são ordenados por versão. `embed.go` incorpora esses arquivos ao binário; mantenha-os nesta pasta.

Execute na raiz:

```sh
docker compose run --rm migrate version
docker compose run --rm migrate up
```

Reversões `down` podem apagar dados. O runtime usa o papel de aplicação; a execução de migrations usa o papel separado de migração. Detalhes em [README](../README.md) e [arquitetura](../ARCHITECTURE.md).
