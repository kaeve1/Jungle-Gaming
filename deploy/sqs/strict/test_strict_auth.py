import datetime as dt
import unittest
from types import SimpleNamespace
from unittest.mock import patch
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials
from strict_auth import verify, Denied


class SignatureTests(unittest.TestCase):
    def request(self, key="AKIAEXAMPLE", secret="correct"):
        body = b'{"QueueName":"example.fifo"}'
        r = AWSRequest(method="POST", url="http://sqs:4566/", data=body,
                       headers={"Content-Type": "application/x-amz-json-1.0", "X-Amz-Target": "AmazonSQS.GetQueueUrl"})
        SigV4Auth(Credentials(key, secret), "sqs", "us-east-1").add_auth(r)
        headers = {k.lower(): v for k, v in r.headers.items()}
        headers["host"] = "sqs:4566"
        return headers, body

    @patch("strict_auth.resolve_credential", return_value=SimpleNamespace(secret_access_key="correct", principal_type="User"))
    def test_valid_and_tampered_requests(self, _resolve):
        h, body = self.request()
        verify("POST", "/", b"", h, body, "admin-key")
        wrong, _ = self.request(secret="wrong")
        cases = [
            (wrong, body), ({}, body), (h, body + b" "),
            ({**h, "x-amz-target": "AmazonSQS.DeleteQueue"}, body),
            ({**h, "x-amz-content-sha256": "UNSIGNED-PAYLOAD"}, body),
        ]
        for headers, payload in cases:
            with self.subTest(headers=list(headers)):
                with self.assertRaises(Denied): verify("POST", "/", b"", headers, payload, "admin-key")
        for key in ["test", "000000000000"]:
            h2, b2 = self.request(key=key)
            with self.assertRaises(Denied): verify("POST", "/", b"", h2, b2, "admin-key")
        with self.assertRaises(Denied):
            verify("POST", "/", b"", h, body, "admin-key", dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=1))
        with self.assertRaises(Denied): verify("POST", "/_ministack/reset", b"", h, body, "admin-key")


if __name__ == "__main__":
    unittest.main()
