#!/bin/bash
# Install CCSA as a macOS launchd service
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY_NAME="claude-code-slack-anywhere"
PLIST_TEMPLATE="$SCRIPT_DIR/com.ccsa.plist"
PLIST_DEST="$HOME/Library/LaunchAgents/com.ccsa.plist"
BINARY_DEST="$HOME/bin/$BINARY_NAME"

echo "Installing CCSA service..."

# Build if needed
if [[ ! -f "$SCRIPT_DIR/$BINARY_NAME" ]]; then
    echo "Building binary..."
    cd "$SCRIPT_DIR"
    go build -o "$BINARY_NAME" .
fi

# Create ~/bin if needed
mkdir -p "$HOME/bin"

# Install binary
echo "Installing binary to $BINARY_DEST"
cp "$SCRIPT_DIR/$BINARY_NAME" "$BINARY_DEST"
chmod +x "$BINARY_DEST"

# Unload existing service
if launchctl list | grep -q com.ccsa; then
    echo "Stopping existing service..."
    launchctl unload "$PLIST_DEST" 2>/dev/null || true
fi

# Install plist with paths substituted
echo "Installing launchd plist..."
sed -e "s|__BINARY_PATH__|$BINARY_DEST|g" \
    -e "s|__HOME__|$HOME|g" \
    "$PLIST_TEMPLATE" > "$PLIST_DEST"

# Load service
echo "Starting service..."
launchctl load "$PLIST_DEST"

sleep 1

# Verify
if launchctl list | grep -q com.ccsa; then
    PID=$(launchctl list | grep com.ccsa | awk '{print $1}')
    echo ""
    echo "Service installed and running (PID: $PID)"
    echo ""
    echo "Useful commands:"
    echo "  tail -f ~/.ccsa.log                           # View logs"
    echo "  launchctl kickstart -k gui/\$(id -u)/com.ccsa  # Restart"
    echo "  launchctl unload ~/Library/LaunchAgents/com.ccsa.plist  # Stop"
else
    echo "Warning: Service may not have started. Check: launchctl list | grep ccsa"
fi
