# SPDX-License-Identifier: AGPL-3.0-only
"""The end-to-end scenarios. Each is a function that raises on failure; they run in order and share state."""

import json
import re
import subprocess
import time

from harness import ENGINE, MM

from ws import dialog_of, ephemeral_text

SCENARIOS = []


def scenario(name, xfail=""):
    """Registers a scenario. xfail names a known gap: its failure prints XFAIL and does not fail the run,
    and a pass prints XPASS so the marker gets removed."""
    def wrap(fn):
        fn.xfail = xfail
        SCENARIOS.append((name, fn))
        return fn
    return wrap


class Fail(Exception):
    pass


def check(cond, msg):
    if not cond:
        raise Fail(msg)


def contains(text, *parts):
    for p in parts:
        check(p.lower() in (text or "").lower(), f"expected {p!r} in {text!r}")


def ref_in(text):
    m = re.search(r"\[([a-z0-9]+-\d+)\]\(", text or "", re.I)
    check(m, f"no task reference in {text!r}")
    return m.group(1).lower()


def eventually(fn, timeout=20, every=0.5, what="condition"):
    """Calls fn until it returns something truthy and returns it, or fails after timeout."""
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        last = fn()
        if last:
            return last
        time.sleep(every)
    raise Fail(f"timed out waiting for {what}")


class Suite:
    def __init__(self, env):
        self.env = env
        self.u = {}
        self.refs = {}

    # --- setup shared by the scenarios --------------------------------------------------

    def setup(self):
        e = self.env
        for name in ("admin", "alice", "bob", "carol", "eve"):
            self.u[name] = e.login(name)
        admin, alice = self.u["admin"], self.u["alice"]
        _, self.team = admin.api("GET", "/teams/name/e2e", expect=200)
        _, self.town = admin.api("GET", f"/teams/{self.team['id']}/channels/name/town-square", expect=200)
        # alice creates #feed, so she is its channel admin; bob is a plain member.
        _, self.feed = alice.api("POST", "/channels", {"team_id": self.team["id"], "name": "feed",
                                                       "display_name": "Feed", "type": "O"}, expect=201)
        for name in ("bob", "carol", "eve", "admin"):
            self.u[name].api("POST", f"/channels/{self.feed['id']}/members", {"user_id": self.u[name].id}, expect=201)
        _, bot = admin.api("GET", "/users/username/tix", expect=200)
        self.bot_id = bot["id"]

    def actor(self, handle):
        _, body, _ = self.env.tix_api("GET", "/actors?limit=200")
        for a in body.get("items", body if isinstance(body, list) else []):
            if a.get("handle") == handle:
                return a["id"]
        raise Fail(f"no tix actor {handle}")

    def task(self, ref):
        status, t, _ = self.env.tix_api("GET", f"/tasks/{ref}")
        check(status == 200, f"tix GET /tasks/{ref}: {status} {t}")
        return t

    def ephemeral_after(self, user, mark, *parts, timeout=15):
        def match(ev):
            t = ephemeral_text(ev)
            return t is not None and all(p.lower() in t.lower() for p in parts)
        ev = user.stream.wait(match, timeout, mark)
        check(ev, f"no ephemeral message with {parts} for {user.username}")
        return ephemeral_text(ev)

    def dialog_after(self, user, mark, timeout=15):
        ev = user.stream.wait(lambda x: dialog_of(x) is not None, timeout, mark)
        check(ev, f"no dialog opened for {user.username}")
        return dialog_of(ev)

    def dms(self, user, since):
        ch = user.direct_channel(self.bot_id)
        return [p for p in user.posts(ch["id"], since) if p["user_id"] == self.bot_id]

    def thread(self, user, post_id):
        _, data = user.api("GET", f"/posts/{post_id}/thread", expect=200)
        return sorted(data["posts"].values(), key=lambda p: p["create_at"])

    def bot_card(self, user, root_id, ref, timeout=15):
        """Waits for the tix bot's card reply showing ref in the thread of root_id."""
        def find():
            for p in self.thread(user, root_id):
                if p["user_id"] == self.bot_id and ref in [r.lower() for r in (p.get("props") or {}).get("tix_refs", [])]:
                    return p
            return None
        return eventually(find, timeout, what=f"a card reply for {ref}")

    def card_post(self, author, ref, channel=None, text=None):
        """Posts a message mentioning ref and returns the bot's card reply (the default reply style)."""
        p = author.post(channel or self.town, text or f"Looking at {ref} now")
        check(not (p.get("props") or {}).get("attachments"), "reply style changed the person's own message")
        card = self.bot_card(author, p["id"], ref)
        check(card.get("root_id") == p["id"], "the card is not a reply in the message's thread")
        check(card["props"].get("attachments"), "the card reply has no attachment")
        return card

    @staticmethod
    def react(user, post_id, emoji="ticket"):
        status, body = user.api("POST", "/reactions", {"user_id": user.id, "post_id": post_id, "emoji_name": emoji})
        check(status in (200, 201), f"reaction: {status} {body}")

    @staticmethod
    def button(post, name):
        for a in post["props"]["attachments"]:
            for act in a.get("actions") or []:
                if act["name"].lower() == name.lower():
                    return act
        names = [act["name"] for a in post["props"]["attachments"] for act in a.get("actions") or []]
        raise Fail(f"no button {name!r} on the card, buttons: {names}")


# --- connecting ------------------------------------------------------------------------------

@scenario("connect by email (alice)")
def _(s):
    out = s.u["alice"].cmd(s.town, "/tix connect")
    contains(out, "Connected to tix as", "matched by your email")


@scenario("connect by email (bob)")
def _(s):
    out = s.u["bob"].cmd(s.town, "/tix connect")
    contains(out, "Connected to tix as", "Bob Baker")


@scenario("connect refused for an unverified email (carol)")
def _(s):
    carol = s.u["carol"]
    m = carol.stream.mark()
    out = carol.cmd(s.town, "/tix connect")
    check("Connected" not in out, f"carol was connected: {out!r}")
    s.connect_dialog = s.dialog_after(carol, m)
    check(s.connect_dialog["dialog"]["title"] == "Connect to tix", "the token dialog did not open")
    status, body = carol.plugin("POST", "/connect")
    check(status == 409 and body.get("code") == "no_match", f"web connect: {status} {body}")
    contains(carol.cmd(s.town, "/tix me"), "connect")


