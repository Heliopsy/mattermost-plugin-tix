# Changelog

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
