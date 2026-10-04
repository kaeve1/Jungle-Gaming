#!/bin/bash
# Provisiona filas e identidades no emulador SQS (MiniStack). Idempotente:
# pode rodar a cada "docker compose up". Executado no container amazon/aws-cli
# com a credencial administrativa do emulador.
#
# Filas (ARCHITECTURE.md §9 e §13):
#   wager-transactions-dlq.fifo  destino do redrive
#   wager-transactions.fifo      entrada de operações; redrive após 10 recebimentos
#   wallet-events.fifo           eventos de saída publicados pela outbox
#
# Identidades IAM (menor privilégio):
#   wager-producer  só envia para wager-transactions.fifo
#   wallet-service  consome wager-transactions.fifo e publica em wallet-events.fifo
#   events-reader   só lê e apaga de wallet-events.fifo (consumidor de eventos/testes)
#
# As chaves geradas são gravadas num arquivo de credenciais no formato padrão da
# AWS ($CREDENTIALS_FILE), lido pelo SDK via AWS_SHARED_CREDENTIALS_FILE.
set -euo pipefail
shopt -s inherit_errexit

: "${AWS_ENDPOINT_URL:?}" "${CREDENTIALS_FILE:?}"
REGION="${AWS_DEFAULT_REGION:-us-east-1}"
ACCOUNT="000000000000"

log() { echo "[sqs-init] $*"; }

attempt=0
until aws sqs list-queues >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if (( attempt >= 30 )); then log "broker indisponível ou credenciais administrativas inválidas"; exit 1; fi
  log "aguardando o emulador SQS..."
  sleep 1
done

arn() { echo "arn:aws:sqs:${REGION}:${ACCOUNT}:$1"; }

create_queue() { # nome atributos-json
  aws sqs create-queue --queue-name "$1" --attributes "$2" --query QueueUrl --output text >/dev/null
  # create-queue não altera atributos de fila existente: aplica sempre.
  local url
  url=$(aws sqs get-queue-url --queue-name "$1" --query QueueUrl --output text)
  aws sqs set-queue-attributes --queue-url "$url" --attributes "$(echo "$2" | sed 's/"FifoQueue":"true",//')"
  log "fila $1 pronta"
}

# Alta vazão FIFO: deduplicação e limite de vazão por grupo (carteira), para que
# carteiras diferentes não disputem o mesmo limite.
FIFO_HT='"FifoQueue":"true","ContentBasedDeduplication":"false","DeduplicationScope":"messageGroup","FifoThroughputLimit":"perMessageGroupId","MessageRetentionPeriod":"1209600"'

create_queue wager-transactions-dlq.fifo "{${FIFO_HT}}"
REDRIVE="{\\\"deadLetterTargetArn\\\":\\\"$(arn wager-transactions-dlq.fifo)\\\",\\\"maxReceiveCount\\\":\\\"10\\\"}"
create_queue wager-transactions.fifo "{${FIFO_HT},\"VisibilityTimeout\":\"30\",\"ReceiveMessageWaitTimeSeconds\":\"20\",\"RedrivePolicy\":\"${REDRIVE}\"}"
create_queue wallet-events.fifo "{${FIFO_HT},\"VisibilityTimeout\":\"30\",\"ReceiveMessageWaitTimeSeconds\":\"20\"}"

policy() { # ações-json recurso-json
  echo "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":$1,\"Resource\":$2}]}"
}