@scenario("paste-token dialog: a bad token is refused")
def _(s):
    status, body = s.u["carol"].submit_dialog(s.connect_dialog, s.town, {"token": "tix_not-a-real-token"})
    check(status == 200 and (body or {}).get("errors", {}).get("token"), f"bad token accepted: {status} {body}")


@scenario("paste-token dialog: carol connects with her own token")
def _(s):
    carol = s.u["carol"]
    m = carol.stream.mark()
    status, body = carol.submit_dialog(s.connect_dialog, s.town, {"token": s.env.carol_token})
    check(status == 200 and not (body or {}).get("errors"), f"dialog submit: {status} {body}")
    s.ephemeral_after(carol, m, "Connected to tix as", "carol")
    contains(carol.cmd(s.town, "/tix me"), "Carol Cole", "carol")


@scenario("/tix me")
def _(s):
    out = s.u["alice"].cmd(s.town, "/tix me")
    contains(out, "Alice Archer", "`alice`", "task:transition", "mattermost-alice-")


# --- working tasks by command ------------------------------------------------------------

@scenario("/tix new with --assign @bob and --priority")
def _(s):
    out = s.u["alice"].cmd(s.town, "/tix new rev Review the deploy runbook --assign @bob --priority high")
    contains(out, "Created", "Review the deploy runbook", "@bob", "high")
    ref = s.refs["rev"] = ref_in(out)
    t = s.task(ref)
    check(t["assignee_actor_id"] == s.actor("bob"), f"assignee {t['assignee_actor_id']}")
    check(t["priority"] == 2, f"priority {t['priority']}")
    check(t["status"] == "triage", f"status {t['status']}")


@scenario("/tix new with quick-add syntax (!high #ops @user)")
def _(s):
    out = s.u["alice"].cmd(s.town, "/tix new infra Fix the nightly backups !high #ops @alice")
    ref = s.refs["infra"] = ref_in(out)
    t = s.task(ref)
    check(t["title"] == "Fix the nightly backups", f"title {t['title']!r}")
    check("ops" in (t.get("tags") or []), f"tags {t.get('tags')}")
    check(t["priority"] == 2, f"priority {t['priority']}")
    check(t["assignee_actor_id"] == s.actor("alice"), "the @alice in the title did not assign alice")


@scenario("/tix new refuses an unlinked @user")
def _(s):
    out = s.u["alice"].cmd(s.town, "/tix new infra Something for eve @eve")
    contains(out, "eve")
    check("Created" not in out, f"created a task for an unlinked user: {out!r}")


@scenario("/tix show")
def _(s):
    out = s.u["bob"].cmd(s.town, f"/tix show {s.refs['rev']}")
    contains(out, s.refs["rev"], "Review the deploy runbook", "Status:** Triage", "@bob", "high")


@scenario("/tix list (mine)")
def _(s):
    out = s.u["bob"].cmd(s.town, "/tix list")
    contains(out, s.refs["rev"])
    check(s.refs["infra"] not in out, "bob's list shows alice's task")


@scenario("/tix list <project>")
def _(s):
    out = s.u["alice"].cmd(s.town, "/tix list rev")
    contains(out, s.refs["rev"], "Triage")


@scenario("/tix list <project> <filter>")
def _(s):
    out = s.u["alice"].cmd(s.town, "/tix list infra tag:ops")
    contains(out, s.refs["infra"])
    out = s.u["alice"].cmd(s.town, "/tix list infra tag:no-such-tag")
    contains(out, "No tasks match")


@scenario("/tix assign me, then none")
def _(s):
    ref = s.refs["infra"]
    bob = s.u["bob"]
    contains(bob.cmd(s.town, f"/tix assign {ref} me"), "Assigned", "@bob")
    check(s.task(ref)["assignee_actor_id"] == s.actor("bob"), "not assigned to bob")
    contains(bob.cmd(s.town, f"/tix assign {ref} none"), "Unassigned")
    check(not s.task(ref).get("assignee_actor_id"), "still assigned")
    contains(bob.cmd(s.town, f"/tix assign {ref} @alice"), "Assigned", "@alice")


@scenario("/tix start, done, reopen, close, move")
def _(s):
    ref, a = s.refs["infra"], s.u["alice"]
    for cmd, state in ((f"start {ref}", "doing"), (f"done {ref}", "done"), (f"reopen {ref}", "todo"),
                       (f"close {ref} not needed", "cancelled"), (f"reopen {ref}", "todo"),
                       (f"move {ref} doing", "doing")):
        out = a.cmd(s.town, "/tix " + cmd)
        contains(out, "Moved")
        check(s.task(ref)["status"] == state, f"/tix {cmd}: status {s.task(ref)['status']}, want {state}")
    contains(a.cmd(s.town, f"/tix move {ref} doing"), "already")


@scenario("/tix move with no direct transition names the route")
def _(s):
    ref = s.refs["rev"]
    out = s.u["bob"].cmd(s.town, f"/tix move {ref} shipped")
    contains(out, "no direct move", "shortest way")
    check(s.task(ref)["status"] == "triage", "the task moved")


@scenario("requires_comment: refused without a comment, accepted with one")
def _(s):
    ref, bob = s.refs["rev"], s.u["bob"]
    contains(bob.cmd(s.town, f"/tix start {ref}"), "Moved")
    contains(bob.cmd(s.town, f"/tix move {ref} review"), "Moved")
    out = bob.cmd(s.town, f"/tix done {ref}")
    contains(out, "needs a comment")
    check(s.task(ref)["status"] == "review", "moved without a comment")
    out = bob.cmd(s.town, f"/tix done {ref} Reviewed with ops, all good")
    contains(out, "Moved", "Shipped", "comment was added")
    check(s.task(ref)["status"] == "shipped", "not shipped")


@scenario("/tix comment")
def _(s):
    ref = s.refs["infra"]
    out = s.u["bob"].cmd(s.town, f"/tix comment {ref} Checked the **logs**, disk was full")
    contains(out, "Commented on", ref)
    contains(s.u["alice"].cmd(s.town, f"/tix show {ref}"), "disk was full")


