# Scripts

Execute os comandos na raiz do projeto. Scripts PowerShell requerem `pwsh` 7; o check roda no container Linux.

| Script | Uso |
| --- | --- |
| `check.sh` | `docker compose run --rm check`: formatação, módulos, vet, testes unitários com race e govulncheck |
| `demo.ps1` | `pwsh -File scripts/demo.ps1`: demonstração autenticada; opções no script e README |
| `test-compose-config.ps1` | `pwsh -File scripts/test-compose-config.ps1`: verifica a configuração renderizada |
| `test-distributed.ps1` | `pwsh -File scripts/test-distributed.ps1`: supervisiona cenários distribuídos e pausas reais |
| `validate-clean.ps1` | `pwsh -File scripts/validate-clean.ps1`: cópia limpa, recursos isolados e validação completa |

`validate-clean.ps1 -EnvironmentCheckOnly` verifica isolamento das variáveis sem iniciar containers. O check normal não substitui a integração real. Pré-requisitos e comandos completos em [README](../README.md) e [catálogo de testes](../docs/TESTES.md).
