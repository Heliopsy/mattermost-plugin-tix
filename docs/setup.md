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
