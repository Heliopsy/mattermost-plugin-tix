# Notifications and channel feeds

tix tells the plugin what happens through a signed webhook. The plugin turns each event into posts in subscribed
channels, DMs to the people concerned and redrawn task cards.

## Setup

1. In tix, make a token for the plugin with `token:admin`, `user:admin` and `tenant:admin` (to find people by email and read
   their role), `webhook:admin`, and every scope it hands out to people (`task:read`, `task:write`,
   `task:transition`, `task:claim`, `project:read`, `workflow:read`, `comment:write`, `artifact:write`,
   `event:subscribe`, `audit:read`): tix only lets a token grant scopes it holds. An admin-role token (`*`) works. Paste it into System Console > Plugins > tix > **Admin token**. `webhook:admin` registers
   the webhook; the read scopes let the plugin show task titles, comment text and workflow state labels
   ("To do → Doing"). Without the read scopes it falls back to the token of the person who caused the event,
   when they have connected, and otherwise to raw state keys.
2. Click **Regenerate** next to **Webhook secret** and save. Until a secret exists the receiver rejects everything.
3. Run `/tix webhook setup` as a system admin. It registers (or updates) a tenant webhook in tix pointing at
   `<SiteURL>/plugins/cc.heliopsy.tix/hooks/webhook`, signed with that secret, for task changes and
   `comment.added`. tix only delivers to `https` URLs on public addresses. An operator can allow private
   addresses with `webhooks.allow_private_targets`, but plain `http` is accepted only for a loopback SiteURL such
   as `http://localhost:8065`, so a Mattermost on a private network needs `https`. `/tix webhook status` shows the endpoint and its last deliveries;
   `/tix webhook remove` deletes it. After regenerating the secret, run `/tix webhook setup` again.

The receiver checks `X-Tix-Signature` (`sha256=` and the hex HMAC-SHA256 of `X-Tix-Timestamp`, a dot and the raw
body), compared in constant time, refuses a timestamp more than five minutes off, and ignores an event it has
already handled, so tix's retries never post twice.

## Channel feeds

| Command | What it does |
| --- | --- |
| `/tix subscribe <project> [events...]` | Post a project's activity in this channel |
| `/tix subscribe all [events...]` | Every project (system admins only) |
| `/tix unsubscribe <project\|all>` | Stop a feed |
| `/tix subscriptions [all]` | This channel's feeds; `all` lists every channel's (system admins) |

Events: `created`, `transitioned` (with the from and to states), `assigned`, `commented`, `done` (moved into a
done or cancelled state; `closed` works too) and `deleted`. Without events a feed gets `created`,
`transitioned` and `commented`. Posts come from the tix bot, one line each, for example
`**alice** moved [infra-42 Fix backups](...) **To do → Doing**`. Feed posts name people without @-mentioning
them, so nobody is pinged by a feed.

Only people who can manage the channel's settings (channel admins, or anyone with the manage public/private
channel properties permission) can add or remove its feeds, and they must be connected to tix and able to see
the project. **A feed publishes.** Whoever subscribes a channel decides that everyone who can read the channel
sees that project's task titles and comment text, including people who cannot open the project in tix. The
subscribe reply says so.

## Personal notifications

Connected people get a DM from the bot when:

- a task is assigned to them;
- someone comments on a task they are assigned to or created;
- a task they created is moved to a done or cancelled state;
- they are @mentioned (by their tix handle) in a comment or a new task's description, and their own tix token can
  read the task.

Nobody is told about their own actions, and each event gives a person at most one DM. `/tix notifications off`
turns these DMs off (`on` turns them back on); channel feeds are not affected.
