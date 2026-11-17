# Commands

Everything is under `/tix`; `/tix help` lists what you may run.

## Your account

| Command | What it does |
| --- | --- |
| `/tix connect` | Link your Mattermost account to tix. With an admin token configured, the plugin finds the tix person with your verified email and issues you a token of your own; otherwise a dialog asks for a token you made in tix. |
| `/tix connect <token>` | Link with a token you made in tix. |
| `/tix me` | Who you are connected as, and with which scopes. |
| `/tix disconnect` | Forget your token, and revoke it in tix when the plugin issued it. |
| `/tix notifications on\|off` | DMs about changes to your tasks (see [notifications](notifications.md)). |
| `/tix digest ...`, `/tix reminders ...` | Daily digest and due reminders (see [digest](digest.md)). |

## Channels

| Command | Who | What it does |
| --- | --- | --- |
| `/tix subscribe <project\|all> [events]`, `unsubscribe`, `subscriptions` | channel admins | Project activity in the channel (see [notifications](notifications.md)). |
| `/tix sync on\|off` | channel admins | Mirror card threads to tix comments and back (see [cards](cards.md)). |
| `/tix channel project <key>\|none` | channel admins | The channel's default project. |
| `/tix webhook setup\|status\|remove` | system admins | Register the tix webhook. |

## Working on tasks

Task commands act as you, through the tix token you linked with `/tix connect`. Replies are only visible to you
unless you ask for a channel post. References autocomplete from your open tasks; `/tix move` suggests the states
the task can go to.

| Command | What it does |
| --- | --- |
| `/tix new [project] <title> [--assign @user\|me] [--priority high] [--post] [--raw]` | Create a task, in the channel's default project when you name none. The title takes tix quick-add tokens (`!high #tag @user due:fri +project`); `--post` announces it in the channel, `--raw` keeps the title as typed. |
| `/tix search <text> [project] [filter] [open]` | Full-text search over titles and bodies, top 15 with status and assignee. Finished tasks are included unless you add `open`. |
| `/tix mine [filter]` | Same as `/tix list mine`. |
| `/tix due [project] [filter]` | Your open tasks due today or overdue, soonest first. |
| `/tix channel` | Show the channel's default project. |
| `/tix channel project <key>\|none` | Channel admins: set or clear the channel's default project, used by `/tix new` and the create reaction. |
| `/tix show <ref>` | Status, assignee, priority, due date, tags, description excerpt and the last three comments. |
| `/tix list [mine\|all\|<project>] [filter]` | Your open tasks by default, grouped by status. A project key lists that project; `all` includes finished tasks; filter terms follow tix's filter language, e.g. `status:todo -tag:ops @bob priority:high due:week title~api`. |
| `/tix start <ref> [comment]` | Move to the workflow's in-progress state. |
| `/tix done <ref> [comment]` | Move to the workflow's done state. |
| `/tix close <ref> [comment]` | Close without doing it: the cancelled state, or done when the workflow has none. |
| `/tix reopen <ref> [comment]` | Move a finished task back to an open state. |
| `/tix move <ref> <state> [comment]` | Move to a state by key or label. |
| `/tix comment <ref> <text>` | Comment on a task (Markdown, line breaks kept). |
| `/tix assign <ref> <@user\|me\|none>` | Assign to a linked Mattermost user, yourself or a tix handle; `none` unassigns. |

Moves follow the task's workflow. When there is no direct transition, the reply lists the states reachable from
the current one and the shortest route; when a transition requires a comment, add it after the reference.

React to a message with :ticket: (the **Create-task reaction** setting) to turn it into a task in the channel's
default project, as you: the first line becomes the title, the message and a link to it the description, and the
bot replies "@you created <ref>" in the thread. Each message gets one task; reacting again shows the existing one.
