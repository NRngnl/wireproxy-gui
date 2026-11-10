# AGENTS.md

## PII and secret scrub before every commit and push

Before every `git commit` and every `git push`, scan for personally identifiable
information (PII) and secrets, and remove them. Do not commit or push until the
scan is clean.

### Where to scan

Everything that will be committed or pushed:

- Staged and unstaged diffs, and new untracked files
- Source code, comments, docstrings, TODOs
- Unit tests, e2e tests, fixtures, snapshots, mocks, seed data
- Docs, README, config, `.env*` samples, CI files, scripts
- Logs, HTML, JSON, CSV, SQL dumps, screenshots and other images
- The commit message (subject and body), tag messages, branch names
- For `push`: every commit not yet on the remote (`git log @{u}..HEAD`)
  including their messages and diffs

### What counts as PII

- Real names of people (other than the exemption below), usernames, handles
- Email addresses, phone numbers, postal/home addresses
- Government, tax, passport, driver-licence, national ID numbers
- Bank, card and payment details; crypto wallet addresses tied to a person
- IP addresses, device IDs, MAC addresses, precise location/coordinates
- Dates of birth, health, biometric, or other sensitive personal data
- Credentials and secrets: passwords, API keys, tokens, private keys, cookies,
  session IDs, connection strings with passwords
- Personal absolute paths such as `/home/<name>/...`, `C:\Users\<name>\...`,
  and machine hostnames that reveal an identity
- Personal URLs: social profiles, private repos, internal/company domains
- Real user data pasted into tests, fixtures or bug reports

### What counts as a secret

- API keys, access/refresh/bearer/JWT tokens, OAuth client secrets, webhook secrets
- Cloud keys (AWS `AKIA...`, GCP service-account JSON, Azure keys, Cloudflare tokens)
- Git host tokens (`ghp_`, `gho_`, `github_pat_`, `glpat-`), npm/PyPI/Docker tokens
- Private keys and certs (`BEGIN ... PRIVATE KEY`, `.pem`, `.p12`, `.pfx`, SSH keys)
- Passwords, DB/Redis/AMQP URLs with credentials (`scheme://user:pass@host`)
- Slack/Stripe/Twilio/SendGrid-style keys (`xox[bpas]-`, `sk_live_`, `SG.`)
- Session cookies, `.env` files, `.npmrc`/`.netrc`/`.pypirc` auth lines, kubeconfigs
- High-entropy strings assigned to key/secret/token/password variables
- Secrets in git history, commit messages, test fixtures and CI config, not just the latest tree

Never commit real secret files; ensure `.env*`, `*.pem`, `*.key`, credentials
files are in `.gitignore`.

### Exemption

The git author/committer **name and email** in commit metadata are allowed.
Upstream author names and emails in the `authors` field, copyright headers
and `LICENSE` files of vendored third-party code (`vendor/`) are also allowed.
Keep them unmodified; licenses require it. Still scan everything else there.
Do not rewrite or strip them. Everything else in the commit, including the
message body and `Co-authored-by:` or `Signed-off-by:` trailers naming
other people, is subject to the scan.

### How to scan

1. Read the full diff yourself:
   `git diff --cached`, `git diff`, `git status --porcelain` (check untracked files),
   and the planned commit message.
2. Run quick pattern checks over the changed content, for example:

   ```sh
   git diff --cached -U0 | grep -nEi \
     '[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}|\b([0-9]{1,3}\.){3}[0-9]{1,3}\b|\+?[0-9][0-9 ()-]{8,}[0-9]|/home/[^/ ]+|/Users/[^/ ]+|C:\\Users\\|(api|secret|token|passw(or)?d|key)[a-z_]*\s*[:=]|BEGIN [A-Z ]*PRIVATE KEY|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{20,}'
   ```

   `gitleaks` (installed with `mise use -g gitleaks@latest`) is required. Run
   `gitleaks git --staged` before every commit and `gitleaks git` over the
   history before every push.
   Also check `git ls-files -o --exclude-standard` for untracked secret files.
3. Review each hit by hand. Patterns miss names and addresses, so read the diff
   even when the grep is empty.
4. Before push, repeat for the whole unpushed range:
   `git log -p @{u}..HEAD` (or `origin/main..HEAD` when no upstream).

### How to fix

- Replace with neutral placeholders: `user@example.com`, `Jane Doe`,
  `192.0.2.1` (documentation range), `+1-555-0100`, `<API_KEY>`, `/path/to/project`.
- Use fake or generated data in tests and fixtures, never real records.
- Read secrets from environment variables or a secret store; keep real values in
  git-ignored files (add `.env` and similar to `.gitignore`).
- Remove PII from the commit message before committing. If a commit already made
  locally contains PII, rewrite it before pushing (`git commit --amend`,
  `git rebase -i`), keeping the author name and email unchanged.
- If PII or a secret was already pushed, stop and tell the user. A pushed secret
  is compromised: it must be revoked/rotated, and history rewrite and
  force-push need their explicit approval.

### Rules

- Never skip the scan, never use `--no-verify` to bypass hooks.
- Never print found PII values back in chat or logs; refer to `file:line` and the
  type of data only.
