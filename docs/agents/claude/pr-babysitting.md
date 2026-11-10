# Babysitting PRs here

- CI's `test` job ends with `Check Go vulnerabilities`. A new advisory turns every PR red at once, so look at that step first: `gh run view <run> --log-failed | grep -E 'GO-20|Found in'`.
- GO-2026-6909 (`crewjam/saml`): v0.5.2 fixes it, but the Go vulnerability database lists no fixed version yet. PR #26 skips only that ID, and only from v0.5.2 on. Remove the exception once `curl -s https://vuln.go.dev/ID/GO-2026-6909.json` shows a `fixed` event.
- The step needs `shell: bash` for `pipefail`. A bare `run:` is `bash -e`, so a failing `jq` inside `$(... | sort -u)` would pass.
- A PR branch can be checked out in another T3 worktree. Work in a detached worktree from `origin/<branch>`, then `git push --force-with-lease=<branch>:<old sha> origin HEAD:<branch>`.
- `go test ./...` needs `web/dist`: run `pnpm install --frozen-lockfile && pnpm build` in `web/` first. Database tests skip locally and run in CI.
- To test the video code page's script, render `mpdGatePageTemplate` from a throwaway `_test.go` in `internal/video`. Then load the HTML in jsdom from `web/node_modules/jsdom` with `runScripts: 'dangerously'` and a stubbed `window.fetch`.
- Merging is for humans. Repo auto-merge is off and `main` has no branch protection.