# --- task cards ---------------------------------------------------------------------------

@scenario("task card: the bot replies in the thread with the card")
def _(s):
    p = s.card_post(s.u["alice"], s.refs["infra"])
    contains(json.dumps(p["props"]["attachments"][0]), "Fix the nightly backups")
    s.card = p


@scenario("task card: the quiet period gives no second card in the channel")
def _(s):
    alice = s.u["alice"]
    p = alice.post(s.town, f"Still on {s.refs['infra']}")
    time.sleep(3)
    cards = [x for x in s.thread(alice, p["id"]) if x["user_id"] == s.bot_id]
    check(not cards, "a second card within the quiet period")


@scenario("task card: an edit that adds a reference gets a card")
def _(s):
    alice = s.u["alice"]
    p = alice.post(s.town, "Will file a task for this")
    alice.api("PUT", f"/posts/{p['id']}/patch", {"message": f"Filed as {s.refs['rev']}"}, expect=200)
    s.edit_card = s.bot_card(alice, p["id"], s.refs["rev"])
    s.edit_source = p


@scenario("task card: deleting the message removes its bot card")
def _(s):
    alice = s.u["alice"]
    alice.api("DELETE", f"/posts/{s.edit_source['id']}", expect=200)
    def gone():
        status, _ = s.u["admin"].api("GET", f"/posts/{s.edit_card['id']}")
        return status == 404
    eventually(gone, 15, what="the card to be deleted")


@scenario("card button: Assign to me")
def _(s):
    ref, bob = s.refs["infra"], s.u["bob"]
    p = s.card
    act = s.button(p, "Assign to me")
    status, _ = bob.click(p["id"], act["id"])
    check(status == 200, f"click: {status}")
    eventually(lambda: s.task(ref)["assignee_actor_id"] == s.actor("bob"), what="bob to be assigned")


@scenario("card button clicked by another user (bob moves alice's card)")
def _(s):
    ref, bob = s.refs["infra"], s.u["bob"]
    check(s.task(ref)["status"] == "doing", "precondition: doing")
    s.card = s.u["alice"].get_post(s.card["id"])
    act = s.button(s.card, "Done")
    status, body = bob.click(s.card["id"], act["id"])
    check(status == 200, f"click: {status} {body}")
    eventually(lambda: s.task(ref)["status"] == "done", what="the task to be done")
    redrawn = eventually(lambda: "Done" in json.dumps(s.u["alice"].get_post(s.card["id"])["props"]["attachments"][0].get("fields")),
                         what="the card to be redrawn")
    check(redrawn, "card not redrawn")


@scenario("card button clicked by an unlinked user is refused")
def _(s):
    ref, eve = s.refs["infra"], s.u["eve"]
    before = s.task(ref)
    p = s.u["alice"].get_post(s.card["id"])
    act = s.button(p, "Comment")
    m = eve.stream.mark()
    eve.click(p["id"], act["id"])
    s.ephemeral_after(eve, m, "/tix connect")
    after = s.task(ref)
    check(after["version"] == before["version"], "the task changed")


@scenario("card Comment button opens a dialog; the submit comments")
def _(s):
    ref, bob = s.refs["infra"], s.u["bob"]
    p = s.u["alice"].get_post(s.card["id"])
    m = bob.stream.mark()
    bob.click(p["id"], s.button(p, "Comment")["id"])
    dlg = s.dialog_after(bob, m)
    status, body = bob.submit_dialog(dlg, s.town, {"comment": ""})
    check(status == 200 and (body or {}).get("errors", {}).get("comment"), f"empty comment accepted: {status} {body}")
    m = bob.stream.mark()
    status, body = bob.submit_dialog(dlg, s.town, {"comment": "From the card dialog"})
    check(status == 200 and not (body or {}).get("error"), f"submit: {status} {body}")
    s.ephemeral_after(bob, m, "Commented on")
    _, cs, _ = s.env.tix_api("GET", f"/tasks/{ref}/comments")
    check(any(c["body"] == "From the card dialog" for c in cs["items"]), "comment not in tix")


@scenario("requires_comment card button opens a dialog; its submit moves the task")
def _(s):
    alice, bob = s.u["alice"], s.u["bob"]
    out = alice.cmd(s.town, "/tix new rev Ship the review board --assign @alice")
    ref = s.refs["rev2"] = ref_in(out)
    alice.cmd(s.town, f"/tix start {ref}")
    alice.cmd(s.town, f"/tix move {ref} review")
    p = s.card_post(alice, ref)
    m = bob.stream.mark()
    status, _ = bob.click(p["id"], s.button(p, "Shipped")["id"])
    check(status == 200, f"click: {status}")
    dlg = s.dialog_after(bob, m)
    contains(dlg["dialog"]["title"], "Move", "Shipped")
    check(s.task(ref)["status"] == "review", "moved before the dialog was submitted")
    status, body = bob.submit_dialog(dlg, s.town, {"comment": "Looks right to me"})
    check(status == 200 and not (body or {}).get("error"), f"submit: {status} {body}")
    eventually(lambda: s.task(ref)["status"] == "shipped", what="shipped")


@scenario("card dialog state cannot be pointed at another post")
def _(s):
    # A forged dialog state naming a post the user cannot read is refused.
    bob = s.u["bob"]
    _, dm = bob.api("POST", "/channels/direct", [bob.id, s.u["alice"].id])
    hidden = bob.post(dm, f"private note about {s.refs['infra']}")
    state = json.dumps({"r": s.refs["infra"], "p": hidden["id"]})
    dlg = {"url": f"{s.env.site_url}/plugins/cc.heliopsy.tix/api/v1/dialogs/card", "dialog": {"callback_id": "tix-card", "state": state}}
    status, body = s.u["carol"].submit_dialog(dlg, s.town, {"comment": "sneaky"})
    check(status == 200 and (body or {}).get("error"), f"forged state accepted: {status} {body}")


# --- webhook, feeds and DMs ---------------------------------------------------------------

@scenario("/tix webhook is for system admins only")
def _(s):
    contains(s.u["alice"].cmd(s.town, "/tix webhook setup"), "Only system admins")


