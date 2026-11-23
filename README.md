# Scrum Board

A single-user, self-hosted Scrum board. One Go binary serves the web UI and stores all data in `board.json`.

## Intended use

This project is meant for **one person on one computer**. It has no login, no user accounts and no encryption, on purpose:

- By default it only accepts connections from the same computer.
- `-lan` opens it to the local network. Only use that on a network you trust, such as your own home Wi-Fi. Anyone on that network can read, change or delete every board.
- Traffic is plain HTTP, so don't use `-lan` on public or shared Wi-Fi.

**No warranty.** The software is provided as is. The author is not responsible for data loss or any other damage, including when it is used by other people, on other machines, or in ways other than described here. Keep your own backups of `board.json` (see [Backup](#backup)).

What it does protect against:

- **Other websites in your browser:** the server only answers requests addressed to `localhost`, an IP address or the computer's own name, so a malicious website can't reach the board through DNS rebinding. The LLM API only accepts `Content-Type: application/json`, so websites can't create tickets through your browser either.
- **Broken or crafted board data:** every save is checked (ids, columns, dates) before it's written.
- **Other users on the same computer:** `board.json` is only readable by the user running the server.

## 1. Install Go on your PC (once)

```bash
sudo dnf install golang
go version        # needs 1.22 or newer
```

## 2. Build

```bash
cd ~/workspace/selfhosted-scrum-ai
go build -o scrum-board .
```

This creates one file, `scrum-board`, with the web files inside it.

If the server has a different CPU, check it with `uname -m` on the server and build for that:

```bash
GOOS=linux GOARCH=amd64 go build -o scrum-board .   # x86_64
GOOS=linux GOARCH=arm64 go build -o scrum-board .   # aarch64, e.g. Raspberry Pi 4/5
```

Optional quick test on your PC:

```bash
./scrum-board     # open http://localhost:8080, Ctrl+C to stop
```

## 3. Copy to the server

```bash
ssh you@server 'mkdir -p ~/scrum-board'
scp scrum-board you@server:~/scrum-board/
```

## 4. Run on the server

```bash
ssh you@server
cd ~/scrum-board
./scrum-board -lan     # creates board.json here on first save
```

Open `http://server-ip:8080`.

> **Note:** By default the app is only reachable from the same machine. `-lan` makes it reachable from the whole network, with no login, so only use it on a trusted network (see [Intended use](#intended-use)). Open it by IP address or the machine's name; other domain names pointing at it are refused.

If the page doesn't load and the server runs Fedora or RHEL, open the port in the firewall:

```bash
sudo firewall-cmd --add-port=8080/tcp --permanent && sudo firewall-cmd --reload
```

If the LLM runs on another machine, open its port too: `--add-port=8081/tcp`.

## 5. Start on boot (optional)

Edit `scrum-board.service` and replace `you` with your username on the server. Then:

```bash
scp scrum-board.service you@server:/tmp/
ssh you@server
sudo mv /tmp/scrum-board.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now scrum-board
systemctl status scrum-board          # check it's running
journalctl -u scrum-board -f          # view logs
```

> **Note:** On Fedora or RHEL servers, SELinux may stop systemd from running a binary in your home folder. If `systemctl status` shows a permission error, move the folder to `/opt/scrum-board` and update both paths in the unit file.

## Options

| Flag | Default | Meaning |
|---|---|---|
| `-lan` | off | allow other devices on the network to connect |
| `-port` | `8080` | port to listen on |
| `-llm` | off | start the LLM API on its own port |
| `-llm-port` | `8081` | port for the LLM API |
| `-data` | `board.json` | path to the board data file |

## LLM API

A local LLM or script can read the boards and create boards and tickets over HTTP on a separate port. It's off by default; start the server with `-llm` to turn it on:

```bash
./scrum-board -llm          # LLM API on port 8081
./scrum-board -lan -llm     # both together
```

That port can't change or delete anything, so give the LLM this address and not the board's `8080`:

```bash
curl http://localhost:8081/api/boards              # every board with ticket counts per column
curl http://localhost:8081/api/boards/board-main   # one board's tickets, comments and blocked flags

# create a board; the reply holds its new id
curl -X POST http://localhost:8081/api/boards -H 'Content-Type: application/json' -d '{"name": "Sprint 12"}'

# create a ticket; only title is required, column defaults to backlog
curl -X POST http://localhost:8081/api/boards/board-main/tickets -H 'Content-Type: application/json' \
  -d '{"title": "Fix login", "desc": "...", "column": "todo", "dueDate": "2026-11-15", "blockedBy": ["task-abc"]}'
```

`column` is one of `backlog`, `todo`, `inprogress`, `review`, `done`, and `blockedBy` lists ticket ids from the same board. A bad request gets a `400` with a message saying what to fix. `POST` requests must send `Content-Type: application/json`, otherwise they get a `415`.

An open board tab picks up new boards and tickets within a few seconds. If you save a change at the same moment the LLM creates something, your change isn't saved and the board reloads, so nothing the LLM added gets overwritten; just redo it.

Like the board, it's only reachable from the same machine unless you start with `-lan`.

## Updating

```bash
go build -o scrum-board .
scp scrum-board you@server:~/scrum-board/scrum-board.new
ssh you@server 'cd ~/scrum-board && mv scrum-board.new scrum-board && sudo systemctl restart scrum-board'
```

`board.json` isn't touched, so your tickets stay. Copying to `.new` first and then renaming avoids a "text file busy" error while the old binary is still running.

## Backup

All your data, every board, is in one file:

```bash
scp you@server:~/scrum-board/board.json ./board-backup.json
```
