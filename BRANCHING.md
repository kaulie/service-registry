# Branching & PR flow

## Branch naming

Create one branch per task, named after the task id:

| Prefix | Use for |
|---|---|
| `feature/<taskId>` | new capability (most service-registry work) |
| `fix/<taskId>` | bug fix |
| `issue/<taskId>` | work driven by a reported issue |

Example: `feature/task-4244746550d44594`.

Branch from `main` and keep the branch scoped to a single task.

## Flow

```bash
git clone https://github.com/kaulie/service-registry.git
cd service-registry
git checkout -b feature/<taskId>
# ... work ...
make lint test
git commit -m "feat: ..."
git push -u origin HEAD
gh pr create --fill --base main
```

- Open the PR against `main`; never push directly to `main`.
- A PR must pass `make lint` and `make test` before review.
- **Merging is a human decision.** The agent that opened the PR reports the PR
  URL and waits; it does not merge, release or deploy on its own.

## Commits

Conventional commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`)
with a one line summary focused on the *why*.
