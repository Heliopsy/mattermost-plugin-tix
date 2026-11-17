# notifications Specification

## Purpose

Bring tix activity into Mattermost: a signed tix webhook feeds project activity into subscribed channels, sends
personal DMs to the people a change concerns, and redraws task cards, without ever acting for a person.

## Requirements

### Requirement: Verified webhook deliveries

The plugin SHALL accept tix deliveries at `POST /plugins/cc.heliopsy.tix/hooks/webhook` only when
`X-Tix-Signature` equals `sha256=` followed by the hex HMAC-SHA256, under the configured webhook secret, of the
`X-Tix-Timestamp` value, a `.` and the raw body, compared in constant time. It SHALL reject a timestamp more than
five minutes from the current time, and SHALL reject every delivery while no secret is configured.

#### Scenario: Valid delivery

- **WHEN** tix posts an event signed with the configured secret and a current timestamp
- **THEN** the plugin answers 202 before doing any work for the event

#### Scenario: Wrong secret or tampered body

- **WHEN** the signature does not match the body and timestamp under the configured secret
- **THEN** the plugin answers 401 and does nothing with the event

#### Scenario: Stale timestamp

- **WHEN** a correctly signed delivery carries a timestamp more than five minutes old
- **THEN** the plugin answers 401

#### Scenario: Secret unset

- **WHEN** the webhook secret setting is empty
- **THEN** every delivery is answered 401

### Requirement: Idempotent handling

The plugin SHALL handle each tix event at most once, remembering handled event ids across cluster nodes for
longer than tix's retry schedule.

#### Scenario: Retried delivery

- **WHEN** tix delivers an event the plugin has already handled
- **THEN** the plugin answers 200 and posts nothing again

### Requirement: Webhook administration

The plugin SHALL let system admins register or update (`/tix webhook setup`), inspect (`/tix webhook status`)
and delete (`/tix webhook remove`) the tenant webhook in tix with the configured admin token. Setup SHALL point
the webhook at the plugin's public webhook URL with the configured secret and the task and comment event types
the plugin handles, updating the plugin's existing endpoint instead of adding a second one.

#### Scenario: Setup twice

- **WHEN** a system admin runs `/tix webhook setup` and an endpoint for the plugin already exists in tix
- **THEN** that endpoint is updated in place

#### Scenario: Not an admin

- **WHEN** a user without the manage system permission runs `/tix webhook setup`
- **THEN** the command is refused and tix is not called

### Requirement: Channel feeds

A user who can manage a public or private channel's properties, is connected to tix and can see a project SHALL
be able to subscribe that channel to the project's activity with `/tix subscribe <project> [events...]`, choosing
among created, transitioned, assigned, commented, done and deleted, with created, transitioned and commented as
the default. Subscribing a channel to every project SHALL require the manage system permission. With the default
settings the bot SHALL post one compact line per matching event, naming people without @-mentioning them and
showing state labels for transitions.

#### Scenario: Subscribe with default events

- **WHEN** a channel admin runs `/tix subscribe infra` in their channel
- **THEN** the channel gets created, transitioned and commented posts for project infra
- **AND** the reply warns that everyone who can read the channel sees those tasks' titles and comments

#### Scenario: Not allowed to manage the channel

- **WHEN** a member without permission to manage the channel's properties runs `/tix subscribe infra`
- **THEN** the command is refused and no subscription is stored

#### Scenario: Transition post

- **WHEN** alice moves infra-42 "Fix backups" from To do to Doing in a subscribed project
- **THEN** the channel gets "**alice** moved [infra-42 Fix backups](url) **To do → Doing**"

#### Scenario: Done subscription

- **WHEN** a channel subscribed only to done sees a task moved into a done or cancelled state
- **THEN** it gets the transition post, and no post for other transitions

### Requirement: Feed formatting

Feed posts SHALL follow the FeedDetail (compact, detailed, card), CommentText (full, excerpt, none) and
FeedPeople (names, mentions) settings, defaulting to compact, excerpt and names. Detailed SHALL add a line with
the task's assignee, priority and due date when tix told the plugin about the task; card SHALL post the event as
a message attachment coloured by the task's state category, with those facts as fields. Excerpt SHALL keep the
first 200 characters of a comment on one line, cut at a word and marked with "…"; full SHALL quote the whole
comment (capped at 4000 bytes); none SHALL only say that someone commented. Mentions SHALL @-mention connected
people, which notifies them. `/tix subscribe <project> [events] [--detail ...] [--comments ...] [--people ...]`
SHALL store those choices on the subscription, overriding the settings for that feed only; a subscription saved
without them SHALL follow the settings, and `/tix subscriptions` SHALL show each feed's overrides. When several
subscriptions of a channel match an event, the first stored one SHALL decide the post's format. Feed posts SHALL
be written in the server's default language and dated in the time zone of the person who set the feed up.

#### Scenario: Card feed

- **WHEN** a channel subscribed with `/tix subscribe infra --detail card` sees infra-42 moved to Doing
- **THEN** it gets a post with one attachment whose text is the transition line and whose fields are the
  assignee, priority and due date

#### Scenario: Comments left out

- **WHEN** the CommentText setting is none and someone comments on a task of a subscribed project
- **THEN** the feed post says that they commented, without the comment text

#### Scenario: Old subscription

- **WHEN** a subscription stored before these options existed matches an event
- **THEN** its post follows the System Console settings

#### Scenario: Bad option

- **WHEN** someone runs `/tix subscribe infra --detail huge`
- **THEN** the reply names the valid values and no subscription is stored

### Requirement: Personal notifications

The bot SHALL DM a connected person when a task is assigned to them, when someone comments on a task they are
assigned to or created, when a task they created is moved to a done or cancelled state, and when they are
@mentioned by tix handle in a comment or a new task's description and their own tix token can read the task.
It SHALL NOT DM a person about their own action, SHALL send at most one DM per person per event, and SHALL send
none to a person who turned notifications off with `/tix notifications off`. A DM SHALL be written in the
recipient's Mattermost language and SHALL quote a comment per the CommentText setting.

#### Scenario: Assigned

- **WHEN** alice assigns a task to bob, who is connected
- **THEN** bob gets a DM saying alice assigned him the task

#### Scenario: Mention of a person who pasted a token

- **WHEN** carol connected by pasting a tix token and alice writes `@carol` in a comment on a task carol can read
- **THEN** carol gets a DM saying alice mentioned her, because the link keeps carol's own tix handle, not the
  token's name

#### Scenario: Bulgarian recipient

- **WHEN** alice assigns a task to bob and bob's Mattermost language is Bulgarian
- **THEN** bob's DM reads "**@alice** ти възложи ..." while the channel feed stays in the server's language

#### Scenario: Own action

- **WHEN** bob assigns a task to himself
- **THEN** bob gets no DM

#### Scenario: Notifications off

- **WHEN** bob ran `/tix notifications off` and alice comments on his task
- **THEN** bob gets no DM

### Requirement: Live task cards

After any task or comment event that names a task, the plugin SHALL ask the cards feature to redraw every card
showing that task. For a new comment it SHALL also hand the comment to the cards feature for synced card threads,
with the channels it already posted it to. A comment the cards feature mirrored from a channel's thread
(`store.MirroredComment`) SHALL NOT be posted to that channel's feed.

#### Scenario: Card refresh

- **WHEN** a task shown in a card is transitioned in tix
- **THEN** the card is redrawn with the new state

#### Scenario: Mirrored comment is not echoed

- **WHEN** a reply in a synced card thread in a subscribed channel becomes a tix comment
- **THEN** that channel's feed gets no post for the comment
