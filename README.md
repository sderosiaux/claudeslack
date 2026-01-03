# Claude Code Slack Anywhere

> Control [Claude Code](https://claude.ai/claude-code) remotely via Slack. Start sessions from your phone, interact with Claude, and receive notifications when tasks complete.

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## Why?

Ever wanted to:
- Start a Claude Code session from your phone while away from your computer?
- Continue a session seamlessly between your phone and PC?
- Get notified when Claude finishes a long-running task?

**Claude Code Slack Anywhere** bridges Claude Code with Slack, letting you control sessions from anywhere.

## Features

- **100% Self-Hosted** - Runs entirely on your machine, no third-party servers
- **Privacy First** - Your code and conversations never leave your computer
- **Remote Control** - Start and manage Claude Code sessions from Slack
- **Live Streaming** - See Claude's output in real-time as a single updating message
- **Reaction Status** - Visual feedback: :eyes: (processing) → :white_check_mark: (done)
- **Multi-Session** - Run multiple concurrent sessions, each with its own Slack channel
- **Seamless Handoff** - Start on phone, continue on PC (or vice versa)
- **Interactive Buttons** - Answer Claude's questions with Block Kit buttons
- **tmux Integration** - Sessions persist and can be attached from any terminal

## Demo Workflow

```
Slack (phone/desktop)           PC (Terminal)
───────────────────────────────────────────────────
1. !new myproject
   → Creates #myproject channel + session

2. "Fix the auth bug"
   → 👀 reaction appears on your message
   → Claude's response streams to thread

3. Response complete
   → ✅ reaction replaces 👀
   → Full output visible in thread

                                4. cd ~/myproject && claude-code-slack-anywhere
                                   → Attaches to same session

                                5. Continue working with Claude
```

## Requirements

- macOS, Linux, or Windows (WSL)
- Go 1.21+
- [tmux](https://github.com/tmux/tmux)
- [Claude Code](https://claude.ai/claude-code) installed
- Slack workspace (free tier works!)

## Installation

### From Source

```bash
git clone https://github.com/sderosiaux/claude-code-slack-anywhere.git
cd claude-code-slack-anywhere
go mod tidy
go build -o claude-code-slack-anywhere .
mv claude-code-slack-anywhere ~/bin/  # or anywhere in PATH
```

### Verify Installation

```bash
claude-code-slack-anywhere --version
# claude-code-slack-anywhere version 2.0.0
```

## Quick Start

### 1. Create a Slack App

Go to [api.slack.com/apps](https://api.slack.com/apps) → **Create New App** → **From scratch**

| Setting | Location | Value |
|---------|----------|-------|
| Socket Mode | Socket Mode | **ON** + create token with `connections:write` → save `xapp-...` |
| Bot Scopes | OAuth & Permissions | `channels:manage`, `channels:history`, `channels:read`, `chat:write`, `reactions:write`, `users:read` |
| Events | Event Subscriptions | **ON** + add `message.channels` |
| Interactivity | Interactivity & Shortcuts | **ON** |
| Install | Install App | Click install → copy `xoxb-...` token |

> **Important:** `reactions:write` is required for the :eyes:/:white_check_mark: status indicators

### 2. Run Setup

```bash
claude-code-slack-anywhere setup xoxb-YOUR-BOT-TOKEN xapp-YOUR-APP-TOKEN
```

Get your User ID: Slack → Profile → **...** → **Copy member ID**

### 3. Start the Listener

```bash
claude-code-slack-anywhere listen
```

Keep this running (or set up as a service). Now you can control Claude from Slack!

## Usage

### Slack Commands

Type these in any channel where the bot is present:

| Command | Description |
|---------|-------------|
| `!new <name>` | Create new session + channel |
| `!continue [name]` | Continue existing session (name optional in session channel) |
| `!kill [name]` | Kill a session (name optional in session channel) |
| `!list` | List active sessions |
| `!output [lines]` | Capture Claude's screen (default: 100 lines) |
| `!ping` | Check if bot is alive |
| `!help` | Show all commands |
| `!c <cmd>` | Run shell command on your machine |

### In a Session Channel

| Input | Description |
|-------|-------------|
| Any message | Sent directly to Claude |
| `//help` | Claude's `/help` command (use `//` because Slack intercepts `/`) |
| `//compact` | Claude's `/compact` command |
| `//clear` | Claude's `/clear` command |

> **Note:** Use double-slash `//` for Claude slash commands. Single `/` is intercepted by Slack.

### Reaction Status

When you send a message in a session channel:

| Reaction | Meaning |
|----------|---------|
| :eyes: | Message received, Claude is processing |
| :white_check_mark: | Claude finished successfully |
| :octagonal_sign: | Session ended |
| :x: | Error occurred |

### Example Session

```bash
# On your PC - start working on a project
cd ~/myproject
claude-code-slack-anywhere
# Claude session starts in tmux

# Later, from phone - check on progress
# Slack: Send message in #myproject channel
# → 👀 appears on your message
# → Claude's response streams to thread
# → ✅ when done

# Back on PC - continue where you left off
cd ~/myproject
claude-code-slack-anywhere
# Attaches to existing session
```

## Configuration

Config is stored in `~/.ccsa.json`:

```json
{
  "bot_token": "xoxb-your-bot-token",
  "app_token": "xapp-your-app-token",
  "user_id": "U01234567",
  "projects_dir": "~/Desktop/ai-projects",
  "sessions": {
    "myproject": "C01234567"
  }
}
```

| Field | Description |
|-------|-------------|
| `bot_token` | Slack Bot User OAuth Token (xoxb-...) |
| `app_token` | Slack App-Level Token (xapp-...) |
| `user_id` | Your Slack member ID (for authorization) |
| `projects_dir` | Base directory for projects (default: `~/Desktop/ai-projects`) |
| `sessions` | Map of session names to channel IDs |

## How It Works

```
┌─────────────┐     Socket Mode      ┌─────────────┐     send-keys     ┌─────────────┐
│    Slack    │◄────────────────────►│   Listener  │─────────────────►│    tmux     │
│   (phone)   │                      │             │                   │   session   │
└─────────────┘                      │             │                   └──────┬──────┘
      ▲                              │             │                          │
      │                              │             │◄── capture-pane ─────────┤
      │                              │             │    (poll every 2s)       ▼
      │                              └─────────────┘                   ┌─────────────┐
      │                                    │                           │ Claude Code │
      └────────────────────────────────────┘                           └─────────────┘
              updateMessage() with streamed output
```

1. `claude-code-slack-anywhere listen` connects to Slack via Socket Mode
2. Messages in session channels are forwarded to the corresponding tmux session
3. Listener polls tmux output every 2 seconds
4. New output is streamed back as an updating Slack message
5. Reactions indicate status (:eyes: → :white_check_mark:)

## Privacy & Security

### Privacy

**This tool runs 100% on your machine.** There are no external servers, no analytics, no data collection.

- Your code stays on your computer
- Claude Code runs locally via Anthropic's official CLI
- Only messages you explicitly send go through Slack
- No telemetry, no tracking, no cloud dependencies

The only external communication is:
1. **Slack API** - For sending/receiving your messages (your workspace, your control)
2. **Anthropic API** - Claude Code's own connection (handled by Claude Code itself)

### Security

- **Authorization**: Bot only accepts messages from the configured `user_id`
- **Config permissions**: `~/.ccsa.json` is created with `0600` (owner-only)
- **Socket Mode**: No public URL needed, connection initiated from your machine
- **Open source**: Full code transparency, audit it yourself

> Note: Uses `--dangerously-skip-permissions` for automation - understand the implications

## Troubleshooting

Run `claude-code-slack-anywhere doctor` to check all dependencies and configuration.

**Bot not responding?**
- Check if `claude-code-slack-anywhere listen` is running
- Verify tokens in `~/.ccsa.json`
- Check logs: `tail -f ~/.ccsa.log`

**No reactions appearing?**
- Add `reactions:write` scope to your Slack app
- Reinstall the app after adding the scope

**Session not starting?**
- Ensure tmux is installed: `which tmux`
- Check if Claude Code is installed: `which claude`

**Messages not reaching Claude?**
- Verify you're in a session channel
- Check if session exists: `!list`
- Try restarting: `!continue`

**Socket Mode connection issues?**
- Verify App Token has `connections:write` scope
- Check Event Subscriptions are enabled
- Restart the listener

## Running as a Service (macOS)

Create `~/Library/LaunchAgents/com.ccsa.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.ccsa</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/YOUR_USERNAME/bin/claude-code-slack-anywhere</string>
        <string>listen</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/YOUR_USERNAME/.ccsa.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/YOUR_USERNAME/.ccsa.log</string>
</dict>
</plist>
```

Then:
```bash
launchctl load ~/Library/LaunchAgents/com.ccsa.plist
```

## Contributing

Contributions welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Run tests: `go test ./...`
4. Submit a PR

See [TODO.md](TODO.md) for planned features.

## License

[MIT License](LICENSE) - feel free to use in your projects!

---

Made with Claude Code
