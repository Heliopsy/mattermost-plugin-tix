# task-cards Specification

## Purpose

Turn task references in Mattermost messages into live tix task cards with buttons that move, assign and
comment on the task, without showing a channel anything the message's author cannot see in tix, and, where a
channel opts in, keep a card's thread and the task's tix comments in step.

## Requirements

### Requirement: Unfurl task references

When the UnfurlRefs setting is on, the plugin SHALL give a new message a task card for each task reference it
mentions, at most three per message. A reference SHALL be `<project key>-<number>` where the key is a project
the author can see in tix, matched case-insensitively and as a whole word. References inside code blocks,
inline code and URLs SHALL be ignored.

With the CardStyle setting `reply` (the default), the plugin SHALL leave the person's message unchanged and,
after it is posted, the tix bot SHALL post the cards as one reply in the message's thread (rooted at the
message's root, or at the message itself), with the shown references in the reply's `tix_refs` prop and the
message's id in its `tix_source_post` prop. With CardStyle `inline`, the cards SHALL be attached to the
message itself and its `tix_refs` prop set, as before. Cards already attached to people's messages keep
working in either style.

#### Scenario: Message mentions a task
- **WHEN** a connected user posts "please look at infra-42" and infra is one of their projects
- **THEN** the message is saved as written and the bot replies in its thread with a card for infra-42 whose
  `tix_refs` prop lists infra-42

#### Scenario: Inline style
- **WHEN** CardStyle is `inline` and a connected user posts "please look at infra-42"
- **THEN** the post itself carries the card and lists infra-42 in its `tix_refs` prop

#### Scenario: Reference in code or a URL
- **WHEN** a message mentions infra-42 only inside `inline code`, a fenced block or a link
- **THEN** no card is posted

#### Scenario: Unknown project key
- **WHEN** a message mentions ops-12 and the author can see no project with key ops
- **THEN** no card is posted

#### Scenario: Unfurling switched off
- **WHEN** UnfurlRefs is off
- **THEN** no message gets a card

### Requirement: No repeated cards

In the reply style the plugin SHALL NOT post a card for a task that already has a card in the same thread, nor
for a task that got a card in the same channel within the last CardQuietMinutes minutes (default 10); such a
reference is skipped. The bot's own posts SHALL never get cards.

#### Scenario: Same task twice in a busy channel
- **WHEN** infra-42 got a card in a channel five minutes ago and someone mentions it there again
- **THEN** no second card is posted

#### Scenario: Thread already has the card
- **WHEN** a reply in a thread mentions infra-42 and the thread already shows its card
- **THEN** no card is posted, however long ago the first one was

### Requirement: Edits and deletions

In the reply style, when a person edits a message to add a reference, the plugin SHALL card the new reference
under the same rules as a new message, so a reference already carded in the thread is not carded again. When a
message a bot card answers is deleted, the plugin SHALL delete that card, and `Refresh` SHALL delete a bot card
whose message is gone.

#### Scenario: Reference added by an edit
- **WHEN** a user edits "hello" into "hello infra-42"
- **THEN** the bot replies with a card for infra-42 once, and further edits add no second card

#### Scenario: Message deleted
- **WHEN** the message a bot card answered is deleted
- **THEN** the bot card is deleted and no longer tracked

### Requirement: Cards are drawn with the author's own access

The plugin SHALL read a task for a card with the tix token of the author of the message that mentions it, for a
bot reply as for an inline card. When the author is not connected to tix or cannot see the task, the plugin
SHALL NOT show a card for it. A message SHALL NOT be able
to bring its own cards: attachments with buttons aimed at the plugin and a `tix_refs` prop supplied by the
poster SHALL be removed before the message is saved.

#### Scenario: Author cannot see the task
- **WHEN** a user mentions a task that tix does not show them
- **THEN** the post gets no card for it, even if other channel members could see the task

#### Scenario: Author not connected
- **WHEN** a user who has not run `/tix connect` mentions a task
- **THEN** the post gets no card

#### Scenario: Forged card
- **WHEN** a message arrives with an attachment whose buttons target the plugin
- **THEN** that attachment and any `tix_refs` prop are removed and other attachments are kept

### Requirement: Card content

A card SHALL be coloured by the category of the task's state (todo grey, in_progress blue, blocked and
waiting amber, done green, cancelled dark), SHALL be titled with the reference and title linking to the task
in tix, SHALL show the fields the CardFields setting lists (status label, assignee, priority, due date and tags
when set; all of them by default), in that order, and SHALL carry the footer `tix · <project name>` when it lists
project, else `tix`. Field names and button labels SHALL be in the server's default language, since a card is a
channel post; workflow state labels SHALL be shown as tix sends them. The due date SHALL follow the DateFormat
setting and be dated in the time zone of the person the card is drawn for.

#### Scenario: Task in progress
- **WHEN** a card is drawn for a task in a state of category in_progress
- **THEN** the card is blue and its Status field shows the state's label

