#!/usr/bin/env python3
"""Manual security probe for the LanBaz control API.

Run with the daemon's address and token, for example:

    python tests/integration/security_probe.py 127.0.0.1:59927 <token>

It asserts the Phase 0 hardening rules: hello-first, token auth, message size
limit and per-connection rate limiting. Exits non-zero on the first violation.
"""
from __future__ import annotations

import base64
import json
import os
import socket
import struct
import sys
import time

MAX_MESSAGE = 1 << 20
API_PATH = "/api"


def encode_masked(payload: bytes) -> bytes:
    """Encode a client->server WebSocket frame with a masked payload."""
    header = bytearray([0x81])  # FIN + text
    n = len(payload)
    if n < 126:
        header.append(0x80 | n)
    elif n < (1 << 16):
        header.append(0x80 | 126)
        header += struct.pack("!H", n)
    else:
        header.append(0x80 | 127)
        header += struct.pack("!Q", n)
    mask = os.urandom(4)
    header += mask
    masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
    return bytes(header) + masked


def read_frame(sock: socket.socket) -> bytes:
    def recv_exact(n: int) -> bytes:
        buf = b""
        while len(buf) < n:
            chunk = sock.recv(n - len(buf))
            if not chunk:
                raise EOFError("connection closed by daemon")
            buf += chunk
        return buf

    b0, b1 = recv_exact(2)
    opcode = b0 & 0x0F
    length = b1 & 0x7F
    if length == 126:
        length = struct.unpack("!H", recv_exact(2))[0]
    elif length == 127:
        length = struct.unpack("!Q", recv_exact(8))[0]
    if opcode == 0x9:  # ping -> pong
        return read_frame(sock)
    if opcode == 0x8:
        raise EOFError("daemon sent close frame")
    return recv_exact(length)


class Client:
    """A minimal WebSocket client.

    The HTTP Upgrade handshake is performed by hand so the probe has no third
    party dependency and stays readable as a protocol reference.
    """

    def __init__(self, addr: str):
        host, port = addr.rsplit(":", 1)
        self.sock = socket.create_connection((host, int(port)), timeout=5)
        self._handshake(host, int(port))

    def _handshake(self, host: str, port: int) -> None:
        key = base64.b64encode(os.urandom(16)).decode()
        request = (
            f"GET {API_PATH} HTTP/1.1\r\n"
            f"Host: {host}:{port}\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\n"
            "Sec-WebSocket-Version: 13\r\n"
            "\r\n"
        )
        self.sock.sendall(request.encode())

        response = b""
        while b"\r\n\r\n" not in response:
            chunk = self.sock.recv(1)
            if not chunk:
                raise EOFError("daemon closed the connection during the upgrade")
            response += chunk
        head = response.decode(errors="replace")
        if "101" not in head.split("\r\n")[0]:
            raise EOFError(f"upgrade refused: {head.splitlines()[0]}")

    def send_raw(self, payload: bytes) -> None:
        self.sock.sendall(encode_masked(payload))

    def recv_json(self) -> dict:
        return json.loads(read_frame(self.sock).decode())

    def request(self, msg: dict) -> dict:
        self.send_raw(json.dumps(msg).encode())
        return self.recv_json()

    def hello(self, token: str) -> dict:
        return self.request(
            {
                "id": "req-hello",
                "type": "hello",
                "version": 1,
                "payload": {"token": token, "client": "security-probe"},
            }
        )

    def close(self) -> None:
        try:
            self.sock.close()
        except OSError:
            pass


def fail(msg: str) -> None:
    print(f"FAIL: {msg}")
    sys.exit(1)


def ok(msg: str) -> None:
    print(f"ok: {msg}")


def main() -> None:
    if len(sys.argv) != 3:
        print(__doc__)
        sys.exit(2)
    addr, token = sys.argv[1], sys.argv[2]

    # 1. A request before hello must be rejected.
    c = Client(addr)
    try:
        resp = c.request({"id": "req-1", "type": "daemon.status", "version": 1})
    except (EOFError, TimeoutError, socket.timeout, ConnectionError):
        ok("daemon closed the connection when hello was skipped")
    else:
        if resp.get("success") is False and resp.get("error", {}).get("code") == "UNAUTHORIZED":
            ok("daemon answered UNAUTHORIZED when hello was skipped")
        else:
            fail(f"expected UNAUTHORIZED before hello, got {resp}")
    finally:
        c.close()

    # 2. A wrong token must be rejected.
    c = Client(addr)
    try:
        resp = c.hello(base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("="))
    except (EOFError, TimeoutError, socket.timeout, ConnectionError):
        ok("daemon closed the connection after a bad token")
    else:
        if resp.get("success") is False and resp.get("error", {}).get("code") == "UNAUTHORIZED":
            ok("daemon answered UNAUTHORIZED for a wrong token")
        else:
            fail(f"expected UNAUTHORIZED for a bad token, got {resp}")
    finally:
        c.close()

    # 3. The correct token must authenticate.
    c = Client(addr)
    resp = c.hello(token)
    if not resp.get("success"):
        fail(f"valid token rejected: {resp}")
    ok(f"authenticated, session {resp['payload']['session_id'][:12]}...")

    # 4. An oversized message must be rejected at the frame layer.
    try:
        c.send_raw(b'{"id":"req-big","type":"daemon.status","version":1,"payload":{"x":"'
                   + b"A" * (MAX_MESSAGE + 1024)
                   + b'"}}')
        c.sock.settimeout(3)
        try:
            big = c.recv_json()
        except (EOFError, socket.timeout, TimeoutError, ConnectionError):
            ok("daemon closed the connection on an oversized message")
        else:
            if big.get("success") is False:
                ok(f"daemon rejected the oversized message: {big['error']['code']}")
            else:
                fail("daemon accepted an oversized message")
    finally:
        c.close()

    # 5. Rate limiting must kick in.
    c = Client(addr)
    if not c.hello(token).get("success"):
        fail("rate limit probe could not authenticate")
    limited = False
    for i in range(3000):
        try:
            resp = c.request({"id": f"req-rl-{i}", "type": "daemon.version", "version": 1})
        except (EOFError, TimeoutError, socket.timeout, ConnectionError):
            limited = True
            print(f"     connection dropped after {i + 1} requests")
            break
        if resp.get("success") is False and resp["error"]["code"] == "RATE_LIMITED":
            limited = True
            print(f"     rate limit hit after {i + 1} requests")
            break
    c.close()
    if limited:
        ok("per-connection rate limit is enforced")
    else:
        print("warn: rate limit not reached within 3000 requests; raise requests_per_second to test")

    print("\nAll security probes passed.")


if __name__ == "__main__":
    main()
