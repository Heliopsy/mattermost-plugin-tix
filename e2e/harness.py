# SPDX-License-Identifier: AGPL-3.0-only
"""Starts tix and Mattermost in podman containers, seeds them and drives them over HTTP."""

import json
import os
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

from ws import EventStream

PREFIX = os.environ.get("E2E_PREFIX", "tix-e2e")
ENGINE = os.environ.get("E2E_ENGINE", "podman")
TIX_IMAGE = os.environ.get("E2E_TIX_IMAGE", "ghcr.io/heliopsy/tix:0.62.0")
MM_IMAGE = os.environ.get("E2E_MM_IMAGE", "docker.io/mattermost/mattermost-preview:11.11.4")
PLUGIN_ID = "cc.heliopsy.tix"

VOL = PREFIX + "-tixdata"
TIX = PREFIX + "-tix"
MM = PREFIX + "-mm"
TIX_PORT_IN = 8080

# The scopes the plugin's admin token needs: its own plus every scope it hands out.
ADMIN_SCOPES = [
    "token:admin", "user:admin", "tenant:admin", "webhook:admin",
    "task:read", "task:write", "task:transition", "task:claim", "project:read",
    "workflow:read", "comment:write", "artifact:write", "event:subscribe", "audit:read",
]
PASTE_SCOPES = ["task:read", "task:write", "task:transition", "project:read", "workflow:read", "comment:write"]

# A workflow with a transition that needs a comment; the demo seed's "default" workflow has none.
REVIEW_WORKFLOW = {
    "key": "review",
    "name": "Review",
    "definition": {
        "initial": "triage",
        "states": [
            {"key": "triage", "label": "Triage", "category": "todo"},
            {"key": "working", "label": "Working", "category": "in_progress"},
            {"key": "review", "label": "In review", "category": "in_progress"},
            {"key": "shipped", "label": "Shipped", "category": "done", "terminal": True},
            {"key": "dropped", "label": "Dropped", "category": "cancelled", "terminal": True},
        ],
        "transitions": [
            {"from": "triage", "to": "working"},
            {"from": "working", "to": "review"},
            {"from": "review", "to": "working"},
            {"from": "review", "to": "shipped", "requires_comment": True},
            {"from": "triage", "to": "dropped"},
            {"from": "working", "to": "dropped"},
            {"from": "shipped", "to": "triage"},
            {"from": "dropped", "to": "triage"},
        ],
    },
}

PASSWORD = "E2e-password-1"


def log(msg):
    print(f"[e2e] {msg}", flush=True)


def run(args, input=None, check=True, quiet=False):
    """Runs a command and returns its stdout."""
    p = subprocess.run(args, input=input, capture_output=True, text=True)
    if check and p.returncode != 0:
        raise RuntimeError(f"{' '.join(args[:4])}... failed ({p.returncode}): {p.stderr.strip() or p.stdout.strip()}")
    if not quiet and p.returncode != 0:
        log(f"(ignored) {' '.join(args[:4])}: {p.stderr.strip()}")
    return p.stdout


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class HTTPError(Exception):
    def __init__(self, status, body):
        super().__init__(f"HTTP {status}: {body[:300]}")
        self.status = status
        self.body = body


def http(method, url, body=None, headers=None, timeout=30):
    """Sends a JSON request and returns (status, parsed body or text). Never raises on an HTTP status."""
    data = None
    hdrs = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode()
        hdrs["Content-Type"] = "application/json"
    hdrs.update(headers or {})
    req = urllib.request.Request(url, data=data, method=method, headers=hdrs)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            status, raw, h = r.status, r.read().decode(), r.headers
    except urllib.error.HTTPError as e:
        status, raw, h = e.code, e.read().decode(), e.headers
    try:
        parsed = json.loads(raw) if raw else None
    except ValueError:
        parsed = raw
    return status, parsed, h


