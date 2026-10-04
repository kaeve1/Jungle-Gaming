import asyncio
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from durable_state import DurableState


class DurableStateTests(unittest.TestCase):
    def test_checkpoint_roundtrip_and_permissions(self):
        with tempfile.TemporaryDirectory() as directory:
            state = DurableState(directory)
            state.save("sqs", {"messages": ["accepted"]})
            self.assertEqual(state.load("sqs"), {"messages": ["accepted"]})
            self.assertEqual((Path(directory) / "sqs.json").stat().st_mode & 0o777, 0o600)
            self.assertEqual(list(Path(directory).glob("*.tmp")), [])

    def test_no_success_before_fsync_and_previous_snapshot_survives_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            state = DurableState(directory)
            state.save("sqs", {"messages": ["old"]})

            async def send():
                return (200, {}, b"accepted")

            module = SimpleNamespace(handle_request=send, get_state=lambda: {"messages": ["old", "new"]})
            handler = state.handler("sqs", module)
            with patch("durable_state.os.fsync", side_effect=OSError("injected disk failure")):
                with self.assertRaises(OSError):
                    asyncio.run(handler())
            self.assertEqual(state.load("sqs"), {"messages": ["old"]})
            self.assertEqual(list(Path(directory).glob("*.tmp")), [])
            self.assertEqual(asyncio.run(handler())[0], 200)
            self.assertEqual(state.load("sqs"), {"messages": ["old", "new"]})

    def test_corrupt_or_newer_checkpoint_is_not_fresh_state(self):
        with tempfile.TemporaryDirectory() as directory:
            state = DurableState(directory)
            self.assertIsNone(state.load("sqs"))
            path = Path(directory) / "sqs.json"
            for content in ["{", "[]", json.dumps({"__ministack_format__": 999, "payload": {}})]:
                path.write_text(content)
                with self.assertRaises(ValueError):
                    state.load("sqs")

    def test_long_poll_does_not_block_send_checkpoint(self):
        async def scenario(state):
            waiting, release = asyncio.Event(), asyncio.Event()
            messages = []

            async def original(action):
                if action == "receive":
                    waiting.set()
                    await release.wait()
                else:
                    messages.append("accepted")
                return (200, {}, b"ok")

            module = SimpleNamespace(handle_request=original, get_state=lambda: {"messages": list(messages)})
            handler = state.handler("sqs", module)
            receiver = asyncio.create_task(handler("receive"))
            await waiting.wait()
            await asyncio.wait_for(handler("send"), 1)
            self.assertEqual(state.load("sqs"), {"messages": ["accepted"]})
            release.set()
            await receiver

        with tempfile.TemporaryDirectory() as directory:
            asyncio.run(scenario(DurableState(directory)))


if __name__ == "__main__":
    unittest.main()
