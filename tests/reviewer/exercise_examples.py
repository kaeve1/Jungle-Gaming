"""External acceptance of the exercise's 1000.00 -> 975.00 example.

Python stdlib only. Creates fresh local data and prints no access tokens.
"""
import argparse
import json
from pathlib import Path
import urllib.parse
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--api', required=True)
    parser.add_argument('--idp', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    for origin in (args.api, args.idp):
        value = urllib.parse.urlsplit(origin)
        if value.scheme != 'http' or value.hostname not in ('127.0.0.1', 'localhost') or value.path or value.query or value.username:
            parser.error('only local HTTP origins allowed')
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def token(client):
        body = urllib.parse.urlencode({'grant_type': 'client_credentials', 'client_id': client,
                                      'client_secret': 'local-example-' + client + '-secret'}).encode()
        request = urllib.request.Request(args.idp + '/realms/jungle-gaming/protocol/openid-connect/token', data=body)
        with opener.open(request, timeout=15) as response:
            return json.load(response)['access_token']

    internal, provider = token('wallet-internal'), token('provider-a')

    def call(method, path, identity, expected, body=None, key=None):
        headers = {'Authorization': 'Bearer ' + identity, 'Content-Type': 'application/json'}
        if key:
            headers['Idempotency-Key'] = key
        request = urllib.request.Request(args.api + path, method=method, headers=headers,
                                         data=None if body is None else json.dumps(body).encode())
        with opener.open(request, timeout=20) as response:
            if response.status != expected:
                raise AssertionError('unexpected HTTP status')
            return json.load(response)

    def check(condition, message):
        if not condition:
            raise AssertionError(message)

    player = str(uuid.uuid4())
    wallet = call('POST', '/wallets', internal, 201, {'playerId': player, 'initialBalance': {'amount': '1000.00', 'currency': 'BRL'}})
    check(wallet['version'] == 1 and wallet['balance'] == {'amount': '1000.00', 'currency': 'BRL'}, 'opening differs from exercise')
    external = 'exercise-' + uuid.uuid4().hex
    body = {'providerId': 'provider-a', 'externalTransactionId': external, 'playerId': player,
            'walletId': wallet['id'], 'roundId': 'round-987', 'gameId': 'fortune-chimp', 'kind': 'BET',
            'money': {'amount': '25.00', 'currency': 'BRL'}}
    key = 'provider-a:' + external
    bet = call('POST', '/wagering/transactions', provider, 201, body, key)
    check(bet['status'] == 'PROCESSED' and bet['balance'] == {'amount': '975.00', 'currency': 'BRL'} and bet['idempotentReplay'] is False, 'BET differs from exercise')
    reconciliation = call('POST', '/wallets/' + wallet['id'] + '/reconciliation', internal, 200)
    check(set(reconciliation) == {'walletId', 'storedBalance', 'calculatedBalance', 'difference', 'consistent', 'checkedEntries'}, 'reconciliation fields differ from exercise')
    check(reconciliation['storedBalance'] == bet['balance'] == reconciliation['calculatedBalance'], 'reconciliation balances differ')
    check(reconciliation['difference'] == {'amount': '0.00', 'currency': 'BRL'} and reconciliation['consistent'] is True and reconciliation['checkedEntries'] == 2, 'reconciliation example mismatch')
    first = call('GET', '/wallets/' + wallet['id'] + '/ledger?limit=1', internal, 200)
    check(len(first['entries']) == 1 and isinstance(first['nextCursor'], str), 'first ledger page invalid')
    second = call('GET', '/wallets/' + wallet['id'] + '/ledger?limit=1&cursor=' + urllib.parse.quote(first['nextCursor'], safe=''), internal, 200)
    check(len(second['entries']) == 1 and second['nextCursor'] is None and first['entries'][0]['id'] != second['entries'][0]['id'], 'ledger pagination repeats or omits data')
    current = call('GET', '/wallets/' + wallet['id'], internal, 200)
    check(current['version'] == 2 and current['balance'] == bet['balance'], 'wallet version after BET invalid')
    win = dict(body, externalTransactionId=external + '-win', kind='WIN', money={'amount': '5.00', 'currency': 'BRL'})
    call('POST', '/wagering/transactions', provider, 201, win, external + '-win-key')
    replay = call('POST', '/wagering/transactions', provider, 200, body, key)
    check(replay['transactionId'] == bet['transactionId'] and replay['balance'] == bet['balance'] and replay['idempotentReplay'] is True, 'replay lost original result')
    current = call('GET', '/wallets/' + wallet['id'], internal, 200)
    check(current['balance'] == {'amount': '980.00', 'currency': 'BRL'} and current['version'] == 3, 'replay changed current wallet')
    result = {'status': 'PASS', 'walletId': wallet['id'], 'openingAmount': '1000.00', 'betAmount': '25.00',
              'originalBalance': '975.00', 'reconciliationEntriesBeforeWin': 2, 'ledgerPagination': 'PASS',
              'currentBalanceAfterWin': '980.00', 'replayBalance': '975.00'}
    Path(args.output).write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
    print('PASS: exercise examples, reconciliation, ledger pagination and original replay')


if __name__ == '__main__':
    main()
