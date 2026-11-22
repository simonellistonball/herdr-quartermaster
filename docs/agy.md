# Workers and the coordinator on Antigravity (agy)

[Antigravity](https://antigravity.google/)'s CLI, `agy`, is an opt-in fifth
agent kind next to `claude`, `codex`, `muse` and `pi`: `--agent agy`,
`worker_agent = "agy"` or `coordinator_agent = "agy"`. Nothing changes until
you ask for it. It is the way to run a worker on Google's Gemini models
(Gemini 4 Neon and friends). Checked against agy 1.5.14 and herdr 0.9.5.

## What works

| | How |
|---|---|
| Start | `herdr agent start --kind agy`; herdr detects agy itself and reports working, idle and blocked. |
| Models | agy's own default, unless `[agents.agy] args` passes `--model <name>` (below). `agy models` lists the names your sign-in can run. |
| Mode | Workers and the coordinator start with `--mode auto` unless `[agents.agy] args` says otherwise (below). |
| Brief | Typed in with `herdr agent prompt`, as for Claude. agy has no SessionStart hook that loads `bd prime`; the brief already says to run it first. |
| Resume | `quartermaster resume` restarts agy with `--continue` in the bead's worktree: its most recent conversation there, as with Claude. A bead agy claimed resumes as agy. |
| Board | `t` starts agy (the board's `a`, `g` and `y` were already taken). Herdr's sidebar shows the kind as agy. |
| Env | `BEADS_ACTOR=agy` and `BEADS_DIR` are exported in the pane before agy starts, as for every kind, so `bd` works from the worktree and claims are recorded as agy. |
| Close verifier | Unchanged: `claude -p`, whatever the worker's kind. |

## Interactive sessions only: the stdin caveat

quartermaster starts agy the interactive way — `herdr agent start --kind agy`
in a pane, then the brief is prompted into that live session (agy's
`--prompt-interactive` shape, delivered by herdr). It never uses agy's print
mode (`agy --print`/`-p`): print mode takes its prompt from its argument or
stdin, and with stdin closed or empty it waits instead of working — and
signed out, it drops into an interactive OAuth wait. A worker has nobody
watching its stdin, so the pane session is the only covered path. The same
goes for `quartermaster tell` and ticker prompts after the brief: typed into
the pane, only when the worker is at rest, as for Claude, Codex and Muse.

## Sign-in is yours

Antigravity keeps its own auth and session state (on Linux under
`~/.gemini/antigravity-cli`); quartermaster never reads or writes it, and
exports nothing into agy's environment but the usual `BEADS_*` and what
`[agents.agy] env` names. Sign in once, yourself, before dispatching:

```sh
agy            # pick Google OAuth (or a Google Cloud project) and finish in the browser
agy models     # should list models instead of "Please sign in"
```

Signed out, a dispatched agy worker sits at its "Select login method" screen
and the brief goes nowhere useful; herdr still reports the pane as an idle
agy agent, so check `agy models` first. A `GEMINI_API_KEY` or Application
Default Credentials sign-in works too and changes which models `agy models`
offers (Gemini 4 Neon arrived through those first); quartermaster doesn't
care which you used.

## Choosing a Gemini model

There is no quartermaster-level model setting; agy's own `--model` goes in
the kind's args, like any other CLI's:

```toml
[agents.agy]
args = ["--mode", "auto", "--model", "<a name from `agy models`>"]
coordinator_args = ["--mode", "auto", "--model", "<a name from `agy models`>"]
```

`args` replace the default (`--mode auto`), so keep `--mode auto` in them
unless you mean to run `accept-edits` or `plan`. `--effort
low|medium|high|xhigh|max` is agy's reasoning-effort flag and goes in the
same place. Then:

```sh
quartermaster dispatch <bead> --agent agy
quartermaster coordinator --agent agy   # or coordinator_agent = "agy"
```

## Permissions

Without an `[agents.agy]` table, agy starts with `--mode auto`. Auto is
agy's own default mode: work inside the workspace proceeds, reads outside it
are allowed, writes outside it ask, and a review judge decides commands that
would leave agy's terminal sandbox — the same shape as Muse's
`:auto-review` default, so an unattended worker isn't left waiting on a
prompt nobody is watching for routine work. quartermaster pins it because a
mode saved in agy's `/config` (say `plan`) would otherwise apply to workers
too.

agy has no default bypass args, like Muse and pi: a worker under a 9 lease
keeps Auto mode's prompts. To opt in, name agy's own switch:

```toml
[agents.agy]
bypass_args = ["--dangerously-skip-permissions"]
```

One caution with that: when a worker's level crosses 9 the ticker relaunches
it by prompting `/exit` and resuming. Whether agy treats `/exit` as its other
kinds do is unproven; until it is, a bypass-configured agy worker may need a
manual exit and `quartermaster resume` at that point.

## What still assumes the other kinds

- **Approval prompts.** `quartermaster prompt` and `answer` parse the pane
  generically (a numbered or cursor menu over an esc/enter footer), and
  agy's menus have that shape, but no agy approval screen has been verified
  end to end. If a prompt doesn't parse, the thread shows as blocked with no
  hash to answer; answer it in the pane.
- **needs_me wake check.** quartermaster can't read agy's session files, so
  it can't tell a typed prompt from agy waking on its own — the same
  "unknown" as Codex and Muse workers.
- **No headless or RPC path.** agy is a Herdr-pane kind only. `--backend
  pi-rpc` still requires `--agent pi`, and the fresh-session handoff to pi
  still starts from a Claude, Codex or Muse worker, not an agy one.
- **`worker_agent = "auto"`** never picks agy: it records no usage reading
  to compare.
- **The brief's last lines** name Claude Code permission prompts and the
  classifier. For agy, Auto mode's judge and its prompts are the equivalent
  surface; a command the judge won't pass is handed over the same way (a
  `RUN:` note and `needs_me`).
