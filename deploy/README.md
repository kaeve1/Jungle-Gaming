# Infraestrutura local

- `postgres/init/`: criação dos papéis locais do banco.
- `keycloak/`: realm e identidades de exemplo importadas no startup.
- `sqs/init.sh`: provisionamento das filas, políticas e credenciais locais.
- [sqs/strict](sqs/strict/README.md): adaptação local de autenticação e durabilidade do MiniStack.

O [Compose](../compose.yaml) coordena os serviços e jobs. Os padrões em [.env.example](../.env.example) servem para demonstração local. Limitações de produção e segurança em [ARCHITECTURE](../ARCHITECTURE.md).