class Env:
    """The containers, their ports and the seeded accounts."""

    def __init__(self, bundle, keep_images=False):
        self.bundle = bundle
        self.keep_images = keep_images
        self.tmp = tempfile.mkdtemp(prefix=PREFIX + "-")
        self.mm_port = int(os.environ.get("E2E_MM_PORT") or free_port())
        self.tix_port = int(os.environ.get("E2E_TIX_PORT") or free_port())
        self.mm_url = f"http://127.0.0.1:{self.mm_port}"
        self.tix_url = f"http://127.0.0.1:{self.tix_port}"
        self.site_url = "http://localhost:8065"
        self.tix_internal = f"http://127.0.0.1:{TIX_PORT_IN}"
        self.pulled = []
        self.streams = []
        self.webhook_secret = secrets.token_hex(24)
        self.encryption_key = secrets.token_hex(16)

    # --- lifecycle -------------------------------------------------------------------------

    def ensure_image(self, image):
        if subprocess.run([ENGINE, "image", "exists", image]).returncode != 0:
            log(f"pulling {image}")
            run([ENGINE, "pull", "-q", image])
            self.pulled.append(image)

    def tix(self, *args, input=None):
        """Runs the tix CLI against the seeded database in a throwaway container."""
        return run([ENGINE, "run", "--rm", "-i", "--name", f"{PREFIX}-cli-{secrets.token_hex(3)}",
                    "-v", f"{VOL}:/data", TIX_IMAGE, "--no-discovery", "--db", "/data/tix.db", *args], input=input)

    def up(self):
        self.down(quiet=True)
        self.ensure_image(TIX_IMAGE)
        self.ensure_image(MM_IMAGE)
        run([ENGINE, "volume", "create", VOL])
        self.seed_tix()
        # tix and Mattermost share one network namespace (tix joins the Mattermost container's), so
        # each reaches the other on 127.0.0.1. tix refuses plain-http webhook targets except on
        # loopback, and Mattermost posts interactive actions back to its SiteURL, so both see
        # http://localhost. Only the two published ports touch the host; works with docker too.
        run([ENGINE, "run", "-d", "--name", MM,
             "-p", f"127.0.0.1:{self.mm_port}:8065", "-p", f"127.0.0.1:{self.tix_port}:{TIX_PORT_IN}",
             "-e", f"MM_SERVICESETTINGS_SITEURL={self.site_url}",
             "-e", "MM_PLUGINSETTINGS_ENABLEUPLOADS=true",
             "-e", "MM_SERVICESETTINGS_ENABLELOCALMODE=true",
             "-e", "MM_SERVICESETTINGS_ALLOWEDUNTRUSTEDINTERNALCONNECTIONS=127.0.0.1 localhost",
             "-e", "MM_RATELIMITSETTINGS_ENABLE=false",
             "-e", "MM_EMAILSETTINGS_REQUIREEMAILVERIFICATION=false",
             "-e", "MM_SERVICESETTINGS_ENABLEUSERACCESSTOKENS=true",
             "-e", "MM_LOGSETTINGS_CONSOLELEVEL=WARN",
             MM_IMAGE])
        run([ENGINE, "run", "-d", "--name", TIX, "--network", f"container:{MM}", "-v", f"{VOL}:/data",
             "-e", "TIX_WEBHOOKS_ALLOW_PRIVATE_TARGETS=true", "-e", "TIX_SERVER_EGRESS_ALLOW_PRIVATE=true",
             TIX_IMAGE, "--no-discovery", "serve", "--insecure-no-tls"])
        self.wait_http(self.tix_url + "/healthz", "tix", 60)
        self.wait_http(self.mm_url + "/api/v4/system/ping", "Mattermost", 240)
        self.wait_mmctl()
        self.seed_mm()

    def down(self, quiet=False):
        for s in self.streams:
            s.close()
        self.streams = []
        names = run([ENGINE, "ps", "-a", "--format", "{{.Names}}"], check=False, quiet=True).split()
        mine = [n for n in names if n.startswith(PREFIX + "-")]
        # tix shares the Mattermost container's network namespace, so it has to go first.
        for group in ([n for n in mine if n != MM], [n for n in mine if n == MM]):
            if group:
                run([ENGINE, "rm", "-f", "-v", "-t", "2", *group], check=False, quiet=quiet)
        run([ENGINE, "volume", "rm", "-f", VOL], check=False, quiet=True)
        if not self.keep_images:
            for image in self.pulled:
                run([ENGINE, "rmi", "-f", image], check=False, quiet=quiet)
            self.pulled = []
        shutil.rmtree(self.tmp, ignore_errors=True)

    def wait_http(self, url, what, timeout):
        log(f"waiting for {what}")
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                status, _, _ = http("GET", url, timeout=5)
                if status == 200:
                    return
            except OSError:
                pass
            time.sleep(1)
        self.dump_logs()
        raise RuntimeError(f"{what} did not become healthy within {timeout}s")

    def wait_mmctl(self):
        deadline = time.time() + 60
        while time.time() < deadline:
            p = subprocess.run([ENGINE, "exec", MM, "mmctl", "--local", "system", "version"], capture_output=True)
            if p.returncode == 0:
                return
            time.sleep(1)
        raise RuntimeError("mmctl local mode did not come up")

    def dump_logs(self):
        for c in (TIX, MM):
            out = subprocess.run([ENGINE, "logs", "--tail", "40", c], capture_output=True, text=True)
            print(f"----- {c} logs -----\n{out.stdout}{out.stderr}", file=sys.stderr)

    def mmctl(self, *args, check=True):
        return run([ENGINE, "exec", MM, "mmctl", "--local", "--json", *args], check=check)

    # --- seeding ---------------------------------------------------------------------------

    def seed_tix(self):
        log("seeding tix")
        self.tix("demo", "seed")
        for handle, name in (("alice", "Alice Archer"), ("bob", "Bob Baker"), ("carol", "Carol Cole")):
            self.tix("user", "create", f"{handle}@e2e.test", "--handle", handle, "--display-name", name,
                     "--role", "member", "--password", PASSWORD)
        self.tix("workflow", "put", "-f", "-", input=json.dumps(REVIEW_WORKFLOW))
        self.tix("project", "create", "rev", "Review board", "--workflow", "review")
        self.admin_token = self._token("mattermost-plugin", "nadia", ADMIN_SCOPES)
        self.carol_token = self._token("carol-pasted", "carol", PASTE_SCOPES)

    def _token(self, name, actor, scopes):
        args = ["token", "create", name, "--actor", actor]
        for s in scopes:
            args += ["--scope", s]
        out = json.loads(self.tix(*args, "-o", "json"))
        for k in ("token", "secret", "plaintext"):
            if out.get(k):
                return out[k]
        raise RuntimeError("no secret in the token create output: " + ",".join(out))

    def seed_mm(self):
        log("seeding Mattermost")
        users = [
            ("admin", "admin@e2e.test", True, True),
            ("alice", "alice@e2e.test", True, False),
            ("bob", "bob@e2e.test", True, False),
            ("carol", "carol@e2e.test", False, False),
            ("eve", "eve@e2e.test", True, False),
        ]
        for name, email, verified, sysadmin in users:
            args = ["user", "create", "--username", name, "--email", email, "--password", PASSWORD]
            if verified:
                args.append("--email-verified")
            if sysadmin:
                args.append("--system-admin")
            self.mmctl(*args)
        self.mmctl("team", "create", "--name", "e2e", "--display-name", "E2E")
        self.mmctl("team", "users", "add", "e2e", "admin", "alice", "bob", "carol", "eve")
        # By default every channel member may manage a public channel's properties, which is what
        # /tix subscribe checks. Take it away so a plain member can be refused.
        self.mmctl("permissions", "remove", "channel_user", "manage_public_channel_properties")
        self.mmctl("permissions", "add", "channel_admin", "manage_public_channel_properties")

        self.plugin_cfg = {
            "tixurl": self.tix_internal,
            "admintoken": self.admin_token,
            "encryptionkey": self.encryption_key,
            "webhooksecret": self.webhook_secret,
            "unfurlrefs": True,
        }
        self.configure_plugin()
        run([ENGINE, "cp", self.bundle, f"{MM}:/tmp/plugin.tar.gz"])
        self.mmctl("plugin", "add", "--force", "/tmp/plugin.tar.gz")
        self.mmctl("plugin", "enable", PLUGIN_ID)
        # Interactive actions and dialogs post back to SiteURL, which must resolve inside the container.
        deadline = time.time() + 60
        while time.time() < deadline:
            status, _, _ = http("GET", f"{self.mm_url}/plugins/{PLUGIN_ID}/api/v1/me")
            if status == 401:
                return
            time.sleep(1)
        raise RuntimeError("the plugin did not start")

    def configure_plugin(self, **changes):
        """Writes the plugin's settings (all of them: a patch replaces the plugin's whole map)."""
        self.plugin_cfg.update(changes)
        patch = {"PluginSettings": {"Plugins": {PLUGIN_ID: self.plugin_cfg}}}
        run([ENGINE, "exec", "-i", MM, "sh", "-c", "cat > /tmp/tix-e2e-config.json"], input=json.dumps(patch))
        self.mmctl("config", "patch", "/tmp/tix-e2e-config.json")

    def plugin_active(self):
        status, _, _ = http("GET", f"{self.mm_url}/plugins/{PLUGIN_ID}/api/v1/me")
        return status == 401

    # --- clients ---------------------------------------------------------------------------

    def login(self, username):
        status, body, headers = http("POST", self.mm_url + "/api/v4/users/login",
                                     {"login_id": username, "password": PASSWORD})
        if status != 200:
            raise RuntimeError(f"login {username}: {status} {body}")
        u = User(self, username, headers["Token"], body)
        u.stream = EventStream("127.0.0.1", self.mm_port, u.token)
        self.streams.append(u.stream)
        return u

    def tix_api(self, method, path, body=None, token=None):
        return http(method, self.tix_url + "/api/v1" + path, body,
                    {"Authorization": "Bearer " + (token or self.admin_token)})


