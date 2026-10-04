"""Crash-safe checkpoints for the pinned MiniStack's local SQS/IAM profile.

Handlers run on one asyncio event loop. Snapshot/serialization/fsync is
synchronous and contains no await: another request cannot mutate the state
during the checkpoint. Long polling itself holds no persistence lock.
This intentionally trades throughput for durability in the local emulator.
"""
import json
import os
from pathlib import Path
import tempfile

from ministack.core import persistence


class DurableState:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)

    def save(self, service, data):
        # Keep the upstream state format so existing local volumes can migrate.
        payload = json.dumps({
            "__ministack_format__": persistence._state_format_version(service),
            "payload": data,
        }, default=persistence._json_default).encode("utf-8")
        fd, temporary = tempfile.mkstemp(prefix=service + ".", suffix=".tmp", dir=self.directory)
        try:
            with os.fdopen(fd, "wb") as stream:
                stream.write(payload)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, self.directory / (service + ".json"))
            directory_fd = os.open(self.directory, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory_fd)
            finally:
                os.close(directory_fd)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)

    def load(self, service):
        path = self.directory / (service + ".json")
        if not path.exists():
            return None
        # Corrupt/incompatible state must fail startup, never become an empty
        # queue or identity store. Only a genuinely absent file is fresh state.
        data = json.loads(path.read_text(encoding="utf-8"), object_hook=persistence._json_object_hook)
        if not isinstance(data, dict):
            raise ValueError("invalid broker checkpoint")
        if "__ministack_format__" in data:
            version = data["__ministack_format__"]
            if type(version) is not int or not 1 <= version <= persistence._state_format_version(service):
                raise ValueError("unsupported broker checkpoint version")
            data = data["payload"]
        if not isinstance(data, dict):
            raise ValueError("invalid broker checkpoint payload")
        return data

    def handler(self, service, module):
        original = module.handle_request

        async def handle(*args, **kwargs):
            response = await original(*args, **kwargs)
            # Also checkpoint Receive/Delete/visibility/redrive and IAM changes,
            # not only SendMessage. Failed requests may have partial effects.
            # On disk error no response is returned to the ASGI gateway, so a
            # success/ACK cannot escape without a durable checkpoint.
            self.save(service, module.get_state())
            return response

        return handle


def install(server):
    if not persistence.PERSIST_STATE:
        raise RuntimeError("PERSIST_STATE=1 is required for the durable broker")
    from ministack.services import iam, sqs

    state = DurableState(persistence.STATE_DIR)
    modules = {"iam": iam, "sqs": sqs}

    def restore():
        for service, module in modules.items():
            data = state.load(service)
            if data is not None:
                module.load_persisted_state(data)
            # Test volume writability before accepting any requests, including
            # the first startup. Restore errors propagate through lifespan.
            state.save(service, module.get_state())

    # Validate before opening the listener; upstream otherwise logs some load
    # errors and continues with fresh state. Restore once during lifespan.
    for service in modules:
        state.load(service)
    server._load_persisted_state = restore
    upstream_save = persistence.save_state

    def save(service, data):
        if service in modules:
            state.save(service, data)
        else:
            upstream_save(service, data)

    persistence.save_state = save
    for service, module in modules.items():
        module.handle_request = state.handler(service, module)
