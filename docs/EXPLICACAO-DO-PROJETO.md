# Como explicar o projeto

O provedor manda uma aposta identificada e autenticada. A aplicação verifica quem ele é, encontra a carteira e toma uma decisão. A mesma operação pode chegar mais de uma vez, por HTTP ou SQS, mas movimenta dinheiro uma única vez.

**Dinheiro:** 25.00 BRL é guardado como2500 centavos e BRL. int64/strings evitam os erros de aproximação de float. Validamos formato, moeda e overflow; não arredondamos entradas inválidas.

**Carteira e ledger:** carteira guarda saldo atual; ledger guarda cada crédito/débito com saldo anterior/posterior. São gravados junto com resultado e eventos na mesma transação SQL. Uma rejeição não cria lançamento. OPENING positivo é crédito interno/versão1; abertura zero e LOSS não movimentam.

**Concorrência:** PostgreSQL trava a linha de uma carteira. Outra carteira continua livre. Três processos separados usam o mesmo banco; um mutex Go não resolveria isso. Duas BET80 sobre100 produzem uma aposta aprovada e outra rejeitada/saldo20. A ordem de chegada não garante qual delas vence.

**Idempotência:** banco guarda chave, ID externo e hash canônico do negócio. Repetição igual consulta resultado anterior; chave com conteúdo diferente ou ID externo reapresentado com outra chave gera conflito. Replay retorna o saldo original, que pode ser diferente do saldo atual.

**Inbox:** guarda messageId do envelope/consumidor/hash na mesma transação da operação SQS. Só removemos a mensagem depois do commit. Se o processo morrer depois do commit, o broker reentrega e a inbox impede repetir o débito.

**Outbox:** guarda evento no commit financeiro; outro worker publica depois. Publicar diretamente durante a operação causaria buracos: banco confirmado sem evento ou evento sem banco confirmado. Se envio foi aceito mas a confirmação SQL falhou, retomamos com o mesmo eventId. Consumidores da saída precisam deduplicar; at-least-once pode repetir. FIFO não substitui as regras SQL nem garante ordem de commit entre publishers.

**Referências:** um REFUND pode chegar antes da BET. Confirmamos PENDING_REFERENCE e um worker tenta novamente com backoff/TTL baseado no relógio DB. Após reinício outra instância continua. Referência inválida rejeita; não encontrada no prazo rejeita; apenas uma reversão bem-sucedida impede devolver o mesmo débito duas vezes.

**Autenticação:** Keycloak emite o JWT; API verifica assinatura, emissor, audience e validade, além das permissões. providerId vem da identidade confiável. Provedores leem só suas transações; carteiras e reconciliação são internas. Produtor SQS é gateway interno confiável com IAM de menor privilégio; não é uma credencial compartilhada com provedores externos.

**Fx:** monta dependências por construtores e coordena início/fim. HTTP e workers começam com dependências verificadas; no shutdown param entradas, encerram trabalho e só depois fecham recursos. Domínio não importa Fx/pgx/HTTP/AWS.

**Provas:** unidade verifica regras e limites; integração usa PostgreSQL/Keycloak/SQS reais; três PIDs verificam concorrência e reinício; barreiras e SIGKILL alcançam as janelas críticas. A demonstração usa o binário real; validate-clean roda tudo com recursos novos. Não afirmar capacidade em RPS sem teste de carga.

**Limites que precisam ser ditos:** a imagem local adaptada persiste SQS/IAM antes do aceite e passou por SIGKILL; snapshots completos têm custo de throughput e dependem de armazenamento que respeite fsync; ambiente local sem TLS e com exemplos de segredo não é configuração de produção. Gateway/jogador↔provedor, retenção, orçamento de conexões e capacidade de carga exigem decisões operacionais adicionais. Não existe garantia universal de ausência de bugs porque uma suíte ficou verde.
