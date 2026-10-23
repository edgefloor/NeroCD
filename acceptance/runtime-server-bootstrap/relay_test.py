"""Actual local child/socket tests with Docker argv intercepted at the boundary."""
import asyncio
import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("fixture_relay", Path(__file__).with_name("relay.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class RelayTest(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.owner = "nerocd-server-bootstrap.test"
        self.container_id = "a" * 64
        self.mode = "echo"
        self.calls = []
        self.processes = []
        actual_spawn = asyncio.create_subprocess_exec

        async def spawn(*args, **kwargs):
            self.calls.append(args)
            self.assertEqual(args[0], "docker")
            if args[1:3] == ("container", "inspect"):
                self.assertEqual(args[-1], self.container_id)
                identity = f"{self.container_id} /{self.owner} {self.owner}"
                if self.mode == "wrong-identity":
                    identity += "other"
                code = f"print({identity!r})"
            else:
                self.assertEqual(args, ("docker", "exec", "--interactive", self.container_id,
                                        "socat", "STDIO", f"TCP:127.0.0.1:{self.relay.target_port},connect-timeout=5"))
                code = "import os; b=os.read(0,65536); os.write(1,b); os.read(0,1)"
                if self.mode == "hang":
                    code = "import time; time.sleep(30)"
            process = await actual_spawn(sys.executable, "-c", code, **kwargs)
            self.processes.append(process)
            return process

        self.patch = patch.object(asyncio, "create_subprocess_exec", spawn)
        self.patch.start()
        self.relay = module.Relay(self.container_id, self.owner, connection_timeout=0.2)

    async def asyncTearDown(self):
        await self.relay.close()
        self.patch.stop()
        self.assertTrue(all(p.returncode is not None for p in self.processes))
        self.assertFalse(self.relay.children)
        self.assertFalse(self.relay.tasks)

    async def test_echo_and_listener_shutdown(self):
        port = await self.relay.start()
        reader, writer = await asyncio.open_connection("127.0.0.1", port)
        writer.write(b"actual local socket")
        await writer.drain()
        self.assertEqual(await asyncio.wait_for(reader.readexactly(19), 2), b"actual local socket")
        writer.close()
        await writer.wait_closed()
        await self.relay.close()
        with self.assertRaises(OSError):
            await asyncio.open_connection("127.0.0.1", port)

    async def test_https_profile_preserves_identity_and_bounded_transport(self):
        self.relay = module.Relay(self.container_id, self.owner, connection_timeout=0.2, profile="onboarding-https")
        port = await self.relay.start()
        self.assertEqual(port, 19443)
        self.assertEqual(self.relay.target_port, 19443)
        reader, writer = await asyncio.open_connection("127.0.0.1", port)
        writer.write(b"fixed HTTPS transport")
        await writer.drain()
        self.assertEqual(await asyncio.wait_for(reader.readexactly(21), 2), b"fixed HTTPS transport")
        writer.close()
        await writer.wait_closed()
        self.mode = "wrong-identity"
        reader, writer = await asyncio.open_connection("127.0.0.1", port)
        self.assertEqual(await asyncio.wait_for(reader.read(), 2), b"")
        writer.close()
        await writer.wait_closed()
        self.assertEqual(sum(call[1] == "exec" for call in self.calls), 1)

    async def test_rejects_arbitrary_transport_profile(self):
        with self.assertRaises(ValueError):
            module.Relay(self.container_id, self.owner, profile="5432")

    async def test_wrong_start_identity_never_listens_or_executes(self):
        self.mode = "wrong-identity"
        with self.assertRaises(RuntimeError):
            await self.relay.start()
        self.assertFalse(any(c[1] == "exec" for c in self.calls))

    async def test_identity_loss_before_connection_never_executes(self):
        port = await self.relay.start()
        self.mode = "wrong-identity"
        reader, writer = await asyncio.open_connection("127.0.0.1", port)
        self.assertEqual(await asyncio.wait_for(reader.read(), 2), b"")
        writer.close()
        await writer.wait_closed()
        self.assertFalse(any(c[1] == "exec" for c in self.calls))

    async def test_deadline_reaps_exec_child(self):
        port = await self.relay.start()
        self.mode = "hang"
        reader, writer = await asyncio.open_connection("127.0.0.1", port)
        self.assertEqual(await asyncio.wait_for(reader.read(), 2), b"")
        writer.close()
        await writer.wait_closed()
        self.assertFalse(self.relay.children)

    async def test_shutdown_reaps_active_children_and_limits_connections(self):
        self.relay.connection_timeout = 10
        port = await self.relay.start()
        self.mode = "hang"
        clients = [await asyncio.open_connection("127.0.0.1", port) for _ in range(5)]
        await asyncio.sleep(0.15)
        self.assertLessEqual(len(self.relay.tasks), 4)
        self.assertLessEqual(sum(c[1] == "exec" for c in self.calls), 4)
        await self.relay.close()
        for reader, writer in clients:
            self.assertEqual(await asyncio.wait_for(reader.read(), 2), b"")
            writer.close()
            await writer.wait_closed()

    async def test_global_deadline_stops_listener_and_reaps_children(self):
        port = await self.relay.start()
        self.mode = "hang"
        reader, writer = await asyncio.open_connection("127.0.0.1", port)
        await asyncio.wait_for(self.relay.serve_until(asyncio.Event(), lifetime=0.1), 3)
        self.assertEqual(await reader.read(), b"")
        writer.close()
        await writer.wait_closed()
        with self.assertRaises(OSError):
            await asyncio.open_connection("127.0.0.1", port)


if __name__ == "__main__":
    unittest.main()
