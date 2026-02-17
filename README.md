# echoshell (prototype)

`echoshell` is an isolated Go prototype of a simple remote tmux manager with a Bubble Tea UI.

- No Sandboxer API usage.
- Runs locally and controls tmux on a remote server over SSH.
- Lets you save server profiles locally (name + `user@host`).
- Connects to a saved server and lists all remote tmux sessions.
- `n` creates a new fixed `grid-2x2` session and opens it immediately.
- `N` creates an `ops-2w` session (2x2 main window + extra ops window).
- `Enter` attaches to an existing session.

## Build

```bash
cd echoshell
go build -o echoshell .
```

Requirements:

- Go 1.22+
- tmux installed on the remote server
- SSH key-based access to remote server

## Usage

```bash
./echoshell
```

## UI keys

- Server list screen:
  - `a`: add server (name, then connection like `root@10.0.0.5`)
  - `Enter`: connect to selected server
  - `d`: delete selected server
  - `j/k` or arrows: move selection
- Session screen (after connect):
  - `n`: create a new `grid-2x2` tmux session and open it
  - `N`: create a new `ops-2w` tmux session and open it
  - `Enter`: attach selected existing session
  - `r`: refresh sessions
  - `b` or `Esc`: back to server list
  - `j/k` or arrows: move selection
  - `q`: quit

## Suggested remote workflow

```bash
cd /path/to/repo/echoshell
go build -o echoshell .
./echoshell
```

Server profiles are saved to:

`~/.config/echoshell/servers.json`

Session layout metadata is saved on remote tmux sessions via user options:

- `@echoshell_profile`
- `@echoshell_layout_version`
