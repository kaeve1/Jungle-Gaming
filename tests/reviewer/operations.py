"""Operational adversarial probes against an explicitly isolated validation project.

Requires the local defaults and three running API containers. The broker probe
purges ONLY this validation project's output queue and kills ONLY its broker.
No secrets or access tokens are printed. Financial rows are never edited/deleted.
"""
import argparse
import datetime
import json
from pathlib import Path
import re
import subprocess
import time
import uuid

from blackbox import Review


class Operations:
    def __init__(self, args):
        self.args, self.results = args, []
        self.http = Review(args)
        self.sqs = args.project + "-sqs-1"
        self.postgres = args.project + "-postgres-1"
        for name, service in [(self.sqs, "sqs"), (self.postgres, "postgres")]:
            value = json.loads(self.docker("inspect", name))[0]
            labels = value["Config"]["Labels"]
            assert labels["com.docker.compose.project"] == args.project
            assert labels["com.docker.compose.service"] == service

    def docker(self, *args, stdin=None):
        proc = subprocess.run(["docker", *args], input=stdin, text=True, capture_output=True, timeout=90)
        if proc.returncode:
            raise RuntimeError(f"docker {args[0]} failed with exit {proc.returncode}; content suppressed")
        return proc.stdout.strip()

    def sql(self, query):
        return self.docker("exec", "-i", self.postgres, "sh", "-c",
            'PGPASSWORD="$WALLET_APP_PASSWORD" exec psql -h 127.0.0.1 -U wallet_app -d wallet -qAt -v ON_ERROR_STOP=1', stdin=query)

    def broker(self, source):
        prefix = """import os,json,time
import botocore.session
from botocore.config import Config
def client(service):
    return botocore.session.get_session().create_client(service, endpoint_url='http://127.0.0.1:4566', region_name='us-east-1', aws_access_key_id=os.environ['AWS_ACCESS_KEY_ID'], aws_secret_access_key=os.environ['AWS_SECRET_ACCESS_KEY'], config=Config(retries={'max_attempts':0},connect_timeout=2,read_timeout=5))
sqs=client('sqs')
"""
        return self.docker("exec", "-i", self.sqs, "python", "-", stdin=prefix + source)

    def wait(self, check, seconds=40):
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            try:
                if check():
                    return
            except Exception:
                pass
            time.sleep(0.2)
        raise AssertionError("bounded condition did not become true")

    def record(self, name, fn):
        start = time.monotonic()
        try:
            detail = fn()
            result = {"case": name, **detail}
        except Exception as exc:
            result = {"case": name, "status": "PROBE_ERROR", "detail": str(exc)}
        result["seconds"] = round(time.monotonic() - start, 3)
        self.results.append(result)
        print(json.dumps(result), flush=True)

    def sql_reconciliation(self):
        source = json.loads(Path(self.args.blackbox).read_text(encoding="utf-8"))
        ids = source["wallet_ids"]
        for value in ids:
            uuid.UUID(value)
        values = ",".join("'" + value + "'" for value in ids)
        result = self.sql("SELECT count(*) FROM wallet.wallets w WHERE w.id IN (" + values + ") AND w.balance_amount <> "
            "(SELECT COALESCE(sum(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END),0) FROM wallet.ledger_entries e WHERE e.wallet_id=w.id);")
        assert result == "0", "SQL reconciliation differs from wallet balance"
        return {"status": "PASS", "wallets": len(ids), "divergences": 0}

    def restart_real_services(self):
        w = self.http.wallet()
        bet = self.http.command(w, "BET", 2500)
        original = self.http.post(bet)
        self.http.post(self.http.command(w, "WIN", 100))
        names = [self.args.project + "-api-1", self.args.project + "-review-api-2", self.args.project + "-review-api-3"]
        for name in names:
            inspected = json.loads(self.docker("inspect", name))[0]
            assert inspected["Config"]["Labels"]["com.docker.compose.project"] == self.args.project
        self.docker("stop", "--time", "20", *names)
        self.docker("start", *names)
        for api in range(3):
            self.wait(lambda: self.http.request("GET", "/health/ready", role=None, api=api)[0] == 200)
            replay = self.http.post(bet, 200, api=api)
            assert replay["transactionId"] == original["transactionId"] and replay["idempotentReplay"]
            assert replay["balance"]["amount"] == "75.00"
        self.http.reconcile(w, 7600, 3)
        return {"status": "PASS", "services_restarted": 3, "original_balance": "75.00", "current_balance": "76.00"}

    def publisher_identity(self):
        names = [self.args.project + "-api-1", self.args.project + "-review-api-2", self.args.project + "-review-api-3"]

        def publication_log(event):
            for name in names:
                for line in self.docker("logs", name).splitlines():
                    try:
                        row = json.loads(line)
                    except ValueError:
                        continue
                    if row.get("eventId") == event and row.get("msg") == "outbox event published":
                        return row
            return None

        w = self.http.wallet()
        bet = self.http.command(w, "BET", 1000)
        refund = self.http.command(w, "REFUND", 1000, bet)
        message = "review-msg-" + uuid.uuid4().hex
        envelope = {"messageId": message, "type": "WagerTransactionRequested", "occurredAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                    "data": {**refund, "idempotencyKey": refund["externalTransactionId"]}}
        send_source = "q=sqs.get_queue_url(QueueName='wager-transactions.fifo')['QueueUrl']\n"
        send_source += "body=" + repr(json.dumps(envelope)) + "\n"
        send_source += "sqs.send_message(QueueUrl=q,MessageBody=body,MessageGroupId=" + repr(w["id"]) + ",MessageDeduplicationId=" + repr(uuid.uuid4().hex) + ")\nprint('sent')\n"
        self.broker(send_source)
        query = "SELECT status FROM wallet.wager_transactions WHERE provider_id='provider-a' AND external_transaction_id='" + refund["externalTransactionId"] + "';"
        self.wait(lambda: self.sql(query) == "PENDING_REFERENCE")
        self.http.post(bet)
        self.wait(lambda: self.sql(query) == "PROCESSED")
        event_query = "SELECT event_id::text FROM wallet.outbox_events WHERE event_type='WagerTransactionProcessed' AND payload->>'correlationId'='" + message + "' AND published_at IS NOT NULL;"
        self.wait(lambda: bool(self.sql(event_query)))
        event = self.sql(event_query)
        uuid.UUID(event)
        self.wait(lambda: publication_log(event) is not None)
        row = publication_log(event)
        logged = row.get("messageId")
        assert row.get("causationId") and row["causationId"] != message, "pending cause mislabeled or lost"
        self.http.reconcile(w, 10000, 3)

        http_wallet = self.http.wallet()
        http_bet = self.http.command(http_wallet, "BET", 1000)
        pending = self.http.post(self.http.command(http_wallet, "REFUND", 1000, http_bet), 202)
        self.http.post(http_bet)
        http_query = ("SELECT event_id::text FROM wallet.outbox_events WHERE event_type='WagerTransactionProcessed' "
            "AND payload->'data'->>'transactionId'='" + pending['transactionId'] + "' AND published_at IS NOT NULL;")
        self.wait(lambda: bool(self.sql(http_query)))
        http_event = self.sql(http_query)
        self.wait(lambda: publication_log(http_event) is not None)
        http_log = publication_log(http_event)
        assert http_log.get('causationId') and 'messageId' not in http_log, "HTTP resolution invented input message"
        self.http.reconcile(http_wallet, 10000, 3)
        return {"status": "FINDING" if logged != message else "PASS", "input_message_id": message,
                "publisher_message_id": logged, "event_id": event, "correlation_id": row.get("correlationId"),
                "causation_id": row.get("causationId"), "http_resolution_has_message_id": False}

    def custom_readiness(self):
        queue = "review-custom-" + uuid.uuid4().hex[:8] + ".fifo"
        policy_name = "review-custom-health"
        policy = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": ["sqs:GetQueueUrl", "sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility"],
                  "Resource": "arn:aws:sqs:us-east-1:000000000000:" + queue}]}
        self.broker("sqs.create_queue(QueueName=" + repr(queue) + ",Attributes={'FifoQueue':'true'})\nclient('iam').put_user_policy(UserName='wallet-service',PolicyName=" + repr(policy_name) + ",PolicyDocument=" + repr(json.dumps(policy)) + ")\nprint('provisioned')")
        name = self.args.project + "-review-custom-health"
        started = False
        try:
            self.docker("compose", "-p", self.args.project, "--env-file", self.args.env_file, "run", "--no-deps", "-d", "--name", name,
                        "-e", "APP_ROLES=api,consumer", "-e", "CONSUMER_QUEUE=" + queue, "api")
            started = True
            self.wait(lambda: self.docker("exec", name, "/service", "healthcheck") == "")
            self.broker("q=sqs.get_queue_url(QueueName=" + repr(queue) + ")['QueueUrl']\nsqs.delete_queue(QueueUrl=q)\nprint('configured queue removed')")
            self.wait(lambda: "consumer receive failed" in self.docker("logs", name), seconds=30)
            check = subprocess.run(["docker", "exec", name, "/service", "healthcheck"], capture_output=True, timeout=10)
            self.broker("sqs.create_queue(QueueName=" + repr(queue) + ",Attributes={'FifoQueue':'true'})\nprint('configured queue restored')")
            self.wait(lambda: self.docker("exec", name, "/service", "healthcheck") == "")
            return {"status": "FINDING" if check.returncode == 0 else "PASS", "configured_queue_exists": False,
                    "consumer_failure_observed": True, "healthcheck_exit": check.returncode, "recovery_healthcheck_exit": 0}
        finally:
            try:
                if started:
                    self.docker("stop", "--time", "20", name)
                    self.docker("rm", name)
            finally:
                self.broker("client('iam').delete_user_policy(UserName='wallet-service',PolicyName=" + repr(policy_name) + ")\nprint('policy removed')")

    def broker_crash(self):
        self.wait(lambda: self.sql("SELECT count(*) FROM wallet.outbox_events WHERE published_at IS NULL;") == "0")
        self.broker("q=sqs.get_queue_url(QueueName='wallet-events.fifo')['QueueUrl']\nsqs.purge_queue(QueueUrl=q)\nprint('isolated output emptied')")
        # Graceful checkpoint preserves identities/queues and an empty output.
        self.docker("stop", "--time", "30", self.sqs)
        self.docker("start", self.sqs)
        self.wait(lambda: self.broker("print(sqs.get_queue_attributes(QueueUrl=sqs.get_queue_url(QueueName='wallet-events.fifo')['QueueUrl'],AttributeNames=['ApproximateNumberOfMessages'])['Attributes']['ApproximateNumberOfMessages'])") == "0")
        w = self.http.wallet(12345)
        query = "SELECT count(*) FROM wallet.outbox_events WHERE partition_key='" + w["id"] + "' AND published_at IS NOT NULL;"
        self.wait(lambda: self.sql(query) == "2")
        count_source = "q=sqs.get_queue_url(QueueName='wallet-events.fifo')['QueueUrl']\nprint(sqs.get_queue_attributes(QueueUrl=q,AttributeNames=['ApproximateNumberOfMessages'])['Attributes']['ApproximateNumberOfMessages'])"
        before = int(self.broker(count_source))
        assert before == 2, "must observe two actual accepted messages before crash"
        input_queue = "review-crash-" + uuid.uuid4().hex[:8] + ".fifo"
        receive_source = ("q=sqs.get_queue_url(QueueName=" + repr(input_queue) + ")['QueueUrl']\n" +
            "print(json.dumps(sqs.receive_message(QueueUrl=q,VisibilityTimeout=1,WaitTimeSeconds=0,AttributeNames=['ApproximateReceiveCount']).get('Messages',[])))")
        self.broker("q=sqs.create_queue(QueueName=" + repr(input_queue) + ",Attributes={'FifoQueue':'true'})['QueueUrl']\n" +
            "sqs.send_message(QueueUrl=q,MessageBody='review-durable-input',MessageGroupId='review',MessageDeduplicationId='" + uuid.uuid4().hex + "')\nprint('input accepted')")
        first = json.loads(self.broker(receive_source))
        assert len(first) == 1 and first[0]['Attributes']['ApproximateReceiveCount'] == '1'
        self.docker("kill", "--signal", "KILL", self.sqs)
        self.docker("start", self.sqs)
        self.wait(lambda: bool(self.broker(count_source)))
        after = int(self.broker(count_source))
        confirmed = int(self.sql(query))
        time.sleep(1.1)
        redelivery = json.loads(self.broker(receive_source))
        assert len(redelivery) == 1 and redelivery[0]['MessageId'] == first[0]['MessageId'], "input lost across crash"
        assert redelivery[0]['Attributes']['ApproximateReceiveCount'] == '2', "receive state did not survive"
        self.broker("q=sqs.get_queue_url(QueueName=" + repr(input_queue) + ")['QueueUrl']\nsqs.delete_message(QueueUrl=q,ReceiptHandle=" + repr(redelivery[0]['ReceiptHandle']) + ")\nprint('delete accepted')")
        self.docker("kill", "--signal", "KILL", self.sqs)
        self.docker("start", self.sqs)
        self.wait(lambda: bool(self.broker(count_source)))
        assert json.loads(self.broker(receive_source)) == [], "acknowledged delete resurrected input"
        assert int(self.broker(count_source)) == after, "output changed during second crash"
        self.http.reconcile(w, 12345, 1)
        return {"status": "FINDING" if after != before else "PASS", "queued_before_kill": before,
                "queued_after_restart": after, "sql_events_marked_published": confirmed, "wallet_id": w["id"],
                "input_receive_count_after_restart": 2, "acknowledged_delete_survived_second_crash": True}


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--project", required=True)
    p.add_argument("--env-file", default="tests/validation.env", help="Compose environment file for the readiness probe")
    p.add_argument("--apis", nargs="+", required=True)
    p.add_argument("--idp", required=True)
    p.add_argument("--blackbox", required=True)
    p.add_argument("--output", required=True)
    args = p.parse_args()
    if not re.fullmatch(r"jungle-gaming-validation-[0-9a-f]{12}", args.project):
        p.error("only isolated validation project allowed")
    ops = Operations(args)
    for name, probe in [("direct SQL reconciliation", ops.sql_reconciliation), ("restart three real service containers", ops.restart_real_services),
                        ("publisher original message identity", ops.publisher_identity), ("readiness for configured queue", ops.custom_readiness),
                        ("confirmed outbox versus broker SIGKILL", ops.broker_crash)]:
        ops.record(name, probe)
    Path(args.output).write_text(json.dumps({"project": args.project, "results": ops.results}, indent=2), encoding="utf-8")
    raise SystemExit(1 if any(r["status"] != "PASS" for r in ops.results) else 0)


if __name__ == "__main__":
    main()
