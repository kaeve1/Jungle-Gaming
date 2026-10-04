#!/bin/sh
# Executado uma única vez pelo container do PostgreSQL, quando o volume está vazio.
# Cria o banco da aplicação e dois papéis (ARCHITECTURE.md §10):
#   wallet_migrator — dono do schema; usado só pelas migrations.
#   wallet_app      — usado pela aplicação; sem superusuário, sem BYPASSRLS, não é
#                     dono de nada, portanto não consegue desligar triggers nem
#                     alterar o schema.
# Senhas chegam por variável de ambiente e entram no SQL como variáveis do psql
# (:'nome'), que o psql escapa — nunca por interpolação de texto.
set -eu

: "${WALLET_DB:?}" "${WALLET_MIGRATOR_PASSWORD:?}" "${WALLET_APP_PASSWORD:?}"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v db="$WALLET_DB" -v migrator_pw="$WALLET_MIGRATOR_PASSWORD" -v app_pw="$WALLET_APP_PASSWORD" <<'SQL'
CREATE ROLE wallet_migrator LOGIN PASSWORD :'migrator_pw'
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE wallet_app LOGIN PASSWORD :'app_pw'
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE DATABASE :"db" OWNER wallet_migrator;
REVOKE ALL ON DATABASE :"db" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"db" TO wallet_app;
SQL

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$WALLET_DB" -v db="$WALLET_DB" <<'SQL'
-- Ninguém além do dono cria objetos; o schema da aplicação pertence ao migrator.
REVOKE ALL ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA wallet AUTHORIZATION wallet_migrator;
GRANT USAGE ON SCHEMA wallet TO wallet_app;
ALTER ROLE wallet_migrator IN DATABASE :"db" SET search_path = wallet;
ALTER ROLE wallet_app IN DATABASE :"db" SET search_path = wallet;
SQL
