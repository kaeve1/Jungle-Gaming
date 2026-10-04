"""Entrada única do broker local: não há listener que ignore a validação."""
import os
import ministack.app as server
from strict_auth import StrictAWSAuth
from durable_state import install

if os.environ.get("AUTH", "").lower() != "true":
    raise SystemExit("AUTH=true is required")
install(server)
server.app = StrictAWSAuth(server.app)
server.main()
