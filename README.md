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

Remote (recommended):
```bash
mosh root@your-server -- echoshell
```

You do not need to run tmux manually in the command. `echoshell` auto-starts inside tmux when needed.

## Keys
- `1..9`: select repo
- `Tab` / `Shift+Tab`: next/prev repo
- `Left/Right`: prev/next repo
- `Up/Down`: move through repos and sessions
- `Enter`: attach selected session
- `0`: menu (refresh/update/quit)
- `o`: spawn `opencode`
- `l`: spawn `lazygit`
- `c`: spawn claude full
- `b`: spawn bash
- `r`: refresh
- `q` / `Esc`: quit

Search args (for example `echoshell v l`) do quick fuzzy attach.
