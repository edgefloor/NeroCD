"""Trusted fixture Docker-exec TCP relay, not product network qualification."""
import asyncio
import contextlib
import re
import signal
import sys
from pathlib import Path


class Relay:
    def __init__(self, container_id, owner, connection_timeout=25):
        if not re.fullmatch(r"[a-f0-9]{64}", container_id):
            raise ValueError("full container ID required")
        if not re.fullmatch(r"nerocd-server-bootstrap\.[a-z0-9]+", owner):
            raise ValueError("fixture owner required")
        self.container_id = container_id
        self.owner = owner
        self.connection_timeout = connection_timeout
        self.tasks = set()
        self.children = set()
        self.server = None

    async def spawn(self, *args, **kwargs):
        # Shield acquisition so cancellation cannot orphan an unrecorded child.
        pending = asyncio.create_task(asyncio.create_subprocess_exec(*args, **kwargs))
        try:
            process = await asyncio.shield(pending)
        except asyncio.CancelledError:
            process = await pending
            self.children.add(process)
            await self.reap(process)
            raise
        self.children.add(process)
        return process

    async def reap(self, process):
        if process.returncode is None:
            with contextlib.suppress(ProcessLookupError):
                process.terminate()
        try:
            await asyncio.wait_for(process.wait(), 2)
        except asyncio.TimeoutError:
            with contextlib.suppress(ProcessLookupError):
                process.kill()
            await process.wait()
        self.children.discard(process)

    async def identity(self):
        process = await self.spawn(
            "docker", "container", "inspect", "--format",
            '{{.Id}} {{.Name}} {{index .Config.Labels "io.nerocd.server-bootstrap.owner"}}',
            self.container_id, stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.DEVNULL,
        )
        try:
            output, _ = await asyncio.wait_for(process.communicate(), 5)
            expected = f"{self.container_id} /{self.owner} {self.owner}"
            return process.returncode == 0 and output.decode().strip() == expected
        finally:
            await self.reap(process)

    def accept(self, reader, writer):
        if len(self.tasks) >= 4:
            writer.close()
            return
        task = asyncio.create_task(self.connection(reader, writer))
        self.tasks.add(task)
        task.add_done_callback(self.tasks.discard)

    async def connection(self, reader, writer):
        process = None
        try:
            async with asyncio.timeout(self.connection_timeout):
                if not await self.identity():
                    print("relay connection refused: fixture identity mismatch", flush=True)
                    return
                process = await self.spawn(
                    "docker", "exec", "--interactive", self.container_id,
                    "socat", "STDIO", "TCP:127.0.0.1:22,connect-timeout=5",
                    stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE,
                    stderr=asyncio.subprocess.DEVNULL,
                )

                async def upload():
                    while data := await reader.read(65536):
                        process.stdin.write(data)
                        await process.stdin.drain()
                    process.stdin.close()

                async def download():
                    while data := await process.stdout.read(65536):
                        writer.write(data)
                        await writer.drain()

                pumps = [asyncio.create_task(upload()), asyncio.create_task(download())]
                try:
                    await asyncio.wait(pumps, return_when=asyncio.FIRST_COMPLETED)
                finally:
                    for task in pumps:
                        task.cancel()
                    await asyncio.gather(*pumps, return_exceptions=True)
        except (asyncio.TimeoutError, ConnectionError, OSError):
            print("relay connection ended or deadline reached", flush=True)
        finally:
            if process is not None:
                await self.reap(process)
            writer.close()
            with contextlib.suppress(ConnectionError):
                await writer.wait_closed()

    async def start(self):
        if not await self.identity():
            raise RuntimeError("fixture identity mismatch before listening")
        self.server = await asyncio.start_server(self.accept, "127.0.0.1", 0)
        return self.server.sockets[0].getsockname()[1]

    async def close(self):
        if self.server is not None:
            self.server.close()
            await self.server.wait_closed()
        pending = list(self.tasks)
        for task in pending:
            task.cancel()
        await asyncio.gather(*pending, return_exceptions=True)
        self.tasks.difference_update(pending)
        for process in list(self.children):
            await self.reap(process)
        print(f"relay stopped; active_children={len(self.children)} active_connections={len(self.tasks)}", flush=True)

    async def serve_until(self, stop, lifetime=180):
        try:
            with contextlib.suppress(asyncio.TimeoutError):
                await asyncio.wait_for(stop.wait(), lifetime)
        finally:
            await self.close()


async def main():
    relay = Relay(sys.argv[1], sys.argv[2])
    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGTERM, signal.SIGINT):
        loop.add_signal_handler(sig, stop.set)
    try:
        port = await relay.start()
        Path(sys.argv[3]).write_text(str(port))
        print(f"fixture Docker-exec TCP relay endpoint=127.0.0.1:{port}", flush=True)
        await relay.serve_until(stop)
    finally:
        await relay.close()


if __name__ == "__main__":
    asyncio.run(main())