#### Scenario: Fewer fields
- **WHEN** CardFields is `due, status`
- **THEN** the card shows Due then Status, and its footer is `tix`

### Requirement: Card buttons

A card SHALL offer up to CardButtons (0 to 3, default 3) transitions the workflow allows from the task's current
state, the ones that move work forward first, plus "Assign to me" while the task is not finished (unless
ShowAssignButton is off) and "Comment" (unless ShowCommentButton is off). A button SHALL act
with the tix token of the person who clicks it, after checking that they can read the post's channel and that
the post shows that task. A person who is not connected SHALL get an ephemeral message pointing to
`/tix connect`.

#### Scenario: Unconnected user clicks a button
- **WHEN** a user who has not connected to tix clicks Done on a card
- **THEN** they see an ephemeral message telling them to run `/tix connect` and nothing changes in tix

#### Scenario: Transition that needs a comment
- **WHEN** a user clicks a transition the workflow marks requires_comment
- **THEN** a dialog asks for the comment and the move is made, with the comment, only when it is submitted

#### Scenario: Comment
- **WHEN** a user clicks Comment and submits text
- **THEN** the comment is added to the task as that user

#### Scenario: Assign to me
- **WHEN** a connected user clicks "Assign to me"
- **THEN** the task is assigned to that user's tix actor

### Requirement: Cards stay current

After a button or dialog changes a task, the plugin SHALL redraw the card in place and every other post showing
that task. `Refresh(ref)` SHALL redraw every post recorded for the reference with the token of the author of
the message the card belongs to, skipping cards whose author is not connected, dropping the card of a task the
author can no longer see (deleting a bot reply left with no card), and forgetting deleted posts. A redraw
SHALL update the bot's reply, never a person's message, unless the card is an inline card on that message.

#### Scenario: Task changes in tix
- **WHEN** notify reports that infra-42 moved to done
- **THEN** every card of infra-42 shows Done and is green, and the messages that mentioned it are not edited

#### Scenario: Post deleted
- **WHEN** a post that showed a card has been deleted
- **THEN** Refresh stops tracking it

### Requirement: Thread sync switch

`/tix sync on|off` SHALL turn thread sync on or off for the channel it is run in, and `/tix sync` SHALL show the
current state. Only people holding the channel's manage-roles permission (channel, team and system admins),
checked on the server, SHALL change it, and only in public and private channels. A change SHALL be audited and
announced in the channel by the bot. Sync is off by default.

#### Scenario: Member tries to turn sync on
- **WHEN** a channel member who is not a channel admin runs `/tix sync on`
- **THEN** they are told only channel admins can, and nothing changes

#### Scenario: Channel admin turns sync on
- **WHEN** a channel admin runs `/tix sync on`
- **THEN** sync is on for that channel and the bot says so in the channel

### Requirement: Replies become tix comments

With sync on, a person's reply in a thread that shows a task card SHALL be added to that task as a tix comment
with the replier's own token. When the thread shows cards for several tasks, the reply SHALL go to the tasks it
names, and to none when it names none of them. A replier who is not connected SHALL get one ephemeral hint to
run `/tix connect`, once, and nothing is mirrored. Bot posts, system posts, webhook posts, edits and posts the
plugin's webapp made on a person's behalf (marked with the `from_tix_plugin` prop, such as the create dialog's
"Post link in thread") SHALL NOT be mirrored, and a post SHALL be mirrored at most once. The marker is set by the
client, so it SHALL only ever be used to skip mirroring, never to grant anything. Each mirrored comment SHALL be recorded in the store
(`store.MarkMirrored`) by comment id and by task, author and text, before and after it is created.

#### Scenario: Reply mirrored
- **WHEN** sync is on and Bob replies "on it" in the thread of the infra-42 card
- **THEN** tix gets the comment "on it" on infra-42, made with Bob's token

#### Scenario: The create dialog's link post
- **WHEN** sync is on and alice creates a task from a thread message with "Post link in thread"
- **THEN** the link message is posted as alice and is not added to the task as a comment

#### Scenario: Unconnected replier
- **WHEN** sync is on and someone who has not run `/tix connect` replies in a card thread
- **THEN** they see one ephemeral hint, and later replies get no further hint; nothing reaches tix

### Requirement: tix comments come into the thread

With sync on, a new tix comment on a task SHALL be posted by the bot into every thread in a synced channel that
shows a card of the task, once per thread, quoting the comment and naming its author. A comment mirrored from a
thread SHALL NOT be posted back into that thread, and notify SHALL NOT post it into the channel it came from
(`store.MirroredComment`). A channel that notify already posted the comment to through a subscription SHALL NOT
get a second copy in a thread. Edited comments SHALL NOT be posted.

#### Scenario: Comment in tix
- **WHEN** sync is on in a channel with an infra-42 card thread and Dana comments on infra-42 in tix
- **THEN** the bot posts Dana's comment in that thread

#### Scenario: No echo
- **WHEN** Bob's thread reply was mirrored to tix and tix reports the new comment
- **THEN** nothing is posted back into Bob's thread or, by notify, into its channel