@scenario("/tix webhook setup and status")
def _(s):
    admin = s.u["admin"]
    out = admin.cmd(s.town, "/tix webhook setup")
    contains(out, "Registered the tix webhook", "/plugins/cc.heliopsy.tix/hooks/webhook")
    contains(admin.cmd(s.town, "/tix webhook setup"), "Updated")
    out = admin.cmd(s.town, "/tix webhook status")
    contains(out, "(active)", "comment.added")


@scenario("/tix subscribe refused for a member who cannot manage the channel")
def _(s):
    out = s.u["bob"].cmd(s.feed, "/tix subscribe rev")
    contains(out, "Only people who can manage this channel")
    contains(s.u["alice"].cmd(s.feed, "/tix subscribe all"), "Only system admins")


@scenario("/tix subscribe by the channel admin")
def _(s):
    out = s.u["alice"].cmd(s.feed, "/tix subscribe rev all")
    contains(out, "now gets tix activity", "rev", "Everyone who can read this channel")
    contains(s.u["alice"].cmd(s.feed, "/tix subscriptions"), "rev", "created")


@scenario("feed posts: created, transitioned, commented")
def _(s):
    alice, bob = s.u["alice"], s.u["bob"]
    since = int(time.time() * 1000)
    ref = s.refs["feed"] = ref_in(alice.cmd(s.town, "/tix new rev Feed me a task"))
    alice.cmd(s.town, f"/tix start {ref}")
    bob.cmd(s.town, f"/tix comment {ref} A comment for the feed")
    want = ["created", "moved", "commented on"]

    def got():
        msgs = [p["message"] for p in alice.posts(s.feed["id"], since) if p["user_id"] == s.bot_id and ref in p["message"]]
        return msgs if all(any(w in m for m in msgs) for w in want) else None
    msgs = eventually(got, 30, what="feed posts")
    joined = "\n".join(msgs)
    contains(joined, "Triage → Working", "A comment for the feed")
    check("@alice" not in joined and "@bob" not in joined, "a feed post pings people")


@scenario("settings: detailed feeds and ISO dates, switched with mmctl config patch")
def _(s):
    alice = s.u["alice"]
    tomorrow = time.strftime("%Y-%m-%d", time.gmtime(time.time() + 86400))
    s.env.configure_plugin(feeddetail="detailed", dateformat="iso")
    try:
        since = int(time.time() * 1000)
        ref = ref_in(alice.cmd(s.town, f"/tix new rev Detailed feed task due:{tomorrow} --assign @bob"))

        def detailed():
            return [p["message"] for p in alice.posts(s.feed["id"], since)
                    if p["user_id"] == s.bot_id and ref in p["message"] and "created" in p["message"]]
        msg = eventually(detailed, 30, what="the detailed feed post")[0]
        contains(msg, "Assignee: **bob**", f"Due: **{tomorrow}**")
        card = s.card_post(alice, ref)
        due = [f["value"] for f in card["props"]["attachments"][0]["fields"] if f["title"] == "Due"]
        check(due == [tomorrow], f"card due field {due}, want the ISO date")
    finally:
        s.env.configure_plugin(feeddetail="compact", dateformat="eu")
    since = int(time.time() * 1000)
    alice.cmd(s.town, f"/tix comment {ref} back to compact")

    def compact():
        return [p["message"] for p in alice.posts(s.feed["id"], since) if p["user_id"] == s.bot_id and "back to compact" in p["message"]]
    msg = eventually(compact, 30, what="the compact feed post")[0]
    check("Assignee:" not in msg, f"still detailed after switching back: {msg!r}")


@scenario("per-feed override: --detail card --comments none")
def _(s):
    alice, bob = s.u["alice"], s.u["bob"]
    out = alice.cmd(s.feed, "/tix subscribe rev all --detail card --comments none")
    contains(out, "now gets tix activity", "--detail card --comments none")
    contains(alice.cmd(s.feed, "/tix subscriptions"), "`--detail card --comments none`")
    try:
        since = int(time.time() * 1000)
        ref = ref_in(alice.cmd(s.town, "/tix new rev Card feed task"))
        bob.cmd(s.town, f"/tix comment {ref} A secret comment")

        def cards():
            out = [p for p in alice.posts(s.feed["id"], since) if p["user_id"] == s.bot_id
                   and any(ref in (a.get("text") or "") for a in (p.get("props") or {}).get("attachments") or [])]
            return out if len(out) >= 2 else None
        posts = eventually(cards, 30, what="the card feed posts")
        texts = [a["text"] for p in posts for a in p["props"]["attachments"]]
        check(any("commented on" in t for t in texts), f"no comment card: {texts}")
        check(not any("A secret comment" in t for t in texts), "--comments none still shows the comment text")
        check(all(not p["message"] for p in posts), "a card feed post has message text")
    finally:
        contains(alice.cmd(s.feed, "/tix subscribe rev all"), "now gets tix activity")
    check("--detail" not in alice.cmd(s.feed, "/tix subscriptions"), "the override survived resubscribing")


@scenario("feed: the infra project is not posted into a rev feed")
def _(s):
    since = int(time.time() * 1000)
    ref = ref_in(s.u["alice"].cmd(s.town, "/tix new infra Not for the feed"))
    time.sleep(4)
    msgs = [p["message"] for p in s.u["alice"].posts(s.feed["id"], since) if ref in p["message"]]
    check(not msgs, f"infra task posted in the rev feed: {msgs}")


@scenario("DM: a task is assigned to me")
def _(s):
    since = int(time.time() * 1000)
    ref = ref_in(s.u["alice"].cmd(s.town, "/tix new infra Rotate the certificates"))
    s.refs["dm"] = ref
    s.u["alice"].cmd(s.town, f"/tix assign {ref} @bob")
    eventually(lambda: [p for p in s.dms(s.u["bob"], since) if "assigned you" in p["message"] and ref in p["message"]],
               30, what="bob's assignment DM")


