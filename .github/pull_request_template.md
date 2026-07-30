## Ticket

<!-- e.g. Closes IMS-142 -->

## Summary

<!-- What changed and why. One paragraph is usually enough. -->

## Screenshots / API examples

<!-- Web and mobile PRs: before/after screenshots.
     Backend PRs: a sample request and response, or the new OpenAPI diff. -->

## Test evidence

<!-- Paste the relevant test output, or name the tests you added. -->

## Checklist

- [ ] Conventional Commit title (`feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `test:`)
- [ ] Tests added or updated, and `make test` passes locally
- [ ] `make lint` passes
- [ ] Database migrations included, reversible, and tested with `make migrate-down`
- [ ] `make docs` run and `docs/openapi.yaml` committed (any API change)
- [ ] README / CONTRIBUTING updated if behaviour or setup changed
- [ ] No secrets, credentials or real `.env` files in the diff

## Breaking API change?

- [ ] **No** — clients keep working unchanged
- [ ] **Yes** — describe the change below and open the matching issues on
      `ims-web` and `ims-mobile` before merging

<!-- If yes: what breaks, and what the clients must do about it. -->
