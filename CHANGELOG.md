# Changelog

## Unreleased

- Languages: the plugin speaks English and Bulgarian, following each person's Mattermost language; channel
  posts, cards and the `/tix` autocomplete use the server's default language.
- Settings for how things read: channel feed detail (compact, detailed, card), comment text (full, excerpt,
  none), people in feeds (names or @-mentions), task card fields and buttons, the date format (EU, ISO,
  relative), the digest's sections and their order, and the bot's display name and icon.
- `/tix subscribe <project> [events] --detail ... --comments ... --people ...` overrides the feed settings for
  one feed; `/tix subscriptions` shows them.
- Cards show the due date in the date format setting (EU by default, was ISO); comment excerpts in feeds and DMs
  are 200 characters cut at a word (were 280).
- Fix: a due date without a time (stored by tix as midnight UTC) showed as overdue on its own day. It is now a
  whole calendar day; due dates are compared as dates in each person's Mattermost time zone in `/tix show`,
  `list`, `due`, cards, the digest and reminders.
- Fix: with thread sync on, the link the create dialog posts in a thread was copied to tix as a comment and
  echoed into the channel's feed.

## 0.1.1 (17.11.2026)

- Fix: connecting with a pasted token stored the token's name as the person's handle, so mention DMs never
  reached them.
- End-to-end test suite (`make e2e`) against a real Mattermost and tix, run on every pull request.

## 0.1.0 (17.11.2026)

First release.

- `/tix connect`: each person links their own tix account, matched by verified email with an admin token, or by
  pasting a token.
- Task commands: `new`, `show`, `list`, `search`, `mine`, `due`, `start`, `done`, `close`, `reopen`, `move`,
  `comment`, `assign`, with autocomplete.
- Task cards with action buttons, posted by the bot as thread replies and redrawn live.
- Channel feeds (`/tix subscribe`), personal DMs, opt-in thread sync (`/tix sync on`).
- A default project per channel, and creating a task by reacting with :ticket:.
- Daily digest and due reminders.
- Sidebar, task view, and *Create tix task* from any message.
