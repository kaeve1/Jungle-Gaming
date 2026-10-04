"""Perfil local restrito: SQS JSON e IAM Query, SigV4 header, conta/região fixas.

Usa a implementação de canonicalização/assinatura do botocore já fixado na imagem.
Não implementa presigned URLs, STS, outros serviços ou endpoints administrativos.
Políticas IAM de SQS continuam sendo avaliadas pelo MiniStack depois da autenticação.
"""
import asyncio
import datetime as dt
import hashlib
import hmac
import json
import os
import re

from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials
from ministack.core.iam_evaluator import resolve_credential, CredentialResolutionError

AUTH_PATTERN = re.compile(
    r"AWS4-HMAC-SHA256 Credential=([A-Za-z0-9_-]+)/([0-9]{8})/us-east-1/(sqs|iam)/aws4_request, "
    r"SignedHeaders=([a-z0-9;-]+), Signature=([0-9a-f]{64})"
)
MAX_BODY = 1024 * 1024


class Denied(Exception):
    pass


def verify(method, path, query, headers, body, root_key, now=None):
    if method != "POST" or path != "/" or query:
        raise Denied()
    match = AUTH_PATTERN.fullmatch(headers.get("authorization", ""))
    if not match:
        raise Denied()
    key, date, service, signed, supplied = match.groups()
    # Não permitir os aliases root embutidos do MiniStack, mesmo com segredo certo.
    if key == "test" or key.isdecimal() or (service == "iam" and key != root_key):
        raise Denied()
    names = signed.split(";")
    if names != sorted(set(names)) or not {"host", "x-amz-date", "content-type"}.issubset(names):
        raise Denied()
    if any(name not in headers for name in names) or "x-amz-security-token" in headers:
        raise Denied()
    if any(k.startswith("x-amz-") and k not in names for k in headers):
        raise Denied()
    if service == "sqs":
        if not headers.get("x-amz-target", "").startswith("AmazonSQS.") or "x-amz-target" not in names:
            raise Denied()
        if not headers["content-type"].startswith("application/x-amz-json-1.0"):
            raise Denied()
    elif "x-amz-target" in headers or not headers["content-type"].startswith("application/x-www-form-urlencoded"):
        raise Denied()
    timestamp = headers["x-amz-date"]
    try:
        at = dt.datetime.strptime(timestamp, "%Y%m%dT%H%M%SZ").replace(tzinfo=dt.timezone.utc)
    except ValueError as exc:
        raise Denied() from exc
    now = now or dt.datetime.now(dt.timezone.utc)
    if timestamp[:8] != date or abs((now - at).total_seconds()) > 300:
        raise Denied()
    digest = hashlib.sha256(body).hexdigest()
    if "x-amz-content-sha256" in headers and headers["x-amz-content-sha256"] != digest:
        raise Denied()
    credential = resolve_credential(key, "000000000000", None)
    if isinstance(credential, CredentialResolutionError):
        raise Denied()
    if credential.principal_type == "Root" and key != root_key:
        raise Denied()
    request = AWSRequest(method=method, url="http://" + headers["host"] + path,
                         data=body, headers={name: headers[name] for name in names})
    request.context["timestamp"] = timestamp
    signer = SigV4Auth(Credentials(key, credential.secret_access_key), service, "us-east-1")
    canonical = signer.canonical_request(request)
    expected = signer.signature(signer.string_to_sign(request, canonical), request)
    if not hmac.compare_digest(expected, supplied):
        raise Denied()


class StrictAWSAuth:
    def __init__(self, app):
        self.app = app
        self.root = os.environ.get("AWS_ACCESS_KEY_ID", "")
        if not self.root or self.root == "test" or self.root.isdecimal() or not os.environ.get("AWS_SECRET_ACCESS_KEY"):
            raise ValueError("explicit non-default admin credentials required")

    async def __call__(self, scope, receive, send):
        if scope["type"] == "lifespan":
            return await self.app(scope, receive, send)
        if scope["type"] != "http":
            await send({"type": "websocket.close", "code": 1008})
            return
        try:
            headers = {}
            for name, value in scope.get("headers", []):
                name = name.decode("ascii").lower()
                if name in headers:
                    raise Denied()
                headers[name] = value.decode("latin1")
            body = bytearray()
            async with asyncio.timeout(10):
                while True:
                    part = await receive()
                    if part["type"] != "http.request":
                        raise Denied()
                    body.extend(part.get("body", b""))
                    if len(body) > MAX_BODY:
                        raise Denied()
                    if not part.get("more_body", False):
                        break
            body = bytes(body)
            verify(scope["method"], scope["path"], scope.get("query_string", b""), headers, body, self.root)
        except (Denied, ValueError, UnicodeError, TimeoutError):
            payload = json.dumps({"__type": "AccessDeniedException", "message": "Request authentication failed"}).encode()
            await send({"type": "http.response.start", "status": 403,
                        "headers": [(b"content-type", b"application/x-amz-json-1.0")]})
            await send({"type": "http.response.body", "body": payload})
            return
        delivered = False

        async def replay():
            nonlocal delivered
            if not delivered:
                delivered = True
                return {"type": "http.request", "body": body, "more_body": False}
            return await receive()

        await self.app(scope, replay, send)
