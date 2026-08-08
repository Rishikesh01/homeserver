## What this changes

<!-- A sentence or two. Link the issue if there is one: Fixes #123 -->

## Why

<!-- The problem or failure mode this addresses. -->

## How it was tested

<!-- Tick what applies, and say what you actually ran. -->

- [ ] `make -C hsctl vet`
- [ ] `make -C hsctl test`
- [ ] Tried it in the sandbox (`make sandbox`) — required for changes to compose files, the
      Caddyfile, the backup path, or `hsctl up`/`down`
- [ ] Ran it on a real stack
- [ ] Docs only, no code changed

## Checklist

- [ ] Docs updated if a flag, config key, or command changed
      (`docs/configuration.md`)
- [ ] No secrets, `.env` contents, or real credentials in the diff
- [ ] Image versions are pinned (no `:latest`)