class User:
    """A logged-in Mattermost user."""

    def __init__(self, env, username, token, me):
        self.env = env
        self.username = username
        self.token = token
        self.id = me["id"]
        self.stream = None

    def api(self, method, path, body=None, expect=None):
        status, data, _ = http(method, self.env.mm_url + "/api/v4" + path, body, {"Authorization": "Bearer " + self.token})
        if expect is not None and status != expect:
            raise HTTPError(status, json.dumps(data) if not isinstance(data, str) else data)
        return status, data

    def plugin(self, method, path, body=None):
        """Calls the plugin's web API (/plugins/<id>/api/v1/...) and returns (status, body)."""
        status, data, _ = http(method, f"{self.env.mm_url}/plugins/{PLUGIN_ID}/api/v1{path}", body,
                               {"Authorization": "Bearer " + self.token, "X-Requested-With": "XMLHttpRequest"})
        return status, data

    def cmd(self, channel, text):
        """Runs a slash command and returns the text of its (ephemeral) response."""
        _, data = self.api("POST", "/commands/execute",
                           {"channel_id": channel["id"], "team_id": channel["team_id"], "command": text}, expect=200)
        return (data or {}).get("text", "")

    def post(self, channel, message, root_id="", props=None):
        body = {"channel_id": channel["id"], "message": message, "root_id": root_id}
        if props:
            body["props"] = props
        _, p = self.api("POST", "/posts", body, expect=201)
        return p

    def get_post(self, post_id):
        _, p = self.api("GET", f"/posts/{post_id}", expect=200)
        return p

    def posts(self, channel_id, since_ms=0):
        _, data = self.api("GET", f"/channels/{channel_id}/posts?per_page=100", expect=200)
        out = [data["posts"][i] for i in data["order"]]
        return [p for p in out if p["create_at"] >= since_ms]

    def click(self, post_id, action_id):
        return self.api("POST", f"/posts/{post_id}/actions/{action_id}", {})

    def submit_dialog(self, dialog, channel, submission, state=None):
        body = {
            "url": dialog["url"],
            "callback_id": dialog["dialog"].get("callback_id", ""),
            "state": dialog["dialog"].get("state", "") if state is None else state,
            "channel_id": channel["id"],
            "team_id": channel["team_id"],
            "submission": submission,
            "cancelled": False,
        }
        return self.api("POST", "/actions/dialogs/submit", body)

    def direct_channel(self, other_id):
        status, ch = self.api("POST", "/channels/direct", [self.id, other_id])
        if status not in (200, 201):
            raise HTTPError(status, str(ch))
        return ch
