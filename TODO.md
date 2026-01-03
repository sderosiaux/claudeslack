# TODO

## High Priority

### Image Support
- [ ] Detect image attachments in Slack messages
- [ ] Download images from Slack (using `files.info` API)
- [ ] Save images to temp directory
- [ ] Pass image path to Claude Code prompt (Claude Code supports images)
- [ ] Clean up temp images after processing

### Better Output Streaming
- [ ] Keep final output visible (don't replace with "finished")
- [ ] Add completion indicator without losing content
- [ ] Show tool usage in real-time (Read, Write, Bash, etc.)

## Medium Priority

### File Attachments
- [ ] Support uploading text/code files from Slack
- [ ] Inject file contents into Claude prompt
- [ ] Support common formats: .txt, .js, .py, .go, .json, .yaml, etc.

### Voice Messages
- [ ] Detect Slack voice messages
- [ ] Transcribe using Whisper API or local model
- [ ] Send transcription to Claude

### Session Management
- [ ] `!pause` / `!resume` - pause streaming without killing session
- [ ] `!snapshot` - save session state
- [ ] `!logs` - get recent session activity
- [ ] Auto-cleanup stale sessions after X hours

### Output Improvements
- [ ] Syntax highlighting for code blocks
- [ ] Collapsible long outputs
- [ ] Show diffs for file changes
- [ ] Progress bar for long operations

## Low Priority

### Multi-User Support
- [ ] Allow multiple authorized user IDs
- [ ] Per-user session isolation
- [ ] Session sharing/handoff between users

### Integrations
- [ ] GitHub PR notifications in session channel
- [ ] Auto-commit summaries posted to Slack
- [ ] Cost tracking / budget alerts
- [ ] Webhooks for external integrations

### UX Improvements
- [ ] Custom command aliases
- [ ] Slack slash commands (`/claude new myproject`)
- [ ] Thread-based conversations (reply = continue context)
- [ ] Reactions to trigger actions (e.g., :x: to cancel)

### Mobile Experience
- [ ] Optimized output for mobile screens
- [ ] Push notifications for specific events
- [ ] Quick action buttons for common tasks

## Ideas / Exploration

- [ ] Web dashboard for session management
- [ ] Local LLM fallback when Anthropic API is down
- [ ] Session recording/playback
- [ ] Collaborative sessions (multiple users same session)
- [ ] Integration with VS Code remote
- [ ] Docker container support
