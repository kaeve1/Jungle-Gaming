"""Independent HTTP acceptance probes. Python stdlib; no application/test imports.

Only local URLs are accepted. Creates fresh wallets; never deletes financial data.
Example: python tests/reviewer/blackbox.py --apis http://localhost:60721 ...
  --idp http://localhost:60720 --output path/to/blackbox.json
"""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
import copy
import json
from pathlib import Path
import random
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


def uid():
    return str(uuid.uuid4())


def money(cents):
    return {"amount": f"{cents // 100}.{cents % 100:02d}", "currency": "BRL"}


def cents(value):
    assert isinstance(value["amount"], str), "money is not a string"
    whole, fraction = value["amount"].split(".")
    assert len(fraction) == 2 and value["currency"] == "BRL"
    return int(whole) * 100 + int(fraction)


class Review:
    def __init__(self, args):
        self.args, self.results, self.wallets = args, [], []
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        self.tokens = {}
        for client in ["wallet-internal", "provider-a", "provider-b", "no-roles"]:
            data = urllib.parse.urlencode({"grant_type": "client_credentials", "client_id": client,
                "client_secret": f"local-example-{client}-secret"}).encode()
            req = urllib.request.Request(args.idp + "/realms/jungle-gaming/protocol/openid-connect/token", data=data)
            with self.opener.open(req, timeout=15) as response:
                self.tokens[client] = json.load(response)["access_token"]

    def request(self, method, path, body=None, role="wallet-internal", api=0, key=None, raw=None, token=None, headers=None):
        hs = {"Content-Type": "application/json"}
        if role is not None:
            hs["Authorization"] = "Bearer " + (token if token is not None else self.tokens[role])
        if key is not None:
            hs["Idempotency-Key"] = key
        hs.update(headers or {})
        data = raw if raw is not None else (json.dumps(body).encode() if body is not None else None)
        req = urllib.request.Request(self.args.apis[api % len(self.args.apis)] + path, data=data, headers=hs, method=method)
        try:
            response = self.opener.open(req, timeout=45)
        except urllib.error.HTTPError as exc:
            response = exc
        with response:
            payload = response.read()
            try:
                value = json.loads(payload)
            except (ValueError, UnicodeError):
                value = {"text": payload.decode(errors="replace")}
            return response.status, value

    def expect(self, code, *args, **kwargs):
        status, body = self.request(*args, **kwargs)
        assert status == code, f"{args[0]} {args[1]}: expected {code}, got {status}; code={body.get('error', {}).get('code')}"
        return body

    def run(self, name, fn):
        start = time.monotonic()
        try:
            fn()
            result = {"case": name, "status": "PASS"}
        except Exception as exc:
            result = {"case": name, "status": "FAIL", "detail": str(exc)}
        result["seconds"] = round(time.monotonic() - start, 3)
        self.results.append(result)
        print(json.dumps(result), flush=True)

    def wallet(self, initial=10000):
        w = self.expect(201, "POST", "/wallets", {"playerId": uid(), "initialBalance": money(initial)})
        assert cents(w["balance"]) == initial and w["version"] == 1
        self.wallets.append(w)
        return w

    def command(self, w, kind, amount, reference=None):
        b = {"providerId": "provider-a", "externalTransactionId": uid(), "playerId": w["playerId"],
             "walletId": w["id"], "roundId": "independent-review", "gameId": "review-game", "kind": kind, "money": money(amount)}
        if reference is not None:
            b["referenceExternalTransactionId"] = reference["externalTransactionId"]
        return b

    def post(self, b, code=201, api=0, key=None):
        return self.expect(code, "POST", "/wagering/transactions", b, role="provider-a", api=api,
                           key=b["externalTransactionId"] if key is None else key)

    def reconcile(self, w, expected, entries=None):
        r = self.expect(200, "POST", f"/wallets/{w['id']}/reconciliation")
        assert r["consistent"] and cents(r["storedBalance"]) == expected and cents(r["calculatedBalance"]) == expected
        assert cents(r["difference"]) == 0
        if entries is not None:
            assert r["checkedEntries"] == entries, f"expected {entries} ledger entries, got {r['checkedEntries']}"
        return r

    def security(self):
        w = self.wallet()
        b = self.command(w, "BET", 100)
        for role, token, expected in [(None, None, 401), ("provider-a", "invalid", 401), ("no-roles", None, 403)]:
            self.expect(expected, "POST", "/wagering/transactions", b, role=role, token=token, key=uid())
        token = self.tokens["provider-a"].split(".")
        claims = json.loads(base64.urlsafe_b64decode(token[1] + "=" * (-len(token[1]) % 4)))
        claims["provider_id"] = "provider-b"
        token[1] = base64.urlsafe_b64encode(json.dumps(claims).encode()).decode().rstrip("=")
        self.expect(401, "POST", "/wagering/transactions", b, role="provider-a", token=".".join(token), key=uid())
        for path, method in [(f"/wallets/{w['id']}", "GET"), (f"/wallets/{w['id']}/ledger", "GET"),
                             (f"/wallets/{w['id']}/reconciliation", "POST"), ("/metrics", "GET"), ("/wallets", "POST")]:
            self.expect(403, method, path, role="provider-a")
        self.expect(403, "POST", "/wagering/transactions", b, role="provider-b", key=uid())
        self.expect(403, "POST", "/wagering/transactions", b, role="wallet-internal", key=uid())
        self.reconcile(w, 10000, 1)
        tx = self.post(b)
        self.expect(404, "GET", "/wagering/transactions/" + tx["transactionId"], role="provider-b")
        other = copy.deepcopy(b)
        other["providerId"] = "provider-b"
        other["money"] = money(200)
        independent = self.expect(201, "POST", "/wagering/transactions", other, role="provider-b", key=b["externalTransactionId"])
        assert independent["transactionId"] != tx["transactionId"]
        self.reconcile(w, 9700, 3)

    def invalid_inputs(self):
        w = self.wallet()
        for amount in ["", "NaN", "Infinity", "1e2", "1.234", "-1.00", "-0.00", "01.00", "1", "1.0", "92233720368547758.08", 1, None]:
            b = self.command(w, "BET", 100)
            b["money"]["amount"] = amount
            self.post(b, 400)
        for kind, amount in [("BET", 0), ("WIN", 0), ("REFUND", 0), ("ROLLBACK", 0), ("LOSS", 1), ("OPENING", 100)]:
            self.post(self.command(w, kind, amount), 400)
        b = self.command(w, "BET", 100)
        raw = json.dumps(b)
        for malformed in [raw[:-1] + ',"kind":"WIN"}', raw + " {}", raw.replace('"amount": "1.00"', '"Amount": "1.00"'),
                          raw.replace('"amount": "1.00"', '"amount":"1.00","amount":"2.00"')]:
            self.expect(400, "POST", "/wagering/transactions", role="provider-a", key=uid(), raw=malformed.encode())
        self.expect(400, "POST", "/wagering/transactions", b, role="provider-a")
        self.expect(413, "POST", "/wagering/transactions", role="provider-a", key=uid(), raw=(raw + " " * 65536).encode())
        self.reconcile(w, 10000, 1)

    def main_path(self):
        w = self.wallet()
        self.expect(409, "POST", "/wallets", {"playerId": w["playerId"], "initialBalance": money(99999)})
        bet = self.command(w, "BET", 2500)
        first = self.post(bet)
        assert cents(first["balance"]) == 7500
        win = self.command(w, "WIN", 1000, bet)
        self.post(win, api=1)
        replay = self.post(bet, 200, api=2)
        assert replay["idempotentReplay"] and replay["transactionId"] == first["transactionId"] and cents(replay["balance"]) == 7500
        changed = copy.deepcopy(bet)
        changed["money"] = money(2600)
        self.post(changed, 409)
        self.post(bet, 409, key=uid())
        loss = self.command(w, "LOSS", 0)
        self.post(loss)
        state = self.expect(200, "GET", f"/wallets/{w['id']}")
        assert state["version"] == 3
        refund = self.command(w, "REFUND", 2500, bet)
        self.post(refund)
        self.post(self.command(w, "REFUND", 2500, bet), 422)
        self.post(self.command(w, "ROLLBACK", 2500, bet), 422)
        self.post(self.command(w, "ROLLBACK", 2500, refund))
        self.post(self.command(w, "ROLLBACK", 1000, win))
        self.reconcile(w, 7500, 6)
        cursor, entries = None, []
        while True:
            path = f"/wallets/{w['id']}/ledger?limit=2" + ("&cursor=" + urllib.parse.quote(cursor) if cursor else "")
            page = self.expect(200, "GET", path)
            entries.extend(page["entries"])
            cursor = page["nextCursor"]
            if cursor is None:
                break
        assert len(entries) == 6 and len({e["id"] for e in entries}) == 6
        assert [e["walletVersion"] for e in entries] == list(range(1, 7))
        self.saved = (bet, first)

    def zero_and_limits(self):
        w = self.wallet(0)
        self.reconcile(w, 0, 0)
        self.post(self.command(w, "LOSS", 0))
        self.reconcile(w, 0, 0)
        assert self.expect(200, "GET", f"/wallets/{w['id']}")["version"] == 1
        limit = self.wallet(9223372036854775807)
        rejection = self.post(self.command(limit, "WIN", 1), 422)
        assert rejection["failureCode"] == "BALANCE_LIMIT_EXCEEDED"
        self.reconcile(limit, 9223372036854775807, 1)
        w = self.wallet()
        win = self.command(w, "WIN", 15000)
        self.post(win)
        self.post(self.command(w, "BET", 24000))
        rejection = self.post(self.command(w, "ROLLBACK", 15000, win), 422)
        assert rejection["failureCode"] == "REVERSAL_INSUFFICIENT_FUNDS"
        self.reconcile(w, 1000, 3)

    def parallel(self):
        assert len(self.args.apis) >= 3, "three service endpoints required"
        w = self.wallet()
        b = self.command(w, "BET", 2000)
        barrier = threading.Barrier(50)
        def send(n):
            barrier.wait(timeout=20)
            return self.request("POST", "/wagering/transactions", b, role="provider-a", key=b["externalTransactionId"], api=n)
        with ThreadPoolExecutor(max_workers=50) as pool:
            replies = list(pool.map(send, range(50)))
        assert [r[0] for r in replies].count(201) == 1 and [r[0] for r in replies].count(200) == 49
        assert len({r[1]["transactionId"] for r in replies}) == 1
        assert all(cents(r[1]["balance"]) == 8000 for r in replies)
        self.reconcile(w, 8000, 2)
        w = self.wallet()
        bodies = [self.command(w, "BET", 8000) for _ in range(2)]
        barrier = threading.Barrier(2)
        def bet(n):
            barrier.wait(timeout=10)
            return self.request("POST", "/wagering/transactions", bodies[n], role="provider-a", key=bodies[n]["externalTransactionId"], api=n)
        with ThreadPoolExecutor(max_workers=2) as pool:
            replies = list(pool.map(bet, range(2)))
        assert sorted(r[0] for r in replies) == [201, 422]
        assert [r[1]["failureCode"] for r in replies if r[0] == 422] == ["INSUFFICIENT_FUNDS"]
        self.post(self.command(w, "WIN", 100), api=2)
        for n, b in enumerate(bodies):
            replay = self.post(b, 200 if replies[n][0] == 201 else 422, api=2)
            assert replay["idempotentReplay"] and cents(replay["balance"]) == 2000
            assert replay["transactionId"] == replies[n][1]["transactionId"]
        self.reconcile(w, 2100, 3)

    def pending(self):
        w = self.wallet()
        bet = self.command(w, "BET", 1000)
        refund = self.command(w, "REFUND", 1000, bet)
        pending = self.post(refund, 202)
        assert pending["balance"] is None
        self.post(bet, api=1)
        end = time.monotonic() + 20
        while time.monotonic() < end:
            tx = self.expect(200, "GET", "/wagering/transactions/" + pending["transactionId"], role="provider-a", api=2)
            if tx["status"] == "PROCESSED":
                break
            time.sleep(0.1)
        assert tx["status"] == "PROCESSED", "reference worker did not resolve"
        replay = self.post(refund, 200)
        assert replay["transactionId"] == pending["transactionId"]
        self.reconcile(w, 10000, 3)

    def model_sequence(self):
        rng = random.Random(20261002)
        w = self.wallet(10000)
        expected, count = 10000, 1
        for n in range(80):
            kind = rng.choice(["BET", "WIN", "LOSS"])
            amount = 0 if kind == "LOSS" else rng.randint(1, 18000)
            rejected = kind == "BET" and amount > expected
            b = self.command(w, kind, amount)
            out = self.post(b, 422 if rejected else 201, api=n)
            if not rejected and kind != "LOSS":
                expected += amount if kind == "WIN" else -amount
                count += 1
            assert cents(out["balance"]) == expected
            if n % 9 == 0:
                replay = self.post(b, 422 if rejected else 200, api=n+1)
                assert replay["idempotentReplay"] and replay["transactionId"] == out["transactionId"]
            self.reconcile(w, expected, count)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--apis", nargs="+", required=True)
    parser.add_argument("--idp", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    for url in [*args.apis, args.idp]:
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme != "http" or parsed.hostname not in ("localhost", "127.0.0.1") or parsed.path or parsed.query or parsed.username:
            parser.error("only local HTTP origins allowed")
    review = Review(args)
    for name, method in [("authorization and provider isolation", review.security), ("adversarial inputs", review.invalid_inputs),
                         ("main path and complete reversals", review.main_path), ("zero, overflow and insufficient reversal", review.zero_and_limits),
                         ("50 duplicates and competing bets across three real services", review.parallel),
                         ("reference arrives after refund", review.pending), ("80 operations against independent integer model", review.model_sequence)]:
        review.run(name, method)
    report = {"results": review.results, "wallet_ids": [w["id"] for w in review.wallets], "apis": args.apis}
    Path(args.output).write_text(json.dumps(report, indent=2), encoding="utf-8")
    raise SystemExit(1 if any(r["status"] == "FAIL" for r in review.results) else 0)


if __name__ == "__main__":
    main()
