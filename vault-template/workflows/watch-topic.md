---
type: workflow
id: watch-topic
name: Watch topics
description: Daily news check on the topics listed in research/watchlist.md — appends a digest to
  the daily note and notifies only when something notable happened.
enabled: false
triggers:
  - cron: "0 8 * * *"
    tz: Europe/Lisbon
  - intent: ["check my watchlist", "watchlist news", "topic news", "novidades da watchlist"]
steps:
  - id: watchlist
    tool: vault.read
    with: { path: research/watchlist.md }

  - id: scan
    agent: news-briefer
    timeout: 20m
    task: |
      Daily watchlist check for {{ string(now).substring(0, 10) }}.
      Topics (from research/watchlist.md):

      {{ steps.watchlist.output.body }}

      Follow your playbook for each topic (freshness: day unless the topic says otherwise; skip
      items already reported in recent daily notes under "Watchlist"). Use the output contract:
      the NOTABLE line, the HEADLINE line, then the digest.

  - id: save
    if: >-
      !run.dry_run && steps.scan.output.split('\n').filter(l, !l.startsWith('NOTABLE:') && !l.startsWith('HEADLINE:')).join('\n').trim() != 'Nothing new.'
    tool: vault.daily_append
    with:
      text: |
        ## Watchlist

        {{ steps.scan.output.split('\n').filter(l, !l.startsWith('NOTABLE:') && !l.startsWith('HEADLINE:')).join('\n').trim() }}

  - id: send
    if: "!run.dry_run && steps.scan.output.trim().startsWith('NOTABLE: yes')"
    tool: notify
    with:
      title: >-
        {{ size(steps.scan.output.split('\n').filter(l, l.startsWith('HEADLINE:'))) > 0 ? steps.scan.output.split('\n').filter(l, l.startsWith('HEADLINE:'))[0].substring(9).trim() : 'Watchlist: something notable' }}
      body: |
        {{ steps.scan.output.split('\n').filter(l, !l.startsWith('NOTABLE:') && !l.startsWith('HEADLINE:')).join('\n').trim() }}
      kind: alert
---

# Watch topics

Every morning the `news-briefer` agent searches recent news for each topic in
`research/watchlist.md`, skips what earlier digests already reported, appends a "Watchlist"
section to the daily note and pushes a notification **only** when something is notable (a
release, a price below your target, a security issue, a deadline…).

Enable it after creating `research/watchlist.md`, for example:

```markdown
---
type: research
title: Watchlist
---
# Watchlist

- **Home Assistant** — releases and breaking changes. notify: always (breaking changes)
- **Heat pumps Portugal** — subsidies (Fundo Ambiental), prices. lang: pt
- **Go language** — releases, security advisories. site: go.dev
- **Steam Deck 2** — announcements; notify if price < €500. weekly
```

Free text works: one bullet per topic, with optional hints (`lang:`, `site:`, `weekly`,
`notify: always`, price targets). Change the schedule in `triggers` (cron, Europe/Lisbon time).
