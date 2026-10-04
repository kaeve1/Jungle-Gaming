# Probes externos de avaliação

Execute a seção 8 do [roteiro de avaliação](../../docs/ROTEIRO-DE-AVALIACAO.md). Ela prepara uma cópia isolada com três containers reais, identifica as portas e fornece os comandos Python e Go.

- `blackbox.py`: sete grupos HTTP, autorização, entradas adversariais, reversões, concorrência e modelo independente de dinheiro inteiro.
- `operations.py`: cinco grupos operacionais, SQL, reinício, identidades, readiness e SIGKILL do broker. Purga apenas a saída do ambiente isolado; não usar em desenvolvimento ou dados necessários.
- `exercise_examples.py`: exemplo literal1000.00 → 975.00, reconciliação, paginação e replay original; comandos no [relatório com .env.example](../../docs/REAVALIACAO-ENV-EXAMPLE.md).
- `domain_probe_test.go`: regressão do parsing direto de Money com conteúdo extra; tag reviewer.

Python 3 com biblioteca padrão e Docker são suficientes. Os nomes dos três containers são exigidos pelo probe operacional. `operations.py --env-file <arquivo>` escolhe o ambiente Compose para o probe de readiness; o padrão é `tests/validation.env`. Resultados e limitações em [VALIDACAO-FINAL](../../docs/VALIDACAO-FINAL.md).
