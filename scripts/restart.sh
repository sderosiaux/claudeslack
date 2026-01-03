#!/bin/bash
# Rebuild and restart claude-code-slack-anywhere service

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

echo "Building..."
go build -o claude-code-slack-anywhere .

echo "Installing to ~/bin..."
cp claude-code-slack-anywhere ~/bin/

echo "Restarting service..."
launchctl unload ~/Library/LaunchAgents/com.ccsa.plist 2>/dev/null || true

# Clean up any rogue listener processes (manual nohup starts, background tasks, etc)
echo "Cleaning up old listeners..."
pkill -9 -f "claude-code-slack-anywhere listen" 2>/dev/null || true
sleep 1

launchctl load ~/Library/LaunchAgents/com.ccsa.plist

sleep 2
echo ""
echo "Service status:"
tail -5 ~/.ccsa.log
