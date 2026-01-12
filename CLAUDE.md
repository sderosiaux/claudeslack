# claudeslack

## Build & Deploy

```bash
make build    # Build with timestamp
make install  # Build + install to ~/bin
make reload   # Build + install + reload launchd service (use this!)
make restart  # Build + restart local process (dev only)
```

## Verify

In Slack: `!version` → shows `v2.0.0 (build: 20260111-174449)`

## launchd Service

The bot runs as a macOS launchd service:
- Plist: `~/Library/LaunchAgents/com.ccsa.plist`
- Logs: `~/.ccsa.log`
- Auto-restarts on crash

## Sessions

Sessions are persisted in `/tmp/claude-slack-sessions.json` (survives server restarts, not machine reboots).