@scenario("DM: a comment on my task")
def _(s):
    since = int(time.time() * 1000)
    ref = s.refs["dm"]
    s.u["carol"].cmd(s.town, f"/tix comment {ref} I can help with this")
    eventually(lambda: [p for p in s.dms(s.u["bob"], since) if "commented on" in p["message"] and "I can help" in p["message"]],
               30, what="bob's comment DM")
    eventually(lambda: [p for p in s.dms(s.u["alice"], since) if "commented on" in p["message"]],
               30, what="the creator's comment DM")


@scenario("DM: a mention in a comment")
def _(s):
    since = int(time.time() * 1000)
    ref = s.refs["dm"]
    s.u["alice"].cmd(s.town, f"/tix comment {ref} @carol can you take the staging part?")
    eventually(lambda: [p for p in s.dms(s.u["carol"], since) if "mentioned you" in p["message"]],
               30, what="carol's mention DM")


@scenario("DM: nobody is told about their own action")
def _(s):
    since = int(time.time() * 1000)
    bob = s.u["bob"]
    ref = ref_in(bob.cmd(s.town, "/tix new infra Bob's own task"))
    bob.cmd(s.town, f"/tix assign {ref} me")
    bob.cmd(s.town, f"/tix comment {ref} note to self")
    # Wait for a later event to be handled, so the absence below means something.
    s.u["alice"].cmd(s.town, f"/tix comment {ref} alice was here")
    eventually(lambda: [p for p in s.dms(bob, since) if "alice was here" in p["message"]], 30, what="the control DM")
    own = [p["message"] for p in s.dms(bob, since) if "note to self" in p["message"] or "assigned you" in p["message"]]
    check(not own, f"bob was told about his own actions: {own}")


@scenario("/tix notifications off stops DMs, on resumes them")
def _(s):
    bob, alice = s.u["bob"], s.u["alice"]
    contains(bob.cmd(s.town, "/tix notifications off"), "off")
    contains(bob.cmd(s.town, "/tix notifications"), "**off**")
    since = int(time.time() * 1000)
    ref = ref_in(alice.cmd(s.town, "/tix new infra Quiet please --assign @bob"))
    # alice hears about carol's comment on her task, proving the events were handled.
    s.u["carol"].cmd(s.town, f"/tix comment {ref} control comment")
    eventually(lambda: [p for p in s.dms(alice, since) if "control comment" in p["message"]], 30, what="the control DM")
    got = [p["message"] for p in s.dms(bob, since) if ref in p["message"]]
    check(not got, f"bob got DMs with notifications off: {got}")
    contains(bob.cmd(s.town, "/tix notifications on"), "on")
    since = int(time.time() * 1000)
    alice.cmd(s.town, f"/tix comment {ref} back on?")
    eventually(lambda: [p for p in s.dms(bob, since) if "back on?" in p["message"]], 30, what="DMs after on")


@scenario("webhook: an unsigned delivery is rejected")
def _(s):
    from harness import http
    status, _, _ = http("POST", f"{s.env.mm_url}/plugins/cc.heliopsy.tix/hooks/webhook",
                        {"id": "x", "type": "task.created"}, {"X-Tix-Signature": "sha256=00", "X-Tix-Timestamp": "2026-01-01T00:00:00Z"})
    check(status == 401, f"status {status}")


@scenario("/tix webhook status lists deliveries")
def _(s):
    out = s.u["admin"].cmd(s.town, "/tix webhook status")
    contains(out, "| Delivery |")


# --- inline cards, thread sync -------------------------------------------------------------

@scenario("task card: inline style attaches the card to the message")
def _(s):
    alice = s.u["alice"]
    s.env.configure_plugin(cardstyle="inline")
    try:
        ref = ref_in(alice.cmd(s.town, "/tix new infra Inline card task"))
        def inline():
            p = alice.post(s.town, f"inline check {ref}")
            return p if (p.get("props") or {}).get("attachments") else None
        p = eventually(inline, 15, every=2, what="an inline card")
        check(ref in [r.lower() for r in p["props"].get("tix_refs", [])], "tix_refs missing on the inline card")
        time.sleep(2)
        check(not [x for x in s.thread(alice, p["id"]) if x["user_id"] == s.bot_id], "a bot reply as well as the inline card")
    finally:
        s.env.configure_plugin(cardstyle="reply")


@scenario("/tix sync is for channel admins only")
def _(s):
    alice = s.u["alice"]
    _, s.sync = alice.api("POST", "/channels", {"team_id": s.team["id"], "name": "sync",
                                                "display_name": "Sync", "type": "O"}, expect=201)
    for name in ("bob", "carol", "eve"):
        s.u[name].api("POST", f"/channels/{s.sync['id']}/members", {"user_id": s.u[name].id}, expect=201)
    contains(s.u["bob"].cmd(s.sync, "/tix sync on"), "Only channel admins")
    contains(alice.cmd(s.sync, "/tix sync"), "off")


@scenario("/tix sync on: a thread reply becomes a tix comment by the replier")
def _(s):
    alice, bob = s.u["alice"], s.u["bob"]
    alice.cmd(s.sync, "/tix sync on")
    contains(alice.cmd(s.sync, "/tix sync"), "is on")
    ref = s.refs["sync"] = ref_in(alice.cmd(s.sync, "/tix new infra Synced thread task"))
    card = s.card_post(alice, ref, s.sync)
    s.sync_root = card["root_id"]
    bob.post(s.sync, "Replying from the thread", root_id=s.sync_root)

    def mirrored():
        _, cs, _ = s.env.tix_api("GET", f"/tasks/{ref}/comments")
        return [c for c in cs["items"] if c["body"] == "Replying from the thread"]
    c = eventually(mirrored, 20, what="the mirrored comment")[0]
    check(c["author_actor_id"] == s.actor("bob"), f"comment author {c['author_actor_id']}")


@scenario("/tix sync: a tix comment by someone else comes back into the thread, no echo")
def _(s):
    alice, ref = s.u["alice"], s.refs["sync"]
    s.u["carol"].cmd(s.town, f"/tix comment {ref} Comment made in tix")

    def back():
        return [p for p in s.thread(alice, s.sync_root)
                if p["user_id"] == s.bot_id and "Comment made in tix" in p["message"]]
    posts = eventually(back, 30, what="the tix comment in the thread")
    contains(posts[0]["message"], "commented on", ref)
    time.sleep(3)
    echoes = [p for p in s.thread(alice, s.sync_root) if p["user_id"] == s.bot_id and "Replying from the thread" in p["message"]]
    check(not echoes, "the mirrored reply was echoed back into the thread")
    check(len(back()) == 1, "the tix comment was posted twice")


