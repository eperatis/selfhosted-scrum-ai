# Scrum Board

A single-user, self-hosted Scrum board. One Go binary serves the web UI and stores all data in `board.json`.

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
./scrum-board     # creates board.json here on first save
```

Open `http://server-ip:8080`.

If the page doesn't load and the server runs Fedora or RHEL, open the port in the firewall:

```bash
sudo firewall-cmd --add-port=8080/tcp --permanent && sudo firewall-cmd --reload
```

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

## Updating

```bash
go build -o scrum-board .
scp scrum-board you@server:~/scrum-board/scrum-board.new
ssh you@server 'cd ~/scrum-board && mv scrum-board.new scrum-board && sudo systemctl restart scrum-board'
```

`board.json` isn't touched, so your tickets stay. Copying to `.new` first and then renaming avoids a "text file busy" error while the old binary is still running.

## Backup

All your data is in one file:

```bash
scp you@server:~/scrum-board/board.json ./board-backup.json
```
