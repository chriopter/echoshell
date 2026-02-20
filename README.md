# echoshell

KISS tmux picker.

- Lists tmux sessions
- Shows repo entries from `~/git/*`
- Includes `root` entry (new sessions there start in `/`)
- Attaches fast from a small TUI
- Runs local-only inside tmux (KISS)

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
./echoshell v l
```

## Recommended remote usage (mosh)

Run echoshell directly on the remote host inside tmux:

```bash
mosh root@your-server -- tmux new -As main
echoshell
```

Passing search terms (for example `v l`) does quick fuzzy matching against existing session names/workspace/repo:
- one match: attach immediately
- multiple matches: opens a selector

## Keys

**Session Management:**
- `1..9`: select repo
- `Tab` / `Shift+Tab`: next/prev repo
- `Left/Right`: prev/next repo
- `Up/Down`: move vertically through repos and sessions
- `Enter`: full attach to selected session
- `0`: command menu (refresh/update/quit/etc)
- `o`: spawn `opencode` in selected repo
- `l`: spawn `lazygit` in selected repo
- `c`: spawn claude FULL in selected repo
- `b`: spawn bash shell in selected repo
- `r`: refresh
- `q` or `Esc`: quit

When running inside tmux, moving selection updates a soft-attach preview in a right split pane.

`n` command picker options:
- `Shell (default)`
- `Claude (claude)`
- `Claude FULL (sandbox off)`
- `OpenCode (opencode)`
- `Lazygit (lazygit)`

New session names use `repo-command-number` (example: `valiido-lazygit-1`).

Session preview updates as you move selection.
After `n`, new session becomes selected and previewed.

Last repo group selection is stored in `~/.config/echoshell/workspaces.txt`.
