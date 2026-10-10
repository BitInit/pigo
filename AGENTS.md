# Development Rules

## Communication

- Keep responses concise, direct, and technical. Avoid filler and emojis in code, commits, issues, and PR comments.
- Use plain language and define unavoidable jargon.
- Explain non-trivial designs with the problem, a concrete example or short trace, and the solution. Distinguish necessary behavior from optional complexity.
- Answer questions before making edits or running implementation commands.
- When responding to feedback, state whether you agree or disagree before describing changes.

## Go Code and Design

- Read affected files in full before editing. For investigations and broad changes, do not rely only on search snippets.
- Follow the Go version declared in `go.mod`. Do not introduce features requiring a newer version without explicitly updating and explaining that requirement.
- Prefer the standard library, ordinary functions, and concrete structs. Introduce interfaces only when they express a useful contract; do not add layers for hypothetical future requirements.
- Keep interfaces small and define them where they are needed. Reuse existing types instead of duplicating interfaces or adding aliases and wrappers without a distinct purpose.
- Prefer concrete types over `any` or `map[string]any`. Use dynamic values only when the data actually requires them.
- Use idiomatic Go naming and package boundaries. Keep helpers local or unexported unless callers need them, and inline trivial single-use helpers.
- Check dependency source or `go doc` for external APIs instead of guessing signatures or behavior.
- Handle errors explicitly. Wrap with `%w` when callers need to inspect the cause, and use `errors.Is` or `errors.As` where appropriate. Do not use panic for expected failures.
- Pass `context.Context` as the first parameter for cancellable operations. Propagate cancellation through model calls and tool execution.
- Add goroutines and channels only for an actual concurrency requirement. Make ownership, shutdown, and synchronization explicit.
- Document exported APIs, especially mutation, callback lifetime, concurrency, and error contracts that callers must understand.
- Ask before removing intentional functionality unless the user has already requested its removal. Do not add compatibility layers unless requested.
- Fix dependency/API mismatches at their source rather than deleting required functionality to make checks pass.
- When generated files are introduced, change their source or generator and regenerate rather than editing generated output manually.

## Formatting and Validation

- After Go code changes, run `gofmt` on changed Go files, then `go test ./...` and `go vet ./...` from the module root. Inspect the complete output; do not hide failures with `tail`.
- Use `go test ./path/to/package -run TestName` for focused iteration. Run every test file you create or modify and resolve failures before finishing.
- Run `go test -race ./...` when changing concurrency or shared-state behavior.
- Use deterministic fake model and tool implementations in tests. Default tests must not call real model providers, consume paid tokens, or depend on credentials being present.
- Keep tests requiring external services behind explicit opt-in controls. Never activate them solely because credentials exist in the environment, and do not run them without authorization.
- Test observable behavior and failure cases rather than mirroring implementation details. Add the issue number near regression tests when fixing an issue.
- Documentation-only changes do not require Go tests. Check the document and its references instead.
- Report checks that could not run and unrelated existing failures accurately. Do not claim a check passed or expand the task to unrelated fixes.
- Write ad-hoc scripts to temporary files, run them, and remove them afterward. Do not embed multiline scripts in shell commands.

## Dependencies

- Treat `go.mod` and `go.sum` changes as reviewed code. `go.sum` records module checksums; it is not a lockfile.
- Add external dependencies only when they provide a concrete benefit over the standard library. Select explicit versions for targeted additions or upgrades.
- Read relevant release notes before upgrades and assess changes affecting the APIs used by this project.
- Avoid broad dependency upgrades such as `go get -u ./...` unless explicitly requested.
- When dependencies change, run `go mod tidy` and inspect the resulting diff. Do not mix unrelated module or toolchain changes into the task.

## Git

Multiple sessions may share this working tree. Preserve changes outside your task.

- Never commit unless the user explicitly asks.
- Commit only files changed in the current task. Stage explicit paths with `git add <path1> <path2>`.
- Before committing, inspect `git status` and the staged diff to verify the scope.
- Use concise commit messages such as `feat(agent): add tool execution loop`, `fix(ai): preserve reasoning content`, or `docs: update development rules`.
- Never run `git reset --hard`, `git checkout .`, `git clean -fd`, `git stash`, `git add -A`, `git add .`, or `git commit --no-verify`.
- Never force push.
- Resolve rebase conflicts only in files changed by the current task. If a conflict affects other files, abort the rebase and ask the user.

## Issues and Pull Requests

- Review PRs without switching the working tree unless explicitly requested. Use `gh pr view`, `gh pr diff`, `gh api`, and `git show` or `git diff` against fetched refs.
- Keep descriptions focused on the concrete problem, resulting behavior, and relevant validation.
- Post issues or comments only when authorized. Write multiline bodies to a temporary file and use `--body-file`.
- Include an AI-generated disclaimer when required by the originating request.
- When a commit is intended to close issues, use `fixes #123` or `closes #123`; repeat the keyword for each issue.