@scenario("/tix sync: a link the webapp posts for a person is not mirrored")
def _(s):
    alice, ref = s.u["alice"], s.refs["sync"]
    # What the create dialog's "Post link in thread" sends: a post as the person, marked by the plugin.
    alice.post(s.sync, f"Created tix task {ref}: Synced thread task", root_id=s.sync_root, props={"from_tix_plugin": "true"})
    time.sleep(3)
    _, cs, _ = s.env.tix_api("GET", f"/tasks/{ref}/comments")
    check(not [c for c in cs["items"] if c["body"].startswith("Created tix task")], "the plugin's own link post was mirrored")


@scenario("/tix sync off stops mirroring")
def _(s):
    alice, ref = s.u["alice"], s.refs["sync"]
    alice.cmd(s.sync, "/tix sync off")
    alice.post(s.sync, "Not for tix", root_id=s.sync_root)
    time.sleep(3)
    _, cs, _ = s.env.tix_api("GET", f"/tasks/{ref}/comments")
    check(not [c for c in cs["items"] if c["body"] == "Not for tix"], "mirrored with sync off")


# --- channel default project, reactions, search, mine, due --------------------------------

@scenario("/tix channel project is for channel admins only")
def _(s):
    contains(s.u["bob"].cmd(s.sync, "/tix channel project infra"), "only channel admins")
    contains(s.u["alice"].cmd(s.sync, "/tix channel project nosuch"), "cannot see")
    contains(s.u["alice"].cmd(s.sync, "/tix channel"), "no default tix project")


@scenario("/tix channel project sets the default; /tix new uses it")
def _(s):
    alice = s.u["alice"]
    contains(alice.cmd(s.sync, "/tix channel project infra"), "INFRA")
    contains(alice.cmd(s.sync, "/tix channel"), "INFRA")
    out = alice.cmd(s.sync, "/tix new A task with no project named")
    check(ref_in(out).startswith("infra-"), f"not in infra: {out}")
    status, body = alice.plugin("GET", f"/me?channel_id={s.sync['id']}")
    check(status == 200 and (body.get("default_project") or "").lower() == "infra", f"/me: {status} {body}")
    status, body = s.u["eve"].plugin("GET", f"/me?channel_id={s.feed['id']}")
    check(status == 200 and not body.get("default_project"), f"default_project leaked: {body}")


@scenario("reaction :ticket: creates a task as the reactor")
def _(s):
    alice, bob = s.u["alice"], s.u["bob"]
    msg = bob.post(s.sync, "The printer on floor 3 is jammed again\nIt ate two reports.")
    s.react(alice, msg["id"])

    def reply():
        return [p for p in s.thread(alice, msg["id"]) if p["user_id"] == s.bot_id and "created" in p["message"]]
    r = eventually(reply, 20, what="the created reply")[0]
    contains(r["message"], "@alice created")
    ref = s.refs["react"] = r["message"].split()[-1].lower()
    t = s.task(ref)
    check(t["title"] == "The printer on floor 3 is jammed again", f"title {t['title']!r}")
    check(t["creator_actor_id"] == s.actor("alice"), "not created as the reactor")
    contains(t.get("body", ""), "It ate two reports", "/_redirect/pl/" + msg["id"])
    s.react_msg, s.react_reply = msg, r


@scenario("reaction :ticket: a second reaction does not make a second task")
def _(s):
    bob = s.u["bob"]
    m = bob.stream.mark()
    s.react(bob, s.react_msg["id"])
    s.ephemeral_after(bob, m, "already tracked", s.refs["react"])


@scenario("reaction :ticket: refused for an unlinked user and in a channel without a default")
def _(s):
    eve, alice = s.u["eve"], s.u["alice"]
    msg = alice.post(s.sync, "eve reacts here")
    m = eve.stream.mark()
    s.react(eve, msg["id"])
    s.ephemeral_after(eve, m, "/tix connect")
    msg = alice.post(s.town, "no default project here")
    m = alice.stream.mark()
    s.react(alice, msg["id"])
    s.ephemeral_after(alice, m, "no default tix project")


@scenario("reaction :ticket: the bot's created reply gets a task card",
          xfail="cards skip bot posts, so the 'created REF' reply never gets a card")
def _(s):
    # Cards skip bot posts, so the "created REF" reply carries no card and no card reply follows it.
    time.sleep(3)
    r = s.u["alice"].get_post(s.react_reply["id"])
    cards = [p for p in s.thread(s.u["alice"], s.react_msg["id"])
             if p["user_id"] == s.bot_id and s.refs["react"] in [x.lower() for x in (p.get("props") or {}).get("tix_refs", [])]]
    check((r.get("props") or {}).get("attachments") or cards,
          "the bot's 'created' reply has no task card (cards skip bot posts)")


@scenario("/tix search")
def _(s):
    out = s.u["alice"].cmd(s.town, "/tix search nightly backups")
    contains(out, s.refs["infra"], "Tasks matching")
    out = s.u["alice"].cmd(s.town, "/tix search printer infra open")
    contains(out, s.refs["react"])
    contains(s.u["alice"].cmd(s.town, "/tix search zzqxnothing"), "No tasks match")


@scenario("/tix mine")
def _(s):
    out = s.u["bob"].cmd(s.town, "/tix mine")
    contains(out, s.refs["dm"], "My open tasks")
    check(s.refs["rev"] not in out, "a shipped task in /tix mine")


