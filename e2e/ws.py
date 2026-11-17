# SPDX-License-Identifier: AGPL-3.0-only
"""A minimal WebSocket client (RFC 6455, text frames only) for the Mattermost event stream.

The suite needs the events Mattermost only sends over the socket: ephemeral posts that a plugin
sends with SendEphemeralPost and the open_dialog event behind an interactive dialog.
"""

import base64
import json
import os
import socket
import struct
import threading
import time


class EventStream:
    """Collects the events of one user's Mattermost WebSocket in the background."""

    def __init__(self, host, port, token):
        self.events = []
        self.lock = threading.Lock()
        self.closed = False
        self.sock = socket.create_connection((host, port), timeout=10)
        key = base64.b64encode(os.urandom(16)).decode()
        req = (
            "GET /api/v4/websocket HTTP/1.1\r\n"
            f"Host: {host}:{port}\r\n"
            "Upgrade: websocket\r\nConnection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n"
            f"Authorization: Bearer {token}\r\n\r\n"
        )
        self.sock.sendall(req.encode())
        head = b""
        while b"\r\n\r\n" not in head:
            chunk = self.sock.recv(1)
            if not chunk:
                raise ConnectionError("websocket handshake closed")
            head += chunk
        if b" 101 " not in head.split(b"\r\n", 1)[0]:
            raise ConnectionError("websocket handshake refused: " + head.split(b"\r\n", 1)[0].decode())
        self.sock.settimeout(None)
        self.thread = threading.Thread(target=self._read, daemon=True)
        self.thread.start()

    def _recv(self, n):
        buf = b""
        while len(buf) < n:
            chunk = self.sock.recv(n - len(buf))
            if not chunk:
                raise ConnectionError("websocket closed")
            buf += chunk
        return buf

    def _send(self, opcode, payload=b""):
        mask = os.urandom(4)
        head = bytes([0x80 | opcode])
        n = len(payload)
        if n < 126:
            head += bytes([0x80 | n])
        elif n < 65536:
            head += bytes([0x80 | 126]) + struct.pack(">H", n)
        else:
            head += bytes([0x80 | 127]) + struct.pack(">Q", n)
        masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
        self.sock.sendall(head + mask + masked)

    def _read(self):
        message = b""
        try:
            while not self.closed:
                b0, b1 = self._recv(2)
                opcode, n = b0 & 0x0F, b1 & 0x7F
                if n == 126:
                    (n,) = struct.unpack(">H", self._recv(2))
                elif n == 127:
                    (n,) = struct.unpack(">Q", self._recv(8))
                payload = self._recv(n) if n else b""
                if opcode == 0x9:
                    self._send(0xA, payload)
                elif opcode == 0x8:
                    break
                elif opcode in (0x0, 0x1):
                    message += payload
                    if b0 & 0x80:
                        try:
                            ev = json.loads(message)
                        except ValueError:
                            ev = None
                        message = b""
                        if isinstance(ev, dict) and "event" in ev:
                            with self.lock:
                                self.events.append(ev)
        except (OSError, ConnectionError):
            pass

    def wait(self, pred, timeout=15, after=0):
        """Returns the first event from index `after` on that pred accepts, or None after timeout."""
        deadline = time.time() + timeout
        while time.time() < deadline:
            with self.lock:
                for ev in self.events[after:]:
                    if pred(ev):
                        return ev
            time.sleep(0.2)
        return None

    def mark(self):
        """Is the index to pass as `after` to see only events from now on."""
        with self.lock:
            return len(self.events)

    def close(self):
        self.closed = True
        try:
            self._send(0x8)
        except OSError:
            pass
        try:
            self.sock.close()
        except OSError:
            pass


def ephemeral_text(ev):
    """Is the message of an ephemeral_message event, or None."""
    if ev.get("event") != "ephemeral_message":
        return None
    try:
        return json.loads(ev["data"]["post"]).get("message", "")
    except (KeyError, ValueError):
        return None


def dialog_of(ev):
    """Is the open_dialog request of an event, or None."""
    if ev.get("event") != "open_dialog":
        return None
    try:
        return json.loads(ev["data"]["dialog"])
    except (KeyError, ValueError):
        return None
