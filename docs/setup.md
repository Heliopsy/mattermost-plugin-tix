# Setup

## Requirements

- Mattermost 11.7 or later (Team or Enterprise Edition).
- A [tix](https://github.com/heliopsy/tix) server reachable from Mattermost over `https`, and able to reach
  Mattermost's `<SiteURL>/plugins/cc.heliopsy.tix/hooks/webhook` for live updates.
- People with the same verified email in both, for automatic linking. With a shared identity provider (OIDC/SSO
  in front of both) this holds already.

## Install

Download `cc.heliopsy.tix-<version>.tar.gz` from the [releases](https://github.com/heliopsy/mattermost-plugin-tix/releases)
and upload it in **System Console > Plugins > Plugin Management**, or let the server fetch it:

```bash
mmctl plugin install-url https://github.com/heliopsy/mattermost-plugin-tix/releases/download/v0.1.1/cc.heliopsy.tix-0.1.1.tar.gz
mmctl plugin enable cc.heliopsy.tix
```

## Configure

In **System Console > Plugins > tix**:

| Setting | What it does |
| --- | --- |
| **tix server URL** | The root of your tix server, e.g. `https://tix.example.com`. |
| **Admin token** | Optional, recommended. A tix token holding `token:admin`, `user:admin`, `tenant:admin` and `webhook:admin`, plus every scope it hands out to people (`task:read`, `task:write`, `task:transition`, `task:claim`, `project:read`, `workflow:read`, `comment:write`, `artifact:write`, `event:subscribe`, `audit:read`), since tix only lets a token grant scopes it holds. An admin-role token (`*`) works. It is used to issue each person their own token on `/tix connect`, to register the webhook and to read labels for feed posts; never to act for a person. |
| **Encryption key** | Generated. Encrypts the personal tokens the plugin stores. Regenerating it disconnects everyone. |
| **Webhook secret** | Generated. tix signs its deliveries with it. Run `/tix webhook setup` again after regenerating it. |
| **Show task cards** | Turn task references in messages into cards. |
| **Task card style** | *Reply in the thread* (default) or *Inline on the message*. |
| **Card quiet period** | Minutes before the same task is carded again in a channel (default 10). |
| **Create-task reaction** | The emoji that turns a message into a task (default `ticket`). |
| **Channel feed detail** | *Compact* (default): one line per event. *Detailed*: adds the assignee, priority and due date. *Card*: each event as a small card. `/tix subscribe --detail` overrides it per feed. |
| **Comment text in feeds and DMs** | *Excerpt* (default): the first 200 characters on one line, cut at a word. *Full*: the whole comment, quoted. *None*: only "commented". `/tix subscribe --comments` overrides it per feed. |
| **People in feeds** | *Names* (default): no one is notified by a feed. *@-mentions*: connected people are @-mentioned, which notifies them. `/tix subscribe --people` overrides it per feed. |
| **Task card fields** | The fields cards show, in order, comma separated: `status, assignee, priority, due, tags, project` (default: all; `project` is the footer). |
| **Task card state buttons** | How many next-state buttons a card shows: none to 3 (default 3). |
| **Show the Assign to me button**, **Show the Comment button** | On by default. |
| **Date format** | *EU* `17.11.2026` (default), *ISO* `2026-11-17` or *Relative* (`today`, `in 2 days`, `3 days ago`; the date beyond two weeks). Used in cards, task commands, the digest and feeds. |
| **Digest sections** | The digest's sections, in order, comma separated: `overdue, due_today, new, blocked, in_progress, counts` (default: all). |
| **Bot display name** | The bot's name (default `tix`); its username stays `tix`. |
| **Bot icon URL** | Optional https URL of a PNG (at most 1 MiB, 1024x1024) for the bot's icon, fetched once when it changes. |

Plugin settings take one value each, so the two list settings are comma-separated text rather than a set of
checkboxes: one line keeps the order you want, which matters for both, and six or seven toggles per list would
crowd the page. Unknown names are ignored and logged when the configuration is saved; an empty value means all.

The bot icon is fetched by the server, so the plugin refuses plain http, addresses on loopback, private and
link-local networks, files over 1 MiB and anything but a PNG; the fetched image is kept, so a restart does not
fetch it again. Mattermost has no file upload setting for plugins, which is why it is a URL.

## Languages

The plugin speaks English and Bulgarian and follows each person's Mattermost language (**Settings > Display >
Language**); any other language gets English. What only one person sees (command replies, button replies,
dialogs, DMs, the sidebar) is in their language. What a channel reads (feed posts, task cards, bot posts in a
channel or thread) is in the server's default language: **Default Client Language** in **System Console > Site
Configuration > Localization**, else **Default Server Language**. The `/tix` autocomplete is registered once
for everyone when the plugin starts, so it is in that server default language too; after changing it, disable
and enable the plugin. Workflow state labels are shown as tix names them. The System Console settings themselves
are English: Mattermost does not translate plugin settings.

Then, as a system admin, run `/tix webhook setup` in any channel. Check it with `/tix webhook status`.

## Link people

Each person runs `/tix connect` once. With the admin token set, that is all: the plugin matches their verified
Mattermost email to their tix account and issues them a token limited to their tix role (member or viewer).
Without it, or when no email matches, a dialog asks for a token they made in tix.

## Permissions and privacy

- Every action runs with the acting person's own tix token; tix decides what they can see and change.
- A task card is drawn with the token of the message's author, so a message never shows a task its author cannot
  see. Buttons act as whoever clicks them.
- Channel feeds and thread sync publish task titles and comments to everyone who can read the channel. Only
  channel admins can turn them on, and the confirmation says so.
- Mattermost gives every member of a public channel the *manage public channel properties* permission by default.
  If feeds should be for channel admins only, move that permission to the channel admin role in
  **System Console > User Management > Permissions**.
- Tokens are stored encrypted (AES-GCM) in the plugin's KV store and never logged.