ensure_user() { # usuário política-json
  aws iam get-user --user-name "$1" >/dev/null 2>&1 || aws iam create-user --user-name "$1" >/dev/null
  aws iam put-user-policy --user-name "$1" --policy-name least-privilege --policy-document "$2"
  # Preserva clientes em execução: nunca revoga chaves no provisionamento.
  local saved_key saved_secret existing record
  record="${CREDENTIALS_FILE}.$1"
  saved_key=""; saved_secret=""
  if [[ -f "$record" ]]; then
    read -r saved_key saved_secret < "$record"
  elif [[ -f "$CREDENTIALS_FILE" ]]; then
    read -r saved_key saved_secret <<<"$(awk -v p="[$1]" '
      /^\[/ { active=($0==p) }
      active && $1=="aws_access_key_id" { k=$3 }
      active && $1=="aws_secret_access_key" { s=$3 }
      END { print k, s }' "$CREDENTIALS_FILE")"
  fi
  existing=$(aws iam list-access-keys --user-name "$1" --query 'AccessKeyMetadata[?Status==`Active`].AccessKeyId' --output text)
  if [[ -n "$saved_key" && -n "$saved_secret" ]] && echo "$existing" | tr '\t' '\n' | grep -Fxq -- "$saved_key"; then
    printf '%s\t%s\n' "$saved_key" "$saved_secret"
    return
  fi
  # Uma chave nova é necessária apenas se o broker foi recriado ou a credencial
  # local não existe. Nunca remover chaves válidas de outros clientes.
  umask 077
  aws iam create-access-key --user-name "$1" --query 'AccessKey.[AccessKeyId,SecretAccessKey]' --output text > "${record}.tmp"
  mv "${record}.tmp" "$record"
  cat "$record"
}

WAGER="\"$(arn wager-transactions.fifo)\""
DLQ="\"$(arn wager-transactions-dlq.fifo)\""
EVENTS="\"$(arn wallet-events.fifo)\""

producer_pair=$(ensure_user wager-producer "$(policy '["sqs:SendMessage","sqs:GetQueueUrl","sqs:GetQueueAttributes"]' "[$WAGER]")")
read -r P_KEY P_SECRET <<<"$producer_pair"
SERVICE_POLICY="{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"sqs:ReceiveMessage\",\"sqs:DeleteMessage\",\"sqs:ChangeMessageVisibility\",\"sqs:GetQueueUrl\",\"sqs:GetQueueAttributes\"],\"Resource\":[$WAGER]},{\"Effect\":\"Allow\",\"Action\":[\"sqs:SendMessage\",\"sqs:GetQueueUrl\",\"sqs:GetQueueAttributes\"],\"Resource\":[$EVENTS]},{\"Effect\":\"Allow\",\"Action\":[\"sqs:GetQueueUrl\",\"sqs:GetQueueAttributes\"],\"Resource\":[$DLQ]}]}"
service_pair=$(ensure_user wallet-service "$SERVICE_POLICY")
read -r S_KEY S_SECRET <<<"$service_pair"
reader_pair=$(ensure_user events-reader "$(policy '["sqs:ReceiveMessage","sqs:DeleteMessage","sqs:GetQueueUrl","sqs:GetQueueAttributes"]' "[$EVENTS]")")
read -r R_KEY R_SECRET <<<"$reader_pair"

umask 077
tmp="${CREDENTIALS_FILE}.tmp"
cat >"$tmp" <<EOF
# Gerado por deploy/sqs/init.sh — credenciais locais preservadas entre subidas.
[wallet-service]
aws_access_key_id = ${S_KEY}
aws_secret_access_key = ${S_SECRET}

[wager-producer]
aws_access_key_id = ${P_KEY}
aws_secret_access_key = ${P_SECRET}

[events-reader]
aws_access_key_id = ${R_KEY}
aws_secret_access_key = ${R_SECRET}
EOF
# Legível pelo usuário sem privilégios da aplicação (distroless nonroot) e dos testes.
chmod 0644 "$tmp"
mv "$tmp" "$CREDENTIALS_FILE"
# Runtime receives a separate volume containing only its own profile.
if [[ -n "${SERVICE_CREDENTIALS_FILE:-}" ]]; then
  umask 077
  service_tmp="${SERVICE_CREDENTIALS_FILE}.tmp"
  printf '[wallet-service]\naws_access_key_id = %s\naws_secret_access_key = %s\n' "$S_KEY" "$S_SECRET" > "$service_tmp"
  chmod 0644 "$service_tmp"
  mv "$service_tmp" "$SERVICE_CREDENTIALS_FILE"
fi
log "identidades wager-producer, wallet-service e events-reader prontas; credenciais em ${CREDENTIALS_FILE}"

