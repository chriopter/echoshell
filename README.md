# echoshell

KISS tmux picker.

- Lists tmux sessions
- Shows repo entries from `/root/workspaces/*/data/repos/*`
- Includes `root` entry (new sessions there start in `/`)
- Attaches fast from a small TUI
- Works local or remote with target selection on startup

## Build
```bash
go build -o echoshell .
```

## Run
```bash
./echoshell
ECHOSHELL_REMOTE=root@your-server ./echoshell
```

## Keys

**Remote Selection (on startup):**
- `j/k` or `up/down`: navigate targets
- `Enter`: select target (or "+ Add new remote..." to add a new one)
- When adding new: type `user@host`, press `Enter` to confirm, `Esc` to cancel
- `q` or `Esc`: quit

**Session Management:**
- `Tab`: next repo entry
- `1..4`: session quick select
- `j/k` or `up/down`: session up/down
- `n`: create new session
- `d`: destroy selected session
- `u`: update from `origin/main` and rebuild locally
- `Enter`: attach
- `r`: refresh
- `q` or `Esc`: quit

Session preview updates as you move selection.
After `n`, new session becomes selected and previewed.

Last target is stored in `~/.config/echoshell/targets.txt`.
