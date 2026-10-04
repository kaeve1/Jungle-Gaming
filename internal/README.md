# Código interno

O domínio contém regras financeiras e usa a biblioteca padrão. A aplicação coordena casos de uso por portas. Adaptadores implementam persistência, transporte e dependências externas. Bootstrap monta o grafo Uber Fx e gerencia o lifecycle.

`config/` valida opções; `dbmigrate/` executa migrations; `archtest/` verifica fronteiras. Testes unitários ficam junto ao pacote que protegem.

Consulte [a arquitetura](../ARCHITECTURE.md) e [o mapa de pastas](../docs/ESTRUTURA.md).