- When unsure whether something is PII, treat it as PII and ask the user.
- Report at the end: what was scanned, what was removed or replaced, and anything
  that needs the user's decision.

## Code rules

- Use trustworthy, well-maintained libraries instead of hand-rolled code.
  Prefer the standard library, then an established dependency (active
  maintenance, wide adoption, no known vulnerabilities). Write custom code only
  when no such library fits.
- Follow the "Dependency versions" section for every dependency and toolchain.
- Keep it simple. No needless wrappers, abstractions, layers, options or config.
  Do not over-engineer; build only what the task needs.
- No code comments, except marker comments such as `TODO`, `FIXME`, `HACK`,
  `XXX` and `NOTE`, and Go doc comments on exported identifiers. Make the code
  self-explanatory through clear names.
- No cgo in any shipped binary (`CGO_ENABLED=0`).
- Run `gofmt` and `golangci-lint run` (`.golangci.yml`) and fix findings before committing.

## Dependency versions

Version practice is mandatory. Never use old versions.

- Use the latest stable release of every library, module, package and
  toolchain. Never take a version from memory, old docs or old examples.
- Look the version up when adding a dependency: `go get <module>@latest`,
  `npm view <pkg> version`. Let the tool write the version; do not type one by hand.
- Before starting work and before each commit, check for stale or vulnerable
  dependencies and update them: `go list -u -m all`, `govulncheck ./...`.
- Use the latest stable Go toolchain (`go.mod`, `.mise.toml`).
- Commit `go.mod` and `go.sum`.
- Do not use deprecated, retracted or unmaintained modules or APIs. Replace them.
- Read the changelog on major upgrades and fix breakage in the same change.
- If the latest version breaks the build, or no maintained release exists,
  stop and ask the user before downgrading or pinning old.
- Owner-approved exception: `github.com/lobehub/lobe-mygo` and the
  `github.com/egoist/mygo` version it requires have no tagged release. Pin the
  newest commit pseudo-version (`go get github.com/lobehub/lobe-mygo@main`),
  and move to a tagged release as soon as one exists.

## App versioning

The app's own version follows Semantic Versioning and is managed by
release-please (`VERSION`, `.release-please-manifest.json`, `CHANGELOG.md`).

- `feat` is `MINOR`, `fix` and `perf` are `PATCH`, and a breaking change (`!`
  after the scope or a `BREAKING CHANGE:` body line) is `MAJOR`. Before 1.0.0,
  breaking changes bump `MINOR`.
- Never bump the version, edit the changelog by hand, or tag in ordinary
  commits. Never reuse or move a published tag.
- Ask the user before a `MAJOR` bump, a release, or pushing a tag.

## Commit messages

Use the `type(scope): subject` format, for example `feat(ui): add profile list`.

- `type`: one of `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`,
  `build`, `ci`, `chore`, `revert`.
- `scope`: short module or area name in parentheses. Always include it.
- `subject`: imperative, lowercase, no trailing period, about 72 characters or fewer.
- Add a body only when needed, after a blank line.
- The message still goes through the PII and secret scan.
- Stage and commit with explicit paths; never `git add -A` or `git commit -a`.

## License

The project is licensed Apache-2.0 (`LICENSE`). Add only dependencies whose
licenses are compatible with Apache-2.0 (no GPL/AGPL/SSPL).

## Keep documents in sync

Every change that alters behaviour, configuration, CLI flags, environment
variables, dependencies, licensing or limits updates the affected documents in
the same commit: `README.md`, `SECURITY.md`, `AGENTS.md` when a rule changes,
and Go doc comments on changed exported identifiers. Do not edit
`CHANGELOG.md` by hand; release-please writes it from commit messages.
Remove claims that are no longer true. Briefs to workers must repeat this rule.

## Frontend design

Every UI change in the lobe-mygo UI (screens, components, styles, layouts, copy in the UI) always uses the two
design skills. If they are missing in this checkout, install them once:

```sh
npx skills add https://github.com/Leonxlnx/taste-skill --skill "design-taste-frontend"
npx impeccable install --project -y
```

- **New UI or a new view: design with `design-taste-frontend` (taste skill).** Follow its
  `references/app-screens.md` run for app screens, keep the existing design tokens, and use its
  real-browser inspector (`scripts/inspect.mjs --app`) and look-and-fix loop on screenshots.
- **Polish and fixes: use `impeccable`.** Run `critique` and `audit` first, fix what they find,
  then `polish`; use `bolder` when a screen is bland and `quieter` when it is loud. Run
  `impeccable detect --json <changed files>` once the change is done.
- The design rules sit **under** this file's other rules and the project's constraints: no
  external requests from the shipped UI (no CDN fonts, scripts or images), light and dark themes,
  both densities, 390 px and 1280 px, keyboard use and an accessibility pass. A skill suggestion
  that breaks one of these loses.
- Check every visual change in a real browser (the WebAssembly build of the screen) at 390 px and 1280 px and keep the screenshots with
  the change (JPEG, at most 1280 px wide).