@scenario("/tix due lists overdue and due-today tasks")
def _(s):
    alice_actor = s.actor("alice")
    now = time.time()
    def mk(title, due):
        status, t, _ = s.env.tix_api("POST", "/tasks", {"project_ref": "infra", "title": title, "assignee_actor_id": alice_actor,
                                                        "due_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(due))})
        check(status in (200, 201), f"create: {status} {t}")
        return t["ref"].lower()
    s.refs["overdue"] = mk("Renew the domain", now - 86400)
    end_of_day = (int(now) // 86400 + 1) * 86400 - 60
    s.refs["today"] = mk("Send the invoice", max(end_of_day, now + 120))
    out = s.u["alice"].cmd(s.town, "/tix due")
    contains(out, s.refs["overdue"], s.refs["today"])
    check(out.lower().index(s.refs["overdue"]) < out.lower().index(s.refs["today"]), "not soonest first")
    contains(s.u["bob"].cmd(s.town, "/tix due"), "Nothing of yours is due")


@scenario("a bare due date of today is due today, not overdue")
def _(s):
    alice = s.u["alice"]
    # tix stores a date without a time as midnight UTC; the reported bug showed it as overdue.
    out = alice.cmd(s.town, "/tix new infra Pay the hosting bill due:today --assign me")
    ref = s.refs["bare_today"] = ref_in(out)
    contains(out, "Due:**")
    check("overdue" not in out, f"a task due today shown as overdue: {out!r}")
    tomorrow = time.strftime("%Y-%m-%d", time.gmtime(time.time() + 86400))
    status, t, _ = s.env.tix_api("POST", "/tasks", {"project_ref": "infra", "title": "Book the venue",
                                                    "assignee_actor_id": s.actor("alice"), "due_at": tomorrow + "T00:00:00Z"})
    check(status in (200, 201), f"create: {status} {t}")
    due = alice.cmd(s.town, "/tix due")
    line = [ln for ln in due.splitlines() if ref in ln.lower()]
    check(line, f"{ref} missing from /tix due: {due!r}")
    check("overdue" not in line[0], f"due today listed as overdue: {line[0]!r}")
    check(t["ref"].lower() not in due.lower(), "a task due tomorrow is listed by /tix due")


# --- web API ------------------------------------------------------------------------------

@scenario("web API: GET /me")
def _(s):
    status, body = s.u["alice"].plugin("GET", "/me")
    check(status == 200 and body["linked"] and body["actor"]["username"] == "alice", f"{status} {body}")
    status, body = s.u["eve"].plugin("GET", "/me")
    check(status == 200 and not body["linked"] and body["can_connect"], f"eve: {status} {body}")


@scenario("web API: GET /tasks mine and open")
def _(s):
    status, body = s.u["bob"].plugin("GET", "/tasks?mine=true&open=true")
    check(status == 200, f"{status} {body}")
    refs = [t["ref"].lower() for t in body["items"]]
    check(s.refs["dm"] in refs, f"bob's open task missing: {refs}")
    check(s.refs["rev"] not in refs, "a shipped task is listed as open")
    check(all(t["assignee"]["username"] == "bob" for t in body["items"]), "someone else's task in mine")


@scenario("web API: GET /tasks/{ref}")
def _(s):
    status, body = s.u["alice"].plugin("GET", f"/tasks/{s.refs['feed']}")
    check(status == 200, f"{status} {body}")
    check(body["task"]["state"]["label"] == "Working", f"state {body['task']['state']}")
    check(any(c["body"] == "A comment for the feed" for c in body["comments"]), "comments missing")
    moves = {m["to"]: m for m in body["transitions"]}
    check("review" in moves and "dropped" in moves, f"transitions {list(moves)}")


@scenario("web API: POST transition, refused then accepted with a comment")
def _(s):
    alice, ref = s.u["alice"], s.refs["feed"]
    status, body = alice.plugin("POST", f"/tasks/{ref}/transition", {"to": "review"})
    check(status == 200 and body["status"] == "review", f"{status} {body}")
    status, body = alice.plugin("POST", f"/tasks/{ref}/transition", {"to": "shipped"})
    check(status in (400, 409, 422), f"moved without a comment: {status} {body}")
    status, body = alice.plugin("POST", f"/tasks/{ref}/transition", {"to": "shipped", "comment": "via the web API"})
    check(status == 200 and body["status"] == "shipped", f"{status} {body}")


@scenario("web API: POST comment")
def _(s):
    status, body = s.u["alice"].plugin("POST", f"/tasks/{s.refs['feed']}/comments", {"body": "web comment"})
    check(status == 201 and body["body"] == "web comment" and body["author"]["username"] == "alice", f"{status} {body}")


@scenario("web API: POST /tasks create")
def _(s):
    status, body = s.u["alice"].plugin("POST", "/tasks", {"project": "infra", "title": "Made in the webapp",
                                                          "priority": 4, "assignee_user_id": s.u["bob"].id, "tags": ["web"]})
    check(status == 201, f"{status} {body}")
    check(body["assignee"]["username"] == "bob" and body["priority"] == 4 and body["project_key"].lower() == "infra", f"{body}")
    status, body = s.u["alice"].plugin("POST", "/tasks", {"title": "x", "assignee_user_id": s.u["eve"].id})
    check(status == 422 and body["code"] == "assignee_not_linked", f"unlinked assignee: {status} {body}")


@scenario("web API: PATCH /tasks/{ref}")
def _(s):
    status, body = s.u["alice"].plugin("PATCH", f"/tasks/{s.refs['dm']}", {"priority": 1, "assignee_user_id": ""})
    check(status == 200 and body["priority"] == 1 and body["assignee"] is None, f"{status} {body}")


@scenario("web API: 409 not_linked for an unlinked user")
def _(s):
    status, body = s.u["eve"].plugin("GET", "/tasks?mine=true")
    check(status == 409 and body["code"] == "not_linked", f"{status} {body}")


@scenario("web API: 401 without a session")
def _(s):
    from harness import http
    for path in ("/me", "/tasks"):
        status, _, _ = http("GET", f"{s.env.mm_url}/plugins/cc.heliopsy.tix/api/v1{path}")
        check(status == 401, f"{path}: {status}")


# --- digest and reminders -----------------------------------------------------------------

@scenario("/tix digest now sends the digest DM")
def _(s):
    alice = s.u["alice"]
    # Something in progress for the digest's In progress section.
    ip = ref_in(alice.cmd(s.town, "/tix new infra Digest in-progress task --assign me"))
    alice.cmd(s.town, f"/tix start {ip}")
    since = int(time.time() * 1000)
    out = alice.cmd(s.town, "/tix digest now")
    dm = eventually(lambda: [p for p in s.dms(alice, since) if "Your tix digest" in p["message"]], 20, what="the digest DM")[0]
    text = dm["message"]
    contains(text, "Overdue", s.refs["overdue"], "Due today", s.refs["today"], "In progress", ip)
    check(text.index("Overdue") < text.index("Due today") < text.index("In progress"), "sections out of order")
    check(out == "" or "digest" in out.lower(), f"odd reply {out!r}")


@scenario("/tix digest status, on, off")
def _(s):
    alice = s.u["alice"]
    contains(alice.cmd(s.town, "/tix digest status"), "digest is **off**", "reminders are **off**")
    contains(alice.cmd(s.town, "/tix digest on 07:30"), "digest is **on**", "07:30", "UTC")
    contains(alice.cmd(s.town, "/tix digest days daily"), "every day")
    contains(alice.cmd(s.town, "/tix digest off"), "digest is **off**")
    contains(alice.cmd(s.town, "/tix digest at 9"), "Usage")
    contains(s.u["eve"].cmd(s.town, "/tix digest on"), "connect")


@scenario("plugin disables and re-enables cleanly")
def _(s):
    e = s.env
    e.mmctl("plugin", "disable", "cc.heliopsy.tix")
    eventually(lambda: not e.plugin_active(), 30, what="the plugin to stop")
    e.mmctl("plugin", "enable", "cc.heliopsy.tix")
    eventually(e.plugin_active, 60, what="the plugin to start")
    contains(s.u["alice"].cmd(s.town, "/tix digest status"), "digest is **off**")
    logs = subprocess.run([ENGINE, "logs", "--since", "2m", MM], capture_output=True, text=True)
    bad = [l for l in (logs.stdout + logs.stderr).splitlines() if "panic" in l.lower() and "tix" in l.lower()]
    check(not bad, f"panics in the server log: {bad[:3]}")


@scenario("/tix reminders on: the due-today reminder arrives on the next tick (up to 3 min)")
def _(s):
    alice = s.u["alice"]
    at = time.gmtime(time.time() + 120)
    hhmm = f"{at.tm_hour:02d}:{at.tm_min:02d}"
    # The reminder time is the digest time; set it, keep the digest itself off. Reminders go on
    # first: a record with neither on is dropped, time and all.
    since = int(time.time() * 1000)
    contains(alice.cmd(s.town, "/tix reminders on"), "reminders are **on**")
    alice.cmd(s.town, f"/tix digest on {hhmm}")
    contains(alice.cmd(s.town, "/tix digest off"), "digest is **off**", f"run at {hhmm}")
    dm = eventually(lambda: [p for p in s.dms(alice, since) if "Due today" in p["message"]], 240, every=5,
                    what="the due reminder DM")[0]
    contains(dm["message"], s.refs["today"])
    check("Your tix digest" not in dm["message"], "the digest was sent although it is off")
    contains(alice.cmd(s.town, "/tix reminders off"), "reminders are **off**")


# --- languages ----------------------------------------------------------------------------

@scenario("a Bulgarian user gets Bulgarian replies and DMs; the channel stays English")
def _(s):
    carol, alice = s.u["carol"], s.u["alice"]
    carol.api("PUT", f"/users/{carol.id}/patch", {"locale": "bg"}, expect=200)
    try:
        contains(carol.cmd(s.town, "/tix nosuchcommand"), "Непозната команда", "| Команда | Какво прави |")
        contains(alice.cmd(s.town, "/tix nosuchcommand"), "Unknown command")
        since = int(time.time() * 1000)
        ref = ref_in(alice.cmd(s.town, "/tix new infra A task for carol"))
        alice.cmd(s.town, f"/tix assign {ref} @carol")
        dm = eventually(lambda: [p for p in s.dms(carol, since) if ref in p["message"]], 30, what="carol's DM")[0]
        contains(dm["message"], "ти възложи")
        card = s.card_post(carol, ref)
        titles = [f["title"] for f in card["props"]["attachments"][0]["fields"]]
        check("Status" in titles, f"a card in the channel is not in the server's language: {titles}")
    finally:
        carol.api("PUT", f"/users/{carol.id}/patch", {"locale": "en"}, expect=200)


# --- disconnect ---------------------------------------------------------------------------

@scenario("/tix unsubscribe")
def _(s):
    contains(s.u["bob"].cmd(s.feed, "/tix unsubscribe rev"), "Only people who can manage")
    contains(s.u["alice"].cmd(s.feed, "/tix unsubscribe rev"), "Stopped")
    contains(s.u["alice"].cmd(s.feed, "/tix subscriptions"), "no tix feeds")


@scenario("/tix disconnect revokes the minted token")
def _(s):
    alice = s.u["alice"]
    _, toks, _ = s.env.tix_api("GET", f"/tokens?actor_id={s.actor('alice')}")
    items = toks.get("items", toks) if isinstance(toks, dict) else toks
    minted = [t for t in items if t["name"].startswith("mattermost-alice-") and not t.get("revoked_at")]
    check(len(minted) == 1, f"expected one live minted token for alice, got {[t['name'] for t in minted]}")
    tok_id = minted[0]["id"]
    contains(alice.cmd(s.town, "/tix disconnect"), "Disconnected")
    status, body, _ = s.env.tix_api("GET", f"/tokens/{tok_id}")
    check(status == 404 or (isinstance(body, dict) and body.get("revoked_at")), f"token still live: {status} {body}")
    contains(alice.cmd(s.town, "/tix me"), "connect")
    status, body = alice.plugin("GET", "/tasks")
    check(status == 409 and body["code"] == "not_linked", f"{status} {body}")


@scenario("/tix disconnect of a pasted token keeps it working in tix")
def _(s):
    contains(s.u["carol"].cmd(s.town, "/tix disconnect"), "still works in tix")
    status, _, _ = s.env.tix_api("GET", "/whoami", token=s.env.carol_token)
    check(status == 200, f"pasted token rejected: {status}")


@scenario("reconnect after disconnect mints a fresh token")
def _(s):
    contains(s.u["alice"].cmd(s.town, "/tix connect"), "Connected to tix as")
    status, body = s.u["alice"].plugin("GET", "/tasks?mine=true")
    check(status == 200, f"{status} {body}")
