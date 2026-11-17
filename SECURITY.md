# Security Policy

## Reporting a vulnerability

Report security issues privately through
[GitHub Security Advisories](https://github.com/heliopsy/mattermost-plugin-tix/security/advisories/new).
Please do not open a public issue for a vulnerability.

Include the affected version, a description, reproduction steps and the impact you believe it has. You will get
an acknowledgement within a few days and an assessment once the report has been reviewed.

## Supported versions

Until v1.0.0, only the latest release receives security fixes.

## Scope

The plugin stores people's tix tokens and acts on their behalf. Findings of particular interest:

- Acting as another person, or with more scopes than the person's own tix role.
- Showing a task, title or comment to someone who cannot see it in tix, other than through a feed or synced
  thread a channel admin chose to publish.
- Reading or recovering stored tokens, or tokens reaching logs or clients.
- Accepting a webhook delivery that was not signed with the configured secret.
- Server-side request forgery through settings or task content.
