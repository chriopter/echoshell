# echoshell

KISS tmux session picker for repos in `~/git/*`.

## Build
```bash
go build -o echoshell .
```

## Install
```bash
go build -o echoshell .
mkdir -p ~/.local/bin
ln -sf "$(pwd)/echoshell" ~/.local/bin/echoshell
```

## Run
Local:
```bash
echoshell
```

Via mosh to the target machine (recommended):
```bash
mosh root@your-server -- echoshell
```

`echoshell` is local-only: it always talks to tmux on the machine where `echoshell` is running.

You do not need to run tmux manually in the command. `echoshell` auto-starts inside tmux when needed.

## Keys
- `1..9`: select repo
- `Tab` / `Shift+Tab`: next/prev repo
- `Left/Right`: prev/next repo
- `Up/Down`: move through repos and sessions
- `Enter`: attach selected session
- `n`: new session template menu
- `d`: destroy selected session
- `0`: menu (refresh/update/quit)
- `o`: spawn `opencode`
- `l`: spawn `lazygit`
- `c`: spawn claude full
- `b`: spawn bash
- `r`: refresh
- `q` / `Esc`: quit

Search args are fuzzy:
- 1 arg: match across repo/session/workspace
- 2+ args: first arg matches repo, remaining args match session name (for example `echoshell op la`)

Safety: the tmux session currently running `echoshell` is hidden from the picker and cannot be destroyed from inside `echoshell`.
