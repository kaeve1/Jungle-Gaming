# Broker local com autenticação estrita

Implementado por **Codex** em resposta à revisão CODEX-002. A imagem base MiniStack 1.5.20 é fixada por digest; seu código não é alterado. O entrypoint instala uma camada ASGI antes de qualquer rota do servidor. Não existe listener alternativo do MiniStack para HTTP sem essa camada.

O perfil suportado é deliberadamente pequeno: conta `000000000000`, região `us-east-1`, SQS JSON com `X-Amz-Target: AmazonSQS.*` e IAM Query administrativo, ambos POST `/` com assinatura SigV4 em header. AWS SDK Go v2 e AWS CLI utilizados no projeto exercitam esse perfil. A canonicalização e assinatura usam botocore já incluído na imagem fixada; comparação em tempo constante. Referência: https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html

São recusados: assinatura ausente/incorreta, segredo errado, aliases administrativos `test` e IDs numéricos, corpo adulterado, alvo não assinado/adulterado, timestamps fora de cinco minutos, payload sem hash válido quando o header de hash existe, headers duplicados, outros serviços, STS/presigned URLs, query strings, WebSockets e endpoints administrativos do emulador. IAM só é acessível pela chave administrativa explicitamente configurada e sua assinatura válida. SQS ainda passa pela avaliação IAM do MiniStack.

Não usar as credenciais de exemplo em produção. Esse é um perfil de testes locais, não uma implementação geral de autenticação AWS. Não acrescenta autorização por provedor ao conteúdo da mensagem: o produtor da fila continua sendo um serviço interno confiável conforme ARCHITECTURE.md.

## Persistência resistente a encerramento abrupto — REV-01

`durable_state.py`, implementado por Codex, envolve os handlers SQS/IAM da versão fixada. Após cada chamada e antes de devolver a resposta ao gateway, grava o snapshot em arquivo temporário privado, executa flush/fsync, substitui o arquivo atomicamente e executa fsync no diretório do volume `/state`. O aceite de SendMessage só sai depois dessa gravação. ReceiveMessage, visibilidade, redrive, DeleteMessage e alterações de IAM também ficam persistidos. Falha de persistência não devolve uma resposta de sucesso.

O startup carrega os snapshots SQS/IAM, verifica o formato e a escrita do volume antes de concluir o lifespan. Estado ilegível, corrompido ou de versão incompatível impede iniciar; não é tratado como fila vazia. O formato permanece compatível com os arquivos do MiniStack fixado, incluindo volumes locais anteriores. As credenciais/restauração IAM também sobrevivem ao reinício. O perfil continua restrito a IAM/SQS; outros serviços são bloqueados na entrada.

A gravação roda no mesmo event loop do handler, sem `await` entre o snapshot e o fsync, e usa um único processo servidor. Long polling não segura um lock de persistência enquanto espera mensagens. A solução grava snapshots completos e reduz throughput conforme o backlog cresce; é uma adaptação do emulador local, sem promessa de capacidade de produção. Requer filesystem Linux que respeite fsync e volume preservado. Não protege contra remoção do volume, corrupção física ou falha do armazenamento que descumpra fsync. Entrega permanece at-least-once; consumidores precisam deduplicar.

O build testa ordem de persistência/aceite, falha de fsync sem sucesso e sem destruir o snapshot anterior, restauração/versão inválida, permissões0600 e envio concorrente a long polling. A prova operacional em `tests/reviewer/operations.py` mata o broker real, confere os eventos aceitos e testa reentrega/ReceiveCount e Delete confirmado após dois reinícios abruptos. Resultados da correção ficam em `docs/VALIDACAO-FINAL.md`.

Validação:

```sh
docker compose build sqs
docker compose --env-file tests/test.env run --rm integration
```

O build executa os testes Python de assinatura. A integração usa o broker real com chaves reais geradas no emulador, testa controle positivo, segredos errados, aliases root, adulterações, expiração e políticas entre filas.

Credenciais em volume são locais, não devem ser copiadas para o repositório. O arquivo agregado é para ferramentas/testes; a futura aplicação deve receber somente o perfil `wallet-service` necessário ao seu papel, nunca credenciais administrativas ou dos demais perfis. Provisionamento deve ser executado por um único job por ambiente; ele preserva chaves existentes e não realiza rotação automática.
