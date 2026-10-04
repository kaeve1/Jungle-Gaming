# Validação final — 2026-10-04

Validação mais recente: [reavaliação independente com `.env.example`](REAVALIACAO-ENV-EXAMPLE.md), com nova suíte integral e correção do probe operacional de readiness.

Material local pronto para avaliação e publicação pelo candidato. Esta pasta contém todas as fontes, testes, migrations, Compose, configuração de exemplo e documentação técnica. Não contém `.git`, `.env` real, credenciais geradas, caches, conversas de agentes ou resíduos de execuções antigas. O histórico de trabalho foi preservado separadamente; nenhuma publicação ou envio foi executado.

## Resultado executado

| Verificação | Resultado |
| --- | --- |
| Build/startup com fontes copiadas e recursos novos | PASS |
| API real, Keycloak, abertura, BET/WIN/LOSS, replay, ledger, reconciliação e SQS | PASS |
| Migrations | Versão 3, clean; reversão/reaplicação testadas em banco descartável |
| gofmt, módulos, tidy, vet normal/com tags | PASS |
| Unitários com detector de corridas | PASS |
| govulncheck | Nenhuma vulnerabilidade conhecida detectada nesta execução |
| Configuração Compose, isolamento de ambiente | PASS |
| Binário real nonroot e SIGTERM | PASS |
| Integração e distribuição com race | **80 testes principais PASS; 261,365s** |
| HTTP externo em três containers reais | **7/7 PASS** |
| Probes operacionais | **5/5 PASS** |
| Parser adversarial/fuzz | PASS, 15 segundos, dois workers |
| Regressão Money direta com race | PASS |
| Testes Python de assinatura/durabilidade do broker | **5/5 PASS** |

As duas entradas SKIP na suíte são os helpers `TestStage8CrashConsumerHelper` e `TestStage11ProcessHelper`: ficam desativados no processo pai e são executados como subprocessos pelos cenários de interrupção. Não representam cenários obrigatórios de recuperação ignorados.

Os probes externos conferiram autorização/isolamento, entradas inválidas, reversões, zero/overflow, 50 duplicatas e duas apostas concorrentes, referências e 80 operações contra um modelo inteiro independente. As verificações operacionais cobriram saldo versus ledger por SQL, reinício das três APIs, identidade da mensagem original, readiness de fila configurada e dois SIGKILL do broker. Mensagens aceitas, contador de recebimento e exclusão confirmada sobreviveram nos cenários testados.

A versão de trabalho também passou por outra execução completa nesta rodada, com integração/race em 271,757s. A execução acima partiu da própria pasta de entrega. Depois dela, somente documentação e evidências foram atualizadas; arquivos executáveis/configuração foram comparados por SHA256 com a fonte validada.

## Evidências e reprodução

- [Resultado estruturado](evidence/results.json).
- [HTTP externo](evidence/final-blackbox.json) e [operações externas](evidence/final-operations.json).
- [Check](evidence/check.log), [integração/distribuição](evidence/distributed.log) e [demonstração](evidence/demo.log).
- [Manifesto das fontes usadas no ensaio](evidence/source-sha256.json).
- [Fuzz](evidence/parser-fuzz.log), [regressão direta](evidence/reviewer-domain-race.log) e [testes do broker](evidence/broker-python-tests.log).
- [Conformidade integral](CONFORMIDADE.md) e [checklist estruturado](CONFORMIDADE.json).
- [Roteiro para repetir os testes](ROTEIRO-DE-AVALIACAO.md).

O manifesto do ensaio inclui os documentos da cópia no momento dos testes. O manifesto na raiz, `MANIFESTO-ENTREGA.json`, corresponde ao conteúdo final desta pasta; diferenças posteriores são documentais, não mudanças de runtime. A verificação de segredos reconhece apenas a chave deliberadamente inválida do teste de autenticação SQS; nenhum segredo real deve ser incluído.

A configuração Git da entrega permite os logs de `docs/evidence/` citados nesta documentação e preserva os bytes dos arquivos sem conversão automática de fim de linha. Isso mantém o manifesto SHA256 verificável após publicação e download do ZIP. Scripts shell já têm os finais de linha compatíveis com Linux usados nos testes.

## Correção desta rodada

O Docker estava desligado na primeira tentativa; testes da aplicação não começaram. O script de validação não preservava bem o resultado quando a limpeza também falhava. Foi acrescentado preflight do engine antes da criação da cópia, limpeza condicionada ao namespace criado, preservação do erro original e gravação dos resultados mesmo diante de falha de cleanup. O teste com Docker simulado indisponível passou sem criar diretório de validação. A infraestrutura iniciou depois de abrir Docker Desktop. A tentativa original permaneceu registrada no histórico privado.

## Escopo e limites

Nenhuma lacuna obrigatória conhecida foi identificada na conferência das 215 cláusulas e nos testes executados. As interpretações do caminho síncrono sem PENDING confirmado, de FAILED, do gateway SQS confiável e das reversões estão em ARCHITECTURE.md. A pontuação e a aprovação pertencem ao avaliador.

O ambiente é local: HTTP, Keycloak start-dev e credenciais de exemplo. Produção, atualização gradual com versões diferentes e capacidade/RPS não foram validados. Mesma carteira serializa; conexões por réplica e crescimento das tabelas exigem planejamento. Outbox é at-least-once; consumidores deduplicam eventId e tratam versões fora de ordem. O broker adaptado usa snapshots completos, fsync e volume preservado; não cobre perda física do volume. Partidas dobradas, tracing, dashboards e carga são opcionais não implementados. Uma suíte verde não garante ausência universal de bugs.
