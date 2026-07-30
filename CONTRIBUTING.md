# Contributing

This file is identical in all three IMS repositories — [`ims-backend`](../ims-backend),
[`ims-web`](../ims-web) and [`ims-mobile`](../ims-mobile). If you change it in one, copy it to
the other two in the same pull request.

---

## 1. Branch model

Trunk-based development with short-lived branches. **`dev` is the working trunk**: it is the
default branch, everything is cut from it, and everything merges back into it.

```
main                  production. Protected, release-tagged (v1.4.0). Only release/* and
                      hotfix/* merge here.
dev                   the trunk. Protected, default branch, auto-deploys to staging.
                      Every feature branch starts and ends here.
  ├── feat/<name>     new behaviour, cut from dev            → feat/stock-transfer
  ├── fix/<name>      bug fix, cut from dev                  → fix/negative-stock-guard
  ├── chore/<name>    tooling, deps, config, no behaviour    → chore/bump-go-1.26
  ├── docs/<name>     documentation only                     → docs/api-examples
  ├── refactor/<name> internal change, behaviour identical   → refactor/extract-ledger
  └── test/<name>     tests only                             → test/ledger-concurrency
release/<version>     stabilisation, cut from dev            → release/1.4.0
hotfix/<name>         urgent production fix, cut from main   → hotfix/token-refresh-loop
```

### Naming

Lower-case, hyphen-separated, describing the change rather than the person:
`feat/barcode-scan`, not `feat/sabin-work`. If you use a tracker, prefix the ticket:
`feat/IMS-142-stock-transfer`.

### Everyday flow

```bash
git switch dev
git pull --ff-only origin dev

git switch -c feat/low-stock-alerts
# ... work, committing as you go ...
git push -u origin feat/low-stock-alerts
gh pr create --base dev --fill
```

Rebase on `dev` rather than merging it back in, so history stays linear (required on both
protected branches):

```bash
git fetch origin
git rebase origin/dev
git push --force-with-lease   # --force-with-lease, never --force
```

Feature branches are squash-merged and deleted on merge. Keep them under a few days: a
week-old branch is a merge conflict waiting to happen.

### Releasing

```bash
git switch -c release/1.4.0 dev     # cut when dev is feature-complete
# only stabilisation commits land here — fixes, no new features
gh pr create --base main --title "release: 1.4.0"
# merge to main → release-please tags v1.4.0 and writes the changelog
git switch dev && git merge --no-ff release/1.4.0   # keep any release fixes in dev
```

### Hotfixes

```bash
git switch -c hotfix/token-refresh-loop main
# fix, then open TWO pull requests:
gh pr create --base main --title "fix: token refresh loop"
gh pr create --base dev  --title "fix: token refresh loop (backport)"
```

**Both** merges are required. A hotfix that lands only on `main` gets silently reverted by
the next release.

---

## 2. Commit convention

[Conventional Commits](https://www.conventionalcommits.org/). The release tooling reads
these to decide version numbers and generate the changelog, so the prefix is not decoration.

```
feat(stock): reserve stock when a sales order is confirmed
fix(auth): revoke the whole token family on refresh replay
chore(deps): bump gorm to 1.31.0
docs(api): document the offline sync contract
refactor(ledger): extract FIFO consumption
test(stock): cover concurrent adjustments
perf(reports): index stock_movements on occurred_at
```

| Prefix | Version bump | Use for |
|---|---|---|
| `feat:` | minor | new user-visible behaviour |
| `fix:` | patch | bug fix |
| `perf:` | patch | faster, same behaviour |
| `refactor:`, `chore:`, `docs:`, `test:`, `ci:`, `style:` | none | everything else |

A breaking change adds `!` and a footer, which triggers a major bump:

```
feat(api)!: return stock quantities as numbers rather than strings

BREAKING CHANGE: clients parsing quantity as a string must be updated.
Coordinated with ims-web #88 and ims-mobile #41.
```

---

## 3. Pull requests

- Target `dev` (or `main` for `release/*` and `hotfix/*`).
- One reviewer approval plus green CI. Both are enforced by branch protection.
- Fill in the PR template: ticket, summary, screenshots for UI work, test evidence, and the
  breaking-API-change flag.
- Keep them small. A 200-line PR gets a real review; a 2,000-line PR gets a rubber stamp.
- Draft PRs are welcome for early feedback — mark them ready when CI is green.

### Before you request review

```bash
make lint        # or npm run lint / flutter analyze
make test        # or npm test / flutter test
make docs        # backend only, if you touched any endpoint
```

---

## 4. The API contract

`ims-backend/docs/openapi.yaml` is the **single source of truth**. Nothing else defines the
API shape.

- Backend: any endpoint change means running `make docs` and committing the regenerated
  spec. CI regenerates it and fails the build if the committed file differs.
- Web and mobile: **never hand-write DTOs.** Run the generator:
  - `ims-web`: `npm run generate:api`
  - `ims-mobile`: `./scripts/generate-api-client`
- A breaking backend change needs issues opened on both client repos *before* it merges,
  and the clients pin a tagged backend version so a spec change never breaks them mid-sprint.

---

## 5. Branch protection

Configure these on both `main` and `dev` (Settings → Branches). Without them the model
above is a suggestion rather than a rule.

- Require a pull request before merging, with at least 1 approval
- Dismiss stale approvals when new commits are pushed
- Require review from Code Owners (see `.github/CODEOWNERS`)
- Require status checks to pass: **`CI passed`** (the aggregate job in `ci.yml`)
- Require branches to be up to date before merging
- Require linear history
- Require conversation resolution before merging
- Block force pushes and deletions
- On `main` only: additionally require the staging smoke-test workflow

Repository settings worth turning on once:

- "Automatically delete head branches" after merge
- Allow squash merging only — disable merge commits and rebase merging, so history stays
  one-commit-per-PR
- Default branch: **`dev`**

### Labels

`backend` · `frontend` · `mobile` · `priority:high` · `priority:med` · `priority:low` ·
`type:bug` · `type:feature` · `type:chore`

### Project board

Backlog → In Progress → In Review → Staging → Done

---

## 6. Local setup

Per-repo instructions live in each `README.md`. In all three cases:

- Copy `.env.example` to `.env` (or `.env.local`) and fill it in. Never commit a real one.
- Clients only ever receive **public** configuration — API base URL, public Firebase config.
  Anything secret belongs in the backend and its GitHub Environments.

---

## 7. Code review expectations

What reviewers look for, in order:

1. **Correctness under concurrency** — anything touching stock goes through
   `stock.Ledger.Apply` inside a transaction. A read-modify-write on `stock_items` outside a
   row lock is a bug even when the tests pass.
2. **Tenant scoping** — every query filters on `organization_id`. A missing scope is a data
   leak, not a style nit.
3. **Authorisation** — new routes carry an explicit permission guard. Defaulting to
   "authenticated is enough" is how a viewer ends up able to write stock.
4. **Audit trail** — stock-affecting actions write a movement; other entity changes write an
   audit row, inside the same transaction as the change.
5. **Tests that would fail without the fix** — a test that passes before and after proves
   nothing.
6. **Money and quantities** use the decimal type, never `float64`.
