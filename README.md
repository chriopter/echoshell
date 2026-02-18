# echoshell

KISS tmux picker.

- Lists tmux sessions
- Shows repo entries from `/root/workspaces/*/data/repos/*`
- Includes `root` entry (new sessions there start in `/`)
- Attaches fast from a small TUI
- Auto-connects to last target (optional target picker)
- Reuses SSH control connections across echoshell instances for faster remote loads

## Build
```bash
go build -o echoshell .
```

## Install (recommended)

Build in the repo, then put a symlink on your PATH so `echoshell` always runs the repo-built binary:

```bash
go build -o echoshell .
mkdir -p ~/.local/bin
ln -sf "$(pwd)/echoshell" ~/.local/bin/echoshell
```

If `~/.local/bin` is not on your PATH, add it in your shell config.

The `u` (update) action will work with this setup because it rebuilds in the git repo and the symlink keeps pointing at the updated binary.

## Run
```bash
./echoshell
ECHOSHELL_REMOTE=root@your-server ./echoshell
./echoshell v l
```

Passing search terms (for example `v l`) does quick fuzzy matching against existing session names/workspace/repo:
- one match: attach immediately
- multiple matches: opens a selector

## Keys

**Remote Selection (on startup):**
- `j/k` or `up/down`: navigate targets
- `Enter`: select target (or "+ Add new remote..." to add a new one)
- When adding new: type `user@host`, press `Enter` to confirm, `Esc` to cancel
- `q` or `Esc`: quit

Set `ECHOSHELL_SELECT_REMOTE=1` to always show this picker on startup.

**Session Management:**
- `1..9`: select repo
- `Tab` / `Shift+Tab`: next/prev repo
- `Left/Right`: prev/next repo
- `Up/Down`: session selection (wraps across repos)
- `0`: command menu (refresh/update/quit/etc)
- `o`: spawn `opencode` in selected repo
- `l`: spawn `lazygit` in selected repo
- `c`: spawn claude FULL in selected repo
- `b`: spawn bash shell in selected repo
- `r`: refresh
- `q` or `Esc`: quit

`n` command picker options:
- `Shell (default)`
- `Claude (claude)`
- `Claude FULL (IS_SANDBOX=1 claude --dangerously-skip-permissions)`
- `OpenCode (opencode)`
- `Lazygit (lazygit)`

New session names use `repo-command-number` (example: `valiido-lazygit-1`).

Session preview updates as you move selection.
After `n`, new session becomes selected and previewed.

Last target is stored in `~/.config/echoshell/targets.txt`.
Last workspace per target is stored in `~/.config/echoshell/workspaces.txt`.
