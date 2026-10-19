# Contributing to Agent Swarm

Thanks for helping. Bug reports, fixes, new agent kinds and docs are all welcome. Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## Licence

Agent Swarm is licensed under the [Apache License 2.0](LICENSE). By opening a pull request you agree that your contribution is licensed under the same terms (Apache-2.0 section 5). There is no CLA.

Third-party material keeps its own licence. If you vendor anything, add its upstream licence file and record the source and every change, as `internal/install/skills/vendor/README.md` and `assets/icons/LICENSES.md` already do.

## Setting up

You need a Mac with the toolchain from the README's Requirements section: Go 1.27+, tmux 3.6+, Node 22 + pnpm 10, Xcode 16+ command line tools. Then:

```bash
git clone https://github.com/AlexanderTar/agent-swarm.git
cd agent-swarm
make test      # Go tests with the coverage gate, web tests, menu bar tests
make dev       # daemon on :17777 with a scratch ~/.swarm-dev, so your real board is untouched
make e2e       # end-to-end run with the fake agent (no model calls)
```

## Before you open a pull request

- `make test-go` must pass. It runs `go vet`, the gofmt check, the race-enabled Go tests and the coverage gate in `scripts/cover.sh` (85% for `items`, `runtime`, `hook`, `mcpserver`, `worktree` and `migrate`, 75% for the other `internal` packages). Never lower or bypass the thresholds; add real behaviour tests instead. See [AGENTS.md](AGENTS.md).
- Run `make test-web` when you touch `web/`, and `make test-menubar` when you touch `apps/menubar/`.
- Write the failing test first, then the fix.
- Adding an agent kind? Follow `docs/adding-an-agent-kind.md`.
- Editing a skill under `internal/install/skills/`? Run `make skills-sync` and commit the result.

## Commits and pull requests

- Sign your commits (`git config commit.gpgsign true`).
- Use conventional commit subjects with a scope, as the history does: `fix(usage): …`, `feat(workflow): …`, `docs(skills): …`, `test(runtime): …`.
- Keep one logical change per commit. Don't add agent attribution lines (`Co-Authored-By` an AI, "Generated with …").
- Open the pull request against `main`. Pull requests are squash-merged, so the title becomes the commit subject: write it in the same style.
- Say what changed, why, and how you verified it (commands and their results).

## Reporting bugs

Open an issue with the **Bug report** template. Include the Swarm version (`git rev-parse --short HEAD` of your checkout), the agent kinds involved, and the output of `swarm doctor` if it looks relevant. Leave out tokens, emails and private repository content.

### Let your agents report Swarm bugs upstream

When an agent running under Swarm hits a bug in Swarm itself, it calls `swarm_report_bug`. That report stays on your Mac (`swarm bugs` lists them). To send one to this repository, run:

```bash
swarm bugs --issue <id>              # the id is in swarm_report_bug's result and in `swarm bugs`
swarm bugs --issue <id> --dry-run    # print the title, body and target; post nothing
swarm bugs --issue <id> --repo OWNER/NAME   # file somewhere else (default AlexanderTar/agent-swarm)
```

It needs an authenticated [`gh`](https://cli.github.com). Swarm builds the issue (label `bug`) from the report's fields, masks emails and token-like strings, shortens home folders to `~`, and leaves out the transcript. If an open issue already has the same title it comments there instead of opening a duplicate. The issue URL is stored on the report and shown in `swarm bugs`; a second `--issue` for the same report refuses. Read the `--dry-run` output first: the masking recognises common token formats, not every secret, and private repository names are left as they are.

If you want your agents to offer this, add a rule to your Swarm instructions (menu bar app, Settings → Instructions, or ask an agent to add it with `swarm_instructions`). Agents only act on it when it's in your instructions:

```markdown
## Reporting Swarm bugs upstream

Whenever you report a Swarm bug with `swarm_report_bug`, also offer to file it on GitHub by running `swarm bugs --issue <id>` (the id is in the tool result). Ask me first.
```

Or, if you work on Swarm yourself, turn fixes into pull requests:

```markdown
## Fixing Swarm bugs upstream

Whenever you report a Swarm bug with `swarm_report_bug`, pass `board: true` so it lands on the board as a Draft bug. When I start that bug and it's fixed and verified in my fork of agent-swarm, offer to open a pull request against AlexanderTar/agent-swarm `main` (`gh pr create -R AlexanderTar/agent-swarm`), following CONTRIBUTING.md: signed conventional commits, `make test-go` passing, and the bug report summarised in the description. Ask me before pushing or opening the pull request.
```

## Security

Please don't open a public issue for a security problem (for example a way for an agent to escape its worktree or read the daemon token). Report it privately through GitHub's **Report a vulnerability** button on the Security tab.
