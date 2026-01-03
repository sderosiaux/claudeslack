package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/websocket"
)

const version = "2.0.0"

// sanitizeSessionName converts a name to a valid tmux session name
// Only allows alphanumeric, hyphens, and underscores
func sanitizeSessionName(name string) string {
	var result strings.Builder
	lastWasHyphen := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			result.WriteRune(r)
			lastWasHyphen = false
		} else if r == ' ' || r == '-' || r == '.' || r == '/' {
			if !lastWasHyphen && result.Len() > 0 {
				result.WriteRune('-')
				lastWasHyphen = true
			}
		}
	}
	s := result.String()
	return strings.Trim(s, "-")
}

// tmuxSessionName returns the tmux session name for a given project name
func tmuxSessionName(name string) string {
	return "claude-" + sanitizeSessionName(name)
}

// getProjectsDir returns the base directory for projects from config or default
func getProjectsDir(config *Config) string {
	if config != nil && config.ProjectsDir != "" {
		// Expand ~ if present
		if strings.HasPrefix(config.ProjectsDir, "~/") {
			home, _ := os.UserHomeDir()
			return filepath.Join(home, config.ProjectsDir[2:])
		}
		return config.ProjectsDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Desktop", "ai-projects")
}

// Config stores bot configuration and session mappings
type Config struct {
	BotToken    string            `json:"bot_token"`              // Slack Bot Token (xoxb-...)
	AppToken    string            `json:"app_token"`              // Slack App Token (xapp-...) for Socket Mode
	UserID      string            `json:"user_id"`                // Authorized Slack user ID
	Sessions    map[string]string `json:"sessions"`               // session name -> channel ID
	ProjectsDir string            `json:"projects_dir,omitempty"` // Base directory for projects (default: ~/Desktop/ai-projects)
}

// Slack API types

type SlackResponse struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Channel json.RawMessage `json:"channel,omitempty"`
	TS      string          `json:"ts,omitempty"`
	URL     string          `json:"url,omitempty"` // For Socket Mode connection
}

type SlackChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SlackUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SlackMessage struct {
	Type      string `json:"type"`
	Channel   string `json:"channel"`
	User      string `json:"user"`
	Text      string `json:"text"`
	TS        string `json:"ts"`
	ThreadTS  string `json:"thread_ts,omitempty"`
	BotID     string `json:"bot_id,omitempty"`
}

// Socket Mode envelope
type SocketModeEnvelope struct {
	Type       string          `json:"type"`
	EnvelopeID string          `json:"envelope_id"`
	Payload    json.RawMessage `json:"payload"`
	RetryAttempt int           `json:"retry_attempt,omitempty"`
	RetryReason  string        `json:"retry_reason,omitempty"`
}

// Event callback payload
type EventCallback struct {
	Type    string          `json:"type"`
	EventID string          `json:"event_id"`
	Event   json.RawMessage `json:"event"`
}

// Block action payload (button clicks)
type BlockActionPayload struct {
	Type        string `json:"type"`
	User        SlackUser `json:"user"`
	Channel     struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"channel"`
	Message     SlackMessage `json:"message"`
	Actions     []BlockAction `json:"actions"`
	ResponseURL string `json:"response_url"`
}

type BlockAction struct {
	ActionID string `json:"action_id"`
	BlockID  string `json:"block_id"`
	Value    string `json:"value"`
	Type     string `json:"type"`
}

// Block Kit types
type Block struct {
	Type     string      `json:"type"`
	Text     *TextObject `json:"text,omitempty"`
	Elements []Element   `json:"elements,omitempty"`
	BlockID  string      `json:"block_id,omitempty"`
}

type TextObject struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Element struct {
	Type     string      `json:"type"`
	Text     *TextObject `json:"text,omitempty"`
	ActionID string      `json:"action_id,omitempty"`
	Value    string      `json:"value,omitempty"`
	Style    string      `json:"style,omitempty"`
}

// HookData represents data received from Claude hook
type HookData struct {
	Cwd            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	SessionID      string `json:"session_id"`
	HookEventName  string `json:"hook_event_name"`
	ToolName       string `json:"tool_name"`
	Prompt         string `json:"prompt"`
	ToolInput      struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	} `json:"tool_input"`
}

func getConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ccsa.json")
}

func loadConfig() (*Config, error) {
	data, err := os.ReadFile(getConfigPath())
	if err != nil {
		return nil, err
	}
	var config Config
	err = json.Unmarshal(data, &config)
	if config.Sessions == nil {
		config.Sessions = make(map[string]string)
	}
	return &config, err
}

func saveConfig(config *Config) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(getConfigPath(), data, 0600)
}

// Slack API helpers

func slackAPI(config *Config, method string, params url.Values) (*SlackResponse, error) {
	apiURL := fmt.Sprintf("https://slack.com/api/%s", method)

	req, err := http.NewRequest("POST", apiURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+config.BotToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result SlackResponse
	json.Unmarshal(body, &result)
	return &result, nil
}

func slackAPIJSON(config *Config, method string, payload interface{}) (*SlackResponse, error) {
	apiURL := fmt.Sprintf("https://slack.com/api/%s", method)

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+config.BotToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result SlackResponse
	json.Unmarshal(body, &result)
	return &result, nil
}

func sendMessage(config *Config, channelID string, text string) (string, error) {
	const maxLen = 3000

	messages := splitMessage(text, maxLen)
	var lastTS string

	for _, msg := range messages {
		params := url.Values{
			"channel": {channelID},
			"text":    {msg},
		}

		result, err := slackAPI(config, "chat.postMessage", params)
		if err != nil {
			return "", err
		}
		if !result.OK {
			return "", fmt.Errorf("slack error: %s", result.Error)
		}
		lastTS = result.TS

		if len(messages) > 1 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return lastTS, nil
}

// sendMessageToThread sends a message as a reply to an existing message (thread)
func sendMessageToThread(config *Config, channelID string, threadTS string, text string) error {
	const maxLen = 3000

	messages := splitMessage(text, maxLen)

	for _, msg := range messages {
		params := url.Values{
			"channel":   {channelID},
			"text":      {msg},
			"thread_ts": {threadTS},
		}

		result, err := slackAPI(config, "chat.postMessage", params)
		if err != nil {
			return err
		}
		if !result.OK {
			return fmt.Errorf("slack error: %s", result.Error)
		}

		if len(messages) > 1 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return nil
}

// sendMessageToThreadGetTS sends a message to a thread and returns its timestamp
func sendMessageToThreadGetTS(config *Config, channelID string, threadTS string, text string) (string, error) {
	payload := map[string]interface{}{
		"channel":   channelID,
		"text":      text,
		"thread_ts": threadTS,
	}

	result, err := slackAPIJSON(config, "chat.postMessage", payload)
	if err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("slack error: %s", result.Error)
	}
	return result.TS, nil
}

func addReaction(config *Config, channelID string, timestamp string, emoji string) error {
	params := url.Values{
		"channel":   {channelID},
		"timestamp": {timestamp},
		"name":      {emoji},
	}
	result, err := slackAPI(config, "reactions.add", params)
	if err != nil {
		logf("Reaction error: %v", err)
		return err
	}
	if !result.OK && result.Error != "already_reacted" {
		logf("Reaction API error: %s", result.Error)
		return fmt.Errorf("reaction failed: %s", result.Error)
	}
	return nil
}

func removeReaction(config *Config, channelID string, timestamp string, emoji string) error {
	params := url.Values{
		"channel":   {channelID},
		"timestamp": {timestamp},
		"name":      {emoji},
	}
	result, err := slackAPI(config, "reactions.remove", params)
	if err != nil {
		return err
	}
	if !result.OK && result.Error != "no_reaction" {
		return fmt.Errorf("remove reaction failed: %s", result.Error)
	}
	return nil
}

func sendMessageWithButtons(config *Config, channelID string, text string, buttons []Element, blockID string) error {
	payload := map[string]interface{}{
		"channel": channelID,
		"text":    text,
		"blocks": []Block{
			{
				Type: "section",
				Text: &TextObject{Type: "mrkdwn", Text: text},
			},
			{
				Type:     "actions",
				BlockID:  blockID,
				Elements: buttons,
			},
		},
	}

	result, err := slackAPIJSON(config, "chat.postMessage", payload)
	if err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("slack error: %s", result.Error)
	}
	return nil
}

func updateMessage(config *Config, channelID string, ts string, text string) error {
	payload := map[string]interface{}{
		"channel": channelID,
		"ts":      ts,
		"text":    text,
		"blocks":  []Block{}, // Remove buttons
	}

	result, err := slackAPIJSON(config, "chat.update", payload)
	if err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("slack error: %s", result.Error)
	}
	return nil
}

func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var messages []string
	remaining := text

	for len(remaining) > 0 {
		if len(remaining) <= maxLen {
			messages = append(messages, remaining)
			break
		}

		splitAt := maxLen
		if idx := strings.LastIndex(remaining[:maxLen], "\n"); idx > maxLen/2 {
			splitAt = idx + 1
		} else if idx := strings.LastIndex(remaining[:maxLen], " "); idx > maxLen/2 {
			splitAt = idx + 1
		}

		messages = append(messages, strings.TrimRight(remaining[:splitAt], " \n"))
		remaining = remaining[splitAt:]
	}

	return messages
}

func createChannel(config *Config, name string) (string, error) {
	// Slack channel names: lowercase, no spaces, max 80 chars
	channelName := strings.ToLower(name)
	channelName = strings.ReplaceAll(channelName, " ", "-")
	if len(channelName) > 80 {
		channelName = channelName[:80]
	}

	params := url.Values{
		"name": {channelName},
	}

	result, err := slackAPI(config, "conversations.create", params)
	if err != nil {
		return "", err
	}
	if !result.OK {
		// Channel might already exist
		if result.Error == "name_taken" {
			// Try to find existing channel
			return findChannelByName(config, channelName)
		}
		return "", fmt.Errorf("failed to create channel: %s", result.Error)
	}

	var channel SlackChannel
	if err := json.Unmarshal(result.Channel, &channel); err != nil {
		return "", fmt.Errorf("failed to parse channel: %w", err)
	}

	return channel.ID, nil
}

func findChannelByName(config *Config, name string) (string, error) {
	params := url.Values{
		"types": {"public_channel,private_channel"},
		"limit": {"1000"},
	}

	req, err := http.NewRequest("GET", "https://slack.com/api/conversations.list?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+config.BotToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		OK       bool           `json:"ok"`
		Channels []SlackChannel `json:"channels"`
		Error    string         `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if !result.OK {
		return "", fmt.Errorf("failed to list channels: %s", result.Error)
	}

	for _, ch := range result.Channels {
		if ch.Name == name {
			return ch.ID, nil
		}
	}

	return "", fmt.Errorf("channel not found: %s", name)
}

// Tmux session management

var (
	tmuxSocket string
	tmuxPath   string
	binPath    string
	claudePath string
)

func init() {
	tmuxSocket = fmt.Sprintf("/private/tmp/tmux-%d/default", os.Getuid())

	if path, err := exec.LookPath("tmux"); err == nil {
		tmuxPath = path
	} else {
		for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"} {
			if _, err := os.Stat(p); err == nil {
				tmuxPath = p
				break
			}
		}
	}

	if exe, err := os.Executable(); err == nil {
		binPath = exe
	}

	home, _ := os.UserHomeDir()
	claudePaths := []string{
		filepath.Join(home, ".claude", "local", "claude"),
		"/usr/local/bin/claude",
	}

	// Add NVM node paths (claude installed via npm)
	nvmDir := filepath.Join(home, ".nvm", "versions", "node")
	if entries, err := os.ReadDir(nvmDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				claudePaths = append(claudePaths, filepath.Join(nvmDir, entry.Name(), "bin", "claude"))
			}
		}
	}

	for _, p := range claudePaths {
		if _, err := os.Stat(p); err == nil {
			claudePath = p
			break
		}
	}

	// Fallback: find claude in PATH
	if claudePath == "" {
		if p, err := exec.LookPath("claude"); err == nil {
			claudePath = p
		}
	}
}

func tmuxSessionExists(name string) bool {
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "has-session", "-t", name)
	return cmd.Run() == nil
}

func createTmuxSession(name string, workDir string, continueSession bool) error {
	binCmd := binPath + " run"
	if continueSession {
		binCmd += " -c"
	}

	args := []string{"-S", tmuxSocket, "new-session", "-d", "-s", name, "-c", workDir, "/bin/zsh", "-l", "-c", binCmd}

	cmd := exec.Command(tmuxPath, args...)
	if err := cmd.Run(); err != nil {
		return err
	}

	exec.Command(tmuxPath, "-S", tmuxSocket, "set-option", "-t", name, "mouse", "on").Run()

	return nil
}

func runClaudeRaw(continueSession bool) error {
	if claudePath == "" {
		return fmt.Errorf("claude binary not found")
	}

	args := []string{"--dangerously-skip-permissions"}
	if continueSession {
		args = append(args, "-c")
	}

	cmd := exec.Command(claudePath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

func startSession(continueSession bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	name := filepath.Base(cwd)
	tmuxName := tmuxSessionName(name)

	config, err := loadConfig()
	if err != nil {
		return runClaudeRaw(continueSession)
	}

	// Create channel if it doesn't exist
	if _, exists := config.Sessions[name]; !exists {
		channelID, err := createChannel(config, name)
		if err == nil {
			config.Sessions[name] = channelID
			saveConfig(config)
			fmt.Printf("Created Slack channel: #%s\n", name)
		}
	}

	if tmuxSessionExists(tmuxName) {
		if os.Getenv("TMUX") != "" {
			cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "switch-client", "-t", tmuxName)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
		cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "attach-session", "-t", tmuxName)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	if err := createTmuxSession(tmuxName, cwd, continueSession); err != nil {
		return err
	}

	if os.Getenv("TMUX") != "" {
		cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "switch-client", "-t", tmuxName)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "attach-session", "-t", tmuxName)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func sendToTmux(session string, text string) error {
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", session, "-l", text)
	if err := cmd.Run(); err != nil {
		return err
	}

	time.Sleep(50 * time.Millisecond)
	cmd = exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", session, "C-m")
	if err := cmd.Run(); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	cmd = exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", session, "C-m")
	return cmd.Run()
}

func killTmuxSession(name string) error {
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "kill-session", "-t", name)
	return cmd.Run()
}

// captureTmuxOutput captures the current visible content of a tmux session
func captureTmuxOutput(session string, lines int) (string, error) {
	// capture-pane -p prints to stdout, -S specifies start line (negative = history)
	args := []string{"-S", tmuxSocket, "capture-pane", "-p", "-t", session}
	if lines > 0 {
		// Capture last N lines of scrollback + visible
		args = append(args, "-S", fmt.Sprintf("-%d", lines))
	}
	cmd := exec.Command(tmuxPath, args...)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// streamOutputToThread streams tmux output changes to a Slack thread
// threadTS is the user's original message - we use reactions to show status
func streamOutputToThread(config *Config, channelID string, threadTS string, tmuxName string) {
	// Capture initial output to use as baseline
	initialOutput, err := captureTmuxOutput(tmuxName, 100)
	if err != nil {
		logf("Stream: Failed to capture initial output: %v", err)
		initialOutput = ""
	}
	logf("Stream: Initial output %d chars", len(initialOutput))

	var lastSentOutput string
	var lastRawOutput string
	var replyMsgTS string // Track the reply message for updates
	unchangedCount := 0
	maxUnchanged := 60 // Stop after 60 seconds of no change

	// Helper to finish streaming with final status
	finishWithStatus := func(emoji string) {
		removeReaction(config, channelID, threadTS, "eyes")
		addReaction(config, channelID, threadTS, emoji)
	}

	// Wait a moment for Claude to start processing
	time.Sleep(1 * time.Second)

	for {
		time.Sleep(2 * time.Second)

		// Check if session still exists
		if !tmuxSessionExists(tmuxName) {
			finishWithStatus("octagonal_sign")
			return
		}

		// Capture current output
		currentOutput, err := captureTmuxOutput(tmuxName, 100)
		if err != nil {
			continue
		}

		// Check if raw output changed at all
		if currentOutput == lastRawOutput {
			unchangedCount++
			if unchangedCount >= maxUnchanged {
				// Finished successfully - mark with green check
				finishWithStatus("white_check_mark")
				return
			}
			continue
		}
		lastRawOutput = currentOutput
		unchangedCount = 0

		// Find NEW content (what appeared after initial)
		newContent := getNewContent(initialOutput, currentOutput)

		// Only update if there's new content different from last sent
		if newContent != "" && newContent != lastSentOutput {
			displayOutput := newContent
			if len(displayOutput) > 2500 {
				displayOutput = "..." + displayOutput[len(displayOutput)-2500:]
			}
			logf("Stream: Updating message with %d chars", len(displayOutput))

			// Update the reply message with actual content
			if replyMsgTS != "" {
				updateMessage(config, channelID, replyMsgTS, "```\n"+displayOutput+"\n```")
			} else {
				// Create first reply in thread
				replyMsgTS, _ = sendMessageToThreadGetTS(config, channelID, threadTS, "```\n"+displayOutput+"\n```")
			}
			lastSentOutput = newContent
		}
	}
}

// isStatusBarLine returns true if the line is a Claude UI status bar element
func isStatusBarLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	// Status bar indicators
	if strings.Contains(line, "──") || strings.Contains(line, "└─") || strings.Contains(line, "┘") {
		return true
	}
	if strings.Contains(line, "bypass") || strings.Contains(line, "shift+tab") {
		return true
	}
	if strings.HasPrefix(trimmed, "⏵") || strings.HasPrefix(trimmed, ">") && len(trimmed) < 5 {
		return true
	}
	// Cost/token indicators
	if strings.Contains(line, "Max ]") || strings.Contains(line, "Opus") || strings.Contains(line, "NO GIT") {
		return true
	}
	// Progress/thinking indicators (Transmuting, Tinkering, etc.)
	if strings.Contains(line, "esc to interrupt") || strings.Contains(line, "tokens") {
		return true
	}
	if strings.HasPrefix(trimmed, "* ") && (strings.Contains(line, "...") || strings.Contains(line, "…")) {
		return true
	}
	// Claude status verbs
	statusVerbs := []string{"Transmuting", "Tinkering", "Thinking", "Analyzing", "Reading", "Writing", "Searching", "Processing"}
	for _, verb := range statusVerbs {
		if strings.Contains(trimmed, verb+"...") || strings.Contains(trimmed, verb+"…") {
			return true
		}
	}
	return false
}

// getNewContent extracts content that appears in current but not in initial
func getNewContent(initial, current string) string {
	if initial == "" {
		return filterStatusBars(current)
	}

	initialLines := strings.Split(initial, "\n")
	currentLines := strings.Split(current, "\n")

	if len(initialLines) == 0 {
		return filterStatusBars(current)
	}

	// Build a set of non-status-bar lines from initial
	initialSet := make(map[string]bool)
	for _, line := range initialLines {
		if !isStatusBarLine(line) {
			initialSet[strings.TrimSpace(line)] = true
		}
	}

	// Find lines in current that are new (not in initial and not status bars)
	var newLines []string
	for _, line := range currentLines {
		if isStatusBarLine(line) {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !initialSet[trimmed] && trimmed != "" {
			newLines = append(newLines, line)
		}
	}

	if len(newLines) == 0 {
		return ""
	}

	return strings.TrimSpace(strings.Join(newLines, "\n"))
}

// filterStatusBars removes status bar lines from content
func filterStatusBars(content string) string {
	lines := strings.Split(content, "\n")
	var filtered []string
	for _, line := range lines {
		if !isStatusBarLine(line) {
			filtered = append(filtered, line)
		}
	}
	return strings.TrimSpace(strings.Join(filtered, "\n"))
}

func listTmuxSessions() ([]string, error) {
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "list-sessions", "-F", "#{session_name}")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var sessions []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		name := scanner.Text()
		if strings.HasPrefix(name, "claude-") {
			sessions = append(sessions, strings.TrimPrefix(name, "claude-"))
		}
	}
	return sessions, nil
}

func sessionName(name string) string {
	return "claude-" + name
}

func getSessionByChannel(config *Config, channelID string) string {
	for name, cid := range config.Sessions {
		if cid == channelID {
			return name
		}
	}
	return ""
}

// Hook handling

func handleHook() error {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hook: no config\n")
		return nil
	}

	var hookData HookData
	decoder := json.NewDecoder(os.Stdin)
	if err := decoder.Decode(&hookData); err != nil {
		fmt.Fprintf(os.Stderr, "hook: decode error: %v\n", err)
		return nil
	}

	fmt.Fprintf(os.Stderr, "hook: cwd=%s transcript=%s\n", hookData.Cwd, hookData.TranscriptPath)

	var sessionName string
	var channelID string
	baseDir := getProjectsDir(config)
	for name, cid := range config.Sessions {
		expectedPath := filepath.Join(baseDir, name)
		if hookData.Cwd == expectedPath || strings.HasSuffix(hookData.Cwd, "/"+name) {
			sessionName = name
			channelID = cid
			break
		}
	}
	if sessionName == "" || channelID == "" {
		fmt.Fprintf(os.Stderr, "hook: no session found for cwd=%s\n", hookData.Cwd)
		return nil
	}

	fmt.Fprintf(os.Stderr, "hook: session=%s channel=%s\n", sessionName, channelID)

	lastMessage := "Session ended"
	if hookData.TranscriptPath != "" {
		if msg := getLastAssistantMessage(hookData.TranscriptPath); msg != "" {
			lastMessage = msg
		}
	}

	fmt.Fprintf(os.Stderr, "hook: sending message to slack\n")
	_, err = sendMessage(config, channelID, fmt.Sprintf(":white_check_mark: *%s*\n\n%s", sessionName, lastMessage))
	return err
}

func handlePermissionHook() error {
	defer func() { recover() }()

	stdinData := make(chan []byte, 1)
	go func() {
		defer func() { recover() }()
		data, _ := io.ReadAll(os.Stdin)
		stdinData <- data
	}()

	var rawData []byte
	select {
	case rawData = <-stdinData:
	case <-time.After(2 * time.Second):
		return nil
	}

	if len(rawData) == 0 {
		return nil
	}

	var hookData HookData
	if err := json.Unmarshal(rawData, &hookData); err != nil {
		return nil
	}

	config, err := loadConfig()
	if err != nil || config == nil {
		return nil
	}

	var sessionName string
	var channelID string
	baseDir := getProjectsDir(config)
	for name, cid := range config.Sessions {
		if name == "" {
			continue
		}
		expectedPath := filepath.Join(baseDir, name)
		if hookData.Cwd == expectedPath || strings.HasSuffix(hookData.Cwd, "/"+name) {
			sessionName = name
			channelID = cid
			break
		}
	}

	if sessionName == "" || channelID == "" {
		return nil
	}

	fmt.Fprintf(os.Stderr, "hook-permission: tool=%s questions=%d\n", hookData.ToolName, len(hookData.ToolInput.Questions))
	if hookData.ToolName == "AskUserQuestion" && len(hookData.ToolInput.Questions) > 0 {
		go func() {
			defer func() { recover() }()
			for qIdx, q := range hookData.ToolInput.Questions {
				if q.Question == "" {
					continue
				}
				msg := fmt.Sprintf(":question: *%s*\n\n%s", q.Header, q.Question)

				var buttons []Element
				for i, opt := range q.Options {
					if opt.Label == "" {
						continue
					}
					// Value format: session:questionIndex:optionIndex
					value := fmt.Sprintf("%s:%d:%d", sessionName, qIdx, i)
					buttons = append(buttons, Element{
						Type:     "button",
						Text:     &TextObject{Type: "plain_text", Text: opt.Label},
						ActionID: fmt.Sprintf("option_%d_%d", qIdx, i),
						Value:    value,
					})
				}

				if len(buttons) > 0 {
					blockID := fmt.Sprintf("question_%s_%d", sessionName, qIdx)
					sendMessageWithButtons(config, channelID, msg, buttons, blockID)
				}
			}
		}()
		return nil
	}

	go func() {
		defer func() { recover() }()
		if hookData.ToolName != "" {
			msg := fmt.Sprintf(":lock: Permission requested: %s", hookData.ToolName)
			sendMessage(config, channelID, msg)
		}
	}()

	return nil
}

func getLastAssistantMessage(transcriptPath string) string {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	var lastMessage string
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var entry map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry["type"] == "assistant" {
			if msg, ok := entry["message"].(map[string]interface{}); ok {
				if content, ok := msg["content"].([]interface{}); ok {
					for _, c := range content {
						if block, ok := c.(map[string]interface{}); ok {
							if block["type"] == "text" {
								if text, ok := block["text"].(string); ok {
									lastMessage = text
								}
							}
						}
					}
				}
			}
		}
	}
	return lastMessage
}

func handlePromptHook() error {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hook-prompt: no config\n")
		return nil
	}

	var hookData HookData
	decoder := json.NewDecoder(os.Stdin)
	if err := decoder.Decode(&hookData); err != nil {
		fmt.Fprintf(os.Stderr, "hook-prompt: decode error: %v\n", err)
		return nil
	}

	if hookData.Prompt == "" {
		fmt.Fprintf(os.Stderr, "hook-prompt: empty prompt\n")
		return nil
	}

	var channelID string
	baseDir := getProjectsDir(config)
	for name, cid := range config.Sessions {
		expectedPath := filepath.Join(baseDir, name)
		if hookData.Cwd == expectedPath || strings.HasSuffix(hookData.Cwd, "/"+name) {
			channelID = cid
			break
		}
	}

	if channelID == "" {
		fmt.Fprintf(os.Stderr, "hook-prompt: no channel found for cwd=%s\n", hookData.Cwd)
		return nil
	}

	prompt := hookData.Prompt
	if len(prompt) > 500 {
		prompt = prompt[:500] + "..."
	}
	fmt.Fprintf(os.Stderr, "hook-prompt: sending to channel %s\n", channelID)
	_, err = sendMessage(config, channelID, fmt.Sprintf(":speech_balloon: %s", prompt))
	return err
}

func handleOutputHook() error {
	config, err := loadConfig()
	if err != nil {
		return nil
	}

	rawData, _ := io.ReadAll(os.Stdin)
	if len(rawData) == 0 {
		return nil
	}

	var hookData HookData
	if err := json.Unmarshal(rawData, &hookData); err != nil {
		return nil
	}

	skipTools := map[string]bool{
		"Read": true, "Glob": true, "Grep": true, "LSP": true,
		"TodoWrite": true, "Task": true, "TaskOutput": true,
	}
	if skipTools[hookData.ToolName] {
		return nil
	}

	var channelID string
	baseDir := getProjectsDir(config)
	for name, cid := range config.Sessions {
		expectedPath := filepath.Join(baseDir, name)
		if hookData.Cwd == expectedPath || strings.HasSuffix(hookData.Cwd, "/"+name) {
			channelID = cid
			break
		}
	}

	if channelID == "" {
		return nil
	}

	if hookData.TranscriptPath != "" {
		if msg := getLastAssistantMessage(hookData.TranscriptPath); msg != "" {
			if len(msg) > 1000 {
				msg = msg[:1000] + "..."
			}
			sendMessage(config, channelID, msg)
		}
	}

	return nil
}

func handleQuestionHook() error {
	config, err := loadConfig()
	if err != nil {
		return nil
	}

	rawData, _ := io.ReadAll(os.Stdin)
	if len(rawData) == 0 {
		return nil
	}

	var hookData HookData
	if err := json.Unmarshal(rawData, &hookData); err != nil {
		return nil
	}

	var sessionName string
	var channelID string
	baseDir := getProjectsDir(config)
	for name, cid := range config.Sessions {
		expectedPath := filepath.Join(baseDir, name)
		if hookData.Cwd == expectedPath || strings.HasSuffix(hookData.Cwd, "/"+name) {
			sessionName = name
			channelID = cid
			break
		}
	}

	if sessionName == "" || channelID == "" {
		return nil
	}

	for qIdx, q := range hookData.ToolInput.Questions {
		if q.Question == "" {
			continue
		}
		msg := fmt.Sprintf(":question: *%s*\n\n%s", q.Header, q.Question)

		var buttons []Element
		for i, opt := range q.Options {
			if opt.Label == "" {
				continue
			}
			value := fmt.Sprintf("%s:%d:%d", sessionName, qIdx, i)
			buttons = append(buttons, Element{
				Type:     "button",
				Text:     &TextObject{Type: "plain_text", Text: opt.Label},
				ActionID: fmt.Sprintf("option_%d_%d", qIdx, i),
				Value:    value,
			})
		}

		if len(buttons) > 0 {
			blockID := fmt.Sprintf("question_%s_%d", sessionName, qIdx)
			sendMessageWithButtons(config, channelID, msg, buttons, blockID)
		} else {
			sendMessage(config, channelID, msg)
		}
	}

	return nil
}

// Install hook in Claude settings

func installHook() error {
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	hookBinPath := filepath.Join(home, "bin", "claude-code-slack-anywhere")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to read settings.json: %w", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("failed to parse settings.json: %w", err)
	}

	stopHook := map[string]interface{}{
		"type":    "command",
		"command": hookBinPath + " hook",
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooks = make(map[string]interface{})
	}
	hooks["Stop"] = []interface{}{stopHook}
	settings["hooks"] = hooks

	newData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsPath, newData, 0600); err != nil {
		return fmt.Errorf("failed to write settings.json: %w", err)
	}

	fmt.Println("Claude hook installed!")
	return nil
}

// Execute shell command

func executeCommand(cmdStr string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", cmdStr)
	cmd.Dir, _ = os.UserHomeDir()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	if output == "" {
		if err != nil {
			output = fmt.Sprintf("Error: %v", err)
		} else {
			output = "(no output)"
		}
	}

	return strings.TrimSpace(output), err
}

// One-shot Claude run

func runClaude(prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	config, _ := loadConfig()
	baseDir := getProjectsDir(config)
	workDir := baseDir

	words := strings.Fields(prompt)
	if len(words) > 0 {
		firstWord := words[0]
		potentialDir := filepath.Join(baseDir, firstWord)
		if info, err := os.Stat(potentialDir); err == nil && info.IsDir() {
			workDir = potentialDir
			prompt = strings.TrimSpace(strings.TrimPrefix(prompt, firstWord))
			if prompt == "" {
				return "Error: no prompt provided after directory name", nil
			}
		}
	}

	if claudePath == "" {
		return "Error: claude binary not found", fmt.Errorf("claude not found")
	}
	cmd := exec.CommandContext(ctx, claudePath, "--dangerously-skip-permissions", "-p", prompt)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	if output == "" {
		if err != nil {
			output = fmt.Sprintf("Error: %v", err)
		} else {
			output = "(no output)"
		}
	}

	return strings.TrimSpace(output), err
}

// Setup

func installService() error {
	home, _ := os.UserHomeDir()

	if _, err := os.Stat("/Library"); err == nil {
		return installLaunchdService(home)
	}
	return installSystemdService(home)
}

func installLaunchdService(home string) error {
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0755); err != nil {
		return fmt.Errorf("failed to create LaunchAgents dir: %w", err)
	}

	plistPath := filepath.Join(plistDir, "com.ccsa.plist")
	logPath := filepath.Join(home, ".ccsa.log")

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.ccsa</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>listen</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, binPath, logPath, logPath)

	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("failed to write plist: %w", err)
	}

	exec.Command("launchctl", "unload", plistPath).Run()
	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		return fmt.Errorf("failed to load service: %w", err)
	}

	fmt.Println("Service installed and started (launchd)")
	return nil
}

func installSystemdService(home string) error {
	serviceDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(serviceDir, 0755); err != nil {
		return fmt.Errorf("failed to create systemd dir: %w", err)
	}

	servicePath := filepath.Join(serviceDir, "ccsa.service")
	service := fmt.Sprintf(`[Unit]
Description=Claude Code Slack Anywhere
After=network.target

[Service]
ExecStart=%s listen
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`, binPath)

	if err := os.WriteFile(servicePath, []byte(service), 0644); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}

	exec.Command("systemctl", "--user", "daemon-reload").Run()
	exec.Command("systemctl", "--user", "enable", "ccsa").Run()
	if err := exec.Command("systemctl", "--user", "start", "ccsa").Run(); err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	fmt.Println("Service installed and started (systemd)")
	return nil
}

func setup(botToken, appToken string) error {
	fmt.Println("Claude Code Slack Anywhere Setup")
	fmt.Println("========================")
	fmt.Println()

	config := &Config{
		BotToken: botToken,
		AppToken: appToken,
		Sessions: make(map[string]string),
	}

	// Step 1: Verify tokens and get bot info
	fmt.Println("Step 1/4: Verifying Slack tokens...")

	// Test bot token
	req, _ := http.NewRequest("GET", "https://slack.com/api/auth.test", nil)
	req.Header.Set("Authorization", "Bearer "+botToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to Slack: %w", err)
	}
	defer resp.Body.Close()

	var authResult struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		UserID string `json:"user_id"`
		User   string `json:"user"`
		BotID  string `json:"bot_id"`
	}
	json.NewDecoder(resp.Body).Decode(&authResult)

	if !authResult.OK {
		return fmt.Errorf("invalid bot token: %s", authResult.Error)
	}
	fmt.Printf("Bot verified: @%s\n\n", authResult.User)

	// Step 2: Get user ID
	fmt.Println("Step 2/4: Send a DM to your bot in Slack...")
	fmt.Println("   Waiting for your message...")

	// We need to use Socket Mode to receive events
	// For setup, we'll use a simpler approach: ask user to input their user ID
	fmt.Print("\nEnter your Slack User ID (find it in your profile > ... > Copy member ID): ")
	reader := bufio.NewReader(os.Stdin)
	userID, _ := reader.ReadString('\n')
	userID = strings.TrimSpace(userID)

	if userID == "" {
		return fmt.Errorf("user ID is required")
	}
	config.UserID = userID

	if err := saveConfig(config); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("User ID saved: %s\n\n", userID)

	// Step 3: Install Claude hook
	fmt.Println("Step 3/4: Installing Claude hook...")
	if err := installHook(); err != nil {
		fmt.Printf("Hook installation failed: %v\n", err)
		fmt.Println("   You can install it later with: claude-code-slack-anywhere install")
	} else {
		fmt.Println()
	}

	// Step 4: Install service
	fmt.Println("Step 4/4: Installing background service...")
	if err := installService(); err != nil {
		fmt.Printf("Service installation failed: %v\n", err)
		fmt.Println("   You can start manually with: claude-code-slack-anywhere listen")
	} else {
		fmt.Println()
	}

	// Done!
	fmt.Println("========================")
	fmt.Println("Setup complete!")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  claude-code-slack-anywhere           Start Claude Code in current directory")
	fmt.Println("  claude-code-slack-anywhere -c        Continue previous session")
	fmt.Println()
	fmt.Println("Slack commands (in any channel with the bot):")
	fmt.Println("  !new <name>   Create new session")
	fmt.Println("  !list         List sessions")
	fmt.Println()
	fmt.Println("Or just message in a project channel to interact with Claude.")

	return nil
}

// Doctor - check all dependencies

func doctor() {
	fmt.Println("claude-code-slack-anywhere doctor")
	fmt.Println("===================================")
	fmt.Println()

	allGood := true

	fmt.Print("tmux.............. ")
	if tmuxPath != "" {
		fmt.Printf("%s\n", tmuxPath)
	} else {
		fmt.Println("not found")
		fmt.Println("   Install: brew install tmux (macOS) or apt install tmux (Linux)")
		allGood = false
	}

	fmt.Print("claude............ ")
	if claudePath != "" {
		fmt.Printf("%s\n", claudePath)
	} else {
		fmt.Println("not found")
		fmt.Println("   Install: npm install -g @anthropic-ai/claude-code")
		allGood = false
	}

	fmt.Print("binary in ~/bin... ")
	home, _ := os.UserHomeDir()
	expectedBinPath := filepath.Join(home, "bin", "claude-code-slack-anywhere")
	if _, err := os.Stat(expectedBinPath); err == nil {
		fmt.Printf("%s\n", expectedBinPath)
	} else {
		fmt.Println("not found")
		fmt.Println("   Run: make install")
		allGood = false
	}

	fmt.Print("config............ ")
	config, err := loadConfig()
	if err != nil {
		fmt.Println("not found")
		fmt.Println("   Run: claude-code-slack-anywhere setup <bot_token> <app_token>")
		allGood = false
	} else {
		fmt.Printf("%s\n", getConfigPath())

		fmt.Print("  bot_token....... ")
		if config.BotToken != "" {
			fmt.Println("configured")
		} else {
			fmt.Println("missing")
			allGood = false
		}

		fmt.Print("  app_token....... ")
		if config.AppToken != "" {
			fmt.Println("configured")
		} else {
			fmt.Println("missing")
			allGood = false
		}

		fmt.Print("  user_id......... ")
		if config.UserID != "" {
			fmt.Printf("%s\n", config.UserID)
		} else {
			fmt.Println("missing")
			allGood = false
		}
	}

	fmt.Print("claude hook....... ")
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var settings map[string]interface{}
		if json.Unmarshal(data, &settings) == nil {
			if hooks, ok := settings["hooks"].(map[string]interface{}); ok {
				if _, hasStop := hooks["Stop"]; hasStop {
					fmt.Println("installed")
				} else {
					fmt.Println("not installed")
					fmt.Println("   Run: claude-code-slack-anywhere install")
					allGood = false
				}
			} else {
				fmt.Println("not installed")
				fmt.Println("   Run: claude-code-slack-anywhere install")
				allGood = false
			}
		} else {
			fmt.Println("settings.json parse error")
		}
	} else {
		fmt.Println("~/.claude/settings.json not found")
	}

	fmt.Print("service........... ")
	if _, err := os.Stat("/Library"); err == nil {
		plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.ccsa.plist")
		if _, err := os.Stat(plistPath); err == nil {
			cmd := exec.Command("launchctl", "list", "com.ccsa")
			if cmd.Run() == nil {
				fmt.Println("running (launchd)")
			} else {
				fmt.Println("installed but not running")
				fmt.Println("   Run: launchctl load ~/Library/LaunchAgents/com.ccsa.plist")
			}
		} else {
			fmt.Println("not installed")
			fmt.Println("   Run: claude-code-slack-anywhere setup <bot_token> <app_token>")
			allGood = false
		}
	} else {
		cmd := exec.Command("systemctl", "--user", "is-active", "ccsa")
		if output, err := cmd.Output(); err == nil && strings.TrimSpace(string(output)) == "active" {
			fmt.Println("running (systemd)")
		} else {
			servicePath := filepath.Join(home, ".config", "systemd", "user", "ccsa.service")
			if _, err := os.Stat(servicePath); err == nil {
				fmt.Println("installed but not running")
				fmt.Println("   Run: systemctl --user start ccsa")
			} else {
				fmt.Println("not installed")
				fmt.Println("   Run: claude-code-slack-anywhere setup <bot_token> <app_token>")
				allGood = false
			}
		}
	}

	fmt.Println()
	if allGood {
		fmt.Println("All checks passed!")
	} else {
		fmt.Println("Some issues found. Fix them and run 'claude-code-slack-anywhere doctor' again.")
	}
}

// Main listen loop using Socket Mode

func logf(format string, args ...interface{}) {
	ts := time.Now().Format("15:04:05")
	fmt.Printf("[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

func listen() error {
	myPid := os.Getpid()
	logf("Starting (PID %d)", myPid)

	cmd := exec.Command("pgrep", "-f", "claude-code-slack-anywhere listen")
	output, _ := cmd.Output()
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if pid, err := strconv.Atoi(line); err == nil && pid != myPid {
			logf("Killing old instance (PID %d)", pid)
			syscall.Kill(pid, syscall.SIGTERM)
		}
	}

	config, err := loadConfig()
	if err != nil {
		return fmt.Errorf("not configured. Run: claude-code-slack-anywhere setup <bot_token> <app_token>")
	}

	logf("Bot listening... (user: %s)", config.UserID)
	logf("Active sessions: %d", len(config.Sessions))
	fmt.Println("Press Ctrl+C to stop")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		logf("Received signal: %v - Shutting down...", sig)
		os.Exit(0)
	}()

	// Session health monitor - check every 30 seconds
	go func() {
		// Track which sessions we've already notified about
		notified := make(map[string]bool)
		for {
			time.Sleep(30 * time.Second)
			cfg, err := loadConfig()
			if err != nil {
				continue
			}
			for sessionName, channelID := range cfg.Sessions {
				tmuxName := tmuxSessionName(sessionName)
				wasAlive := !notified[sessionName]
				isAlive := tmuxSessionExists(tmuxName)

				if wasAlive && !isAlive {
					// Session died - notify
					logf("Session %s died unexpectedly", tmuxName)
					sendMessage(cfg, channelID, ":skull: Session died unexpectedly. Use `!continue "+sessionName+"` to restart.")
					notified[sessionName] = true
				} else if isAlive {
					// Session is alive, reset notification state
					notified[sessionName] = false
				}
			}
		}
	}()

	// Connect via Socket Mode
	for {
		if err := connectSocketMode(config); err != nil {
			fmt.Fprintf(os.Stderr, "Socket Mode error: %v (reconnecting in 5s...)\n", err)
			time.Sleep(5 * time.Second)
		}
	}
}

func connectSocketMode(config *Config) error {
	// Get WebSocket URL
	req, err := http.NewRequest("POST", "https://slack.com/api/apps.connections.open", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+config.AppToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var connResult SlackResponse
	json.NewDecoder(resp.Body).Decode(&connResult)

	if !connResult.OK {
		return fmt.Errorf("failed to open connection: %s", connResult.Error)
	}

	wsURL := connResult.URL
	// Connect WebSocket
	ws, err := websocket.Dial(wsURL, "", "https://slack.com")
	if err != nil {
		return fmt.Errorf("websocket dial failed: %w", err)
	}
	defer ws.Close()

	var wsMutex sync.Mutex

	// Handle messages
	for {
		var envelope SocketModeEnvelope
		if err := websocket.JSON.Receive(ws, &envelope); err != nil {
			return fmt.Errorf("websocket receive failed: %w", err)
		}

		// Acknowledge envelope
		if envelope.EnvelopeID != "" {
			ack := map[string]string{"envelope_id": envelope.EnvelopeID}
			wsMutex.Lock()
			websocket.JSON.Send(ws, ack)
			wsMutex.Unlock()
		}

		switch envelope.Type {
		case "hello":
			logf("Socket Mode connected")

		case "events_api":
			var eventCallback EventCallback
			json.Unmarshal(envelope.Payload, &eventCallback)

			if eventCallback.Type == "event_callback" {
				go handleSlackEvent(config, eventCallback.Event)
			}

		case "interactive":
			var action BlockActionPayload
			json.Unmarshal(envelope.Payload, &action)
			go handleBlockAction(config, action)

		case "disconnect":
			return fmt.Errorf("disconnected by server")
		}
	}
}

func handleSlackEvent(config *Config, eventData json.RawMessage) {
	var event struct {
		Type    string `json:"type"`
		Channel string `json:"channel"`
		User    string `json:"user"`
		Text    string `json:"text"`
		TS      string `json:"ts"`
		BotID   string `json:"bot_id"`
	}
	json.Unmarshal(eventData, &event)

	// Ignore bot messages
	if event.BotID != "" {
		return
	}

	// Only accept from authorized user
	if event.User != config.UserID {
		return
	}

	if event.Type != "message" {
		return
	}

	text := strings.TrimSpace(event.Text)
	if text == "" {
		return
	}

	channelID := event.Channel

	logf("[message] @%s in %s: %s", event.User, channelID, text)

	// Reload config
	config, _ = loadConfig()

	// Handle commands
	if strings.HasPrefix(text, "!ping") {
		sendMessage(config, channelID, "pong!")
		return
	}

	if strings.HasPrefix(text, "!help") {
		helpText := "*Claude Code Slack Anywhere - Commands*\n\n" +
			":rocket: *Session Management*\n" +
			"• `!new <name>` - Create new session with channel\n" +
			"• `!continue [name]` - Continue session (name optional if in session channel)\n" +
			"• `!kill [name]` - Kill a session (name optional if in session channel)\n" +
			"• `!list` - List active sessions\n\n" +
			":computer: *Interaction*\n" +
			"• `!output [lines]` - Capture Claude's screen (default: 100 lines)\n" +
			"• `!c <cmd>` - Execute shell command\n\n" +
			":information_source: *Other*\n" +
			"• `!ping` - Check if bot is alive\n" +
			"• `!help` - Show this help\n\n" +
			":speech_balloon: *In a session channel:*\n" +
			"• Type messages to talk to Claude\n" +
			"• Use `//command` for Claude slash commands (e.g., `//help`, `//compact`)"
		sendMessage(config, channelID, helpText)
		return
	}

	if strings.HasPrefix(text, "!list") {
		sessions, _ := listTmuxSessions()
		if len(sessions) == 0 {
			sendMessage(config, channelID, "No active sessions")
		} else {
			sendMessage(config, channelID, "Sessions:\n"+strings.Join(sessions, "\n"))
		}
		return
	}

	if strings.HasPrefix(text, "!kill") {
		name := strings.TrimSpace(strings.TrimPrefix(text, "!kill"))
		// If no arg provided, try to use the session for this channel
		if name == "" {
			name = getSessionByChannel(config, channelID)
		}
		if name == "" {
			sendMessage(config, channelID, "Usage: `!kill [name]` - name optional if in session channel")
			return
		}
		if _, exists := config.Sessions[name]; !exists {
			sendMessage(config, channelID, fmt.Sprintf("Session '%s' not found", name))
			return
		}
		killTmuxSession(sessionName(name))
		delete(config.Sessions, name)
		saveConfig(config)
		sendMessage(config, channelID, fmt.Sprintf(":wastebasket: Session '%s' killed", name))
		return
	}

	// !output [name] [lines] - capture tmux screen output
	if strings.HasPrefix(text, "!output") {
		args := strings.Fields(strings.TrimPrefix(text, "!output"))

		// Determine session name
		var targetSession string
		lines := 100 // default lines to capture

		if len(args) >= 1 && args[0] != "" {
			// Check if first arg is a number (lines) or session name
			if n, err := strconv.Atoi(args[0]); err == nil {
				lines = n
				// No session name provided, try to use current channel's session
				targetSession = getSessionByChannel(config, channelID)
			} else {
				targetSession = args[0]
				if len(args) >= 2 {
					if n, err := strconv.Atoi(args[1]); err == nil {
						lines = n
					}
				}
			}
		} else {
			// No args, use current channel's session
			targetSession = getSessionByChannel(config, channelID)
		}

		if targetSession == "" {
			sendMessage(config, channelID, ":x: Usage: `!output [session_name] [lines]`\nOr use in a session channel.")
			return
		}

		tmuxName := tmuxSessionName(targetSession)
		if !tmuxSessionExists(tmuxName) {
			sendMessage(config, channelID, fmt.Sprintf(":x: Session '%s' not running", targetSession))
			return
		}

		output, err := captureTmuxOutput(tmuxName, lines)
		if err != nil {
			sendMessage(config, channelID, fmt.Sprintf(":x: Failed to capture output: %v", err))
			return
		}

		if output == "" {
			sendMessage(config, channelID, ":information_source: Screen is empty")
			return
		}

		// Send as code block
		sendMessage(config, channelID, fmt.Sprintf(":computer: *%s* output:\n```\n%s\n```", targetSession, output))
		return
	}

	if strings.HasPrefix(text, "!c ") {
		cmdStr := strings.TrimPrefix(text, "!c ")
		output, err := executeCommand(cmdStr)
		if err != nil {
			output = fmt.Sprintf(":warning: %s\n\nExit: %v", output, err)
		}
		sendMessage(config, channelID, "```\n"+output+"\n```")
		return
	}

	if strings.HasPrefix(text, "!new ") || strings.HasPrefix(text, "!continue") {
		isNew := strings.HasPrefix(text, "!new ")
		var arg string
		if isNew {
			arg = strings.TrimSpace(strings.TrimPrefix(text, "!new "))
		} else {
			arg = strings.TrimSpace(strings.TrimPrefix(text, "!continue"))
		}
		continueSession := !isNew

		// If no arg provided, try to use the session for this channel
		if arg == "" {
			arg = getSessionByChannel(config, channelID)
		}

		if arg == "" {
			sendMessage(config, channelID, "Usage: !new <name> or !continue <name>")
			return
		}

		// Create channel if needed
		var targetChannelID string
		isNewChannel := false
		if cid, exists := config.Sessions[arg]; exists {
			targetChannelID = cid
		} else {
			cid, err := createChannel(config, arg)
			if err != nil {
				sendMessage(config, channelID, fmt.Sprintf(":x: Failed to create channel: %v", err))
				return
			}
			targetChannelID = cid
			config.Sessions[arg] = cid
			saveConfig(config)
			isNewChannel = true
		}

		// Send immediate feedback with channel link
		if isNewChannel {
			sendMessage(config, channelID, fmt.Sprintf(":sparkles: Created <#%s> for `%s`", targetChannelID, arg))
		} else {
			sendMessage(config, channelID, fmt.Sprintf(":arrow_right: Using existing <#%s>", targetChannelID))
		}

		// Find or create work directory
		baseDir := getProjectsDir(config)
		workDir := filepath.Join(baseDir, arg)
		if _, err := os.Stat(workDir); os.IsNotExist(err) {
			// Create the project directory
			if err := os.MkdirAll(workDir, 0755); err != nil {
				sendMessage(config, targetChannelID, fmt.Sprintf(":x: Failed to create directory %s: %v", workDir, err))
				return
			}
			sendMessage(config, targetChannelID, fmt.Sprintf(":file_folder: Created `%s`", workDir))
		} else {
			sendMessage(config, targetChannelID, fmt.Sprintf(":open_file_folder: Using existing `%s`", workDir))
		}

		// Get tmux session name (sanitized)
		tmuxName := tmuxSessionName(arg)
		logf("Creating session: %s -> %s (dir: %s)", arg, tmuxName, workDir)

		// Kill existing if running
		if tmuxSessionExists(tmuxName) {
			logf("Killing existing session: %s", tmuxName)
			killTmuxSession(tmuxName)
			time.Sleep(300 * time.Millisecond)
		}

		if err := createTmuxSession(tmuxName, workDir, continueSession); err != nil {
			logf("Failed to create session: %v", err)
			sendMessage(config, targetChannelID, fmt.Sprintf(":x: Failed to start: %v", err))
			return
		}

		time.Sleep(500 * time.Millisecond)
		if tmuxSessionExists(tmuxName) {
			action := "started"
			if continueSession {
				action = "continued"
			}
			logf("Session %s %s successfully", tmuxName, action)
			sendMessage(config, targetChannelID, fmt.Sprintf(":rocket: Session '%s' %s!\n\nSend messages here to interact with Claude.", arg, action))
		} else {
			logf("Session %s died immediately!", tmuxName)
			sendMessage(config, targetChannelID, ":warning: Session died immediately. Check if ~/bin/claude-code-slack-anywhere works.")
		}
		return
	}

	// Check if message is in a session channel
	sessionName := getSessionByChannel(config, channelID)
	if sessionName != "" {
		tmuxName := tmuxSessionName(sessionName)
		logf("Session found: %s -> tmux: %s", sessionName, tmuxName)
		if tmuxSessionExists(tmuxName) {
			logf("Tmux session exists, adding reaction to user message...")
			// Add reaction to user's message instead of sending separate acknowledgment
			addReaction(config, channelID, event.TS, "eyes")

			// Convert // to / for Claude slash commands (Slack intercepts single /)
			// e.g., "//help" -> "/help", "//compact" -> "/compact"
			claudeText := text
			if strings.HasPrefix(claudeText, "//") {
				claudeText = strings.TrimPrefix(claudeText, "/")
				logf("Converted slash command: %s -> %s", text, claudeText)
			}

			// Add remote context to help Claude understand the user's situation
			remoteText := "[REMOTE via Slack - I cannot see your screen or open files locally. Please show relevant output/content in your responses. IMPORTANT: Do NOT use interactive prompts like AskUserQuestion - I cannot interact with menus. Just proceed with the most reasonable option or ask questions in plain text.] " + claudeText
			if err := sendToTmux(tmuxName, remoteText); err != nil {
				logf("Failed to send to tmux: %v", err)
				addReaction(config, channelID, event.TS, "x")
				sendMessageToThread(config, channelID, event.TS, fmt.Sprintf(":x: Failed to send to Claude: %v", err))
			} else {
				logf("Message sent to tmux successfully")
				// Start streaming output as replies to the user's message
				logf("Starting output stream to thread %s", event.TS)
				go streamOutputToThread(config, channelID, event.TS, tmuxName)
			}
		} else {
			logf("Tmux session does not exist: %s", tmuxName)
			addReaction(config, channelID, event.TS, "warning")
			sendMessageToThread(config, channelID, event.TS, "Session not running. Use `!continue` to restart.")
		}
		return
	}

	// Otherwise, run one-shot Claude
	sendMessage(config, channelID, ":robot_face: Running Claude...")
	go func(p string, cid string) {
		defer func() {
			if r := recover(); r != nil {
				sendMessage(config, cid, fmt.Sprintf(":boom: Panic: %v", r))
			}
		}()
		output, err := runClaude(p)
		if err != nil {
			if strings.Contains(err.Error(), "context deadline exceeded") {
				output = fmt.Sprintf(":stopwatch: Timeout (10min)\n\n%s", output)
			} else {
				output = fmt.Sprintf(":warning: %s\n\nExit: %v", output, err)
			}
		}
		sendMessage(config, cid, output)
	}(text, channelID)
}

func handleBlockAction(config *Config, action BlockActionPayload) {
	// Only accept from authorized user
	if action.User.ID != config.UserID {
		return
	}

	if len(action.Actions) == 0 {
		return
	}

	act := action.Actions[0]

	// Parse value: session:questionIndex:optionIndex
	parts := strings.Split(act.Value, ":")
	if len(parts) != 3 {
		return
	}

	sessionName := parts[0]
	optionIndex, _ := strconv.Atoi(parts[2])

	// Update message to show selection
	originalText := action.Message.Text
	newText := fmt.Sprintf("%s\n\n:white_check_mark: Selected: *%s*", originalText, act.Value)
	updateMessage(config, action.Channel.ID, action.Message.TS, newText)

	tmuxName := tmuxSessionName(sessionName)
	if tmuxSessionExists(tmuxName) {
		// Send arrow down keys to select option, then Enter
		for i := 0; i < optionIndex; i++ {
			exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", tmuxName, "Down").Run()
			time.Sleep(50 * time.Millisecond)
		}
		exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", tmuxName, "Enter").Run()
		fmt.Printf("[action] Selected option %d for %s\n", optionIndex, sessionName)
	}
}

func printHelp() {
	fmt.Printf(`claude-code-slack-anywhere v%s

Control Claude Code remotely via Slack and tmux.

USAGE:
    claude-code-slack-anywhere                Start/attach tmux session in current directory
    claude-code-slack-anywhere -c             Continue previous session
    claude-code-slack-anywhere <message>      Send notification (if away mode is on)

COMMANDS:
    setup <bot> <app>       Complete setup (tokens, hook, service)
    doctor                  Check all dependencies and configuration
    listen                  Start the Slack bot listener manually
    install                 Install Claude hook manually
    run                     Run Claude directly (used by tmux sessions)
    hook                    Handle Claude hook (internal)

SLACK COMMANDS (in any channel):
    !ping                   Check if bot is alive
    !new <name>             Create new session with channel
    !continue <name>        Continue existing session
    !kill <name>            Kill a session
    !list                   List active sessions
    !output [name] [lines]  Capture Claude's screen output (default: 100 lines)
    !c <cmd>                Execute shell command

FLAGS:
    -h, --help              Show this help
    -v, --version           Show version

For more info: https://github.com/sderosiaux/claude-code-slack-anywhere
`, version)
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			printHelp()
			return
		case "-v", "--version", "version":
			fmt.Printf("claude-code-slack-anywhere version %s\n", version)
			return
		}
	}

	if len(os.Args) < 2 {
		if err := startSession(false); err != nil {
			os.Exit(1)
		}
		return
	}

	if os.Args[1] == "-c" {
		if err := startSession(true); err != nil {
			os.Exit(1)
		}
		return
	}

	switch os.Args[1] {
	case "run":
		continueSession := len(os.Args) > 2 && os.Args[2] == "-c"
		if err := runClaudeRaw(continueSession); err != nil {
			os.Exit(1)
		}
		return

	case "setup":
		if len(os.Args) < 4 {
			fmt.Println("Usage: claude-code-slack-anywhere setup <bot_token> <app_token>")
			fmt.Println()
			fmt.Println("Get tokens from your Slack App:")
			fmt.Println("  1. Create app at https://api.slack.com/apps")
			fmt.Println("  2. Enable Socket Mode (get App Token: xapp-...)")
			fmt.Println("  3. Add Bot Token Scopes: channels:manage, channels:history,")
			fmt.Println("     chat:write, users:read")
			fmt.Println("  4. Install to workspace (get Bot Token: xoxb-...)")
			os.Exit(1)
		}
		if err := setup(os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "doctor":
		doctor()

	case "listen":
		if err := listen(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook":
		if err := handleHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook-permission":
		if err := handlePermissionHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook-prompt":
		if err := handlePromptHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook-question":
		if err := handleQuestionHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "hook-output":
		if err := handleOutputHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "install":
		if err := installHook(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	default:
		// Send notification
		config, err := loadConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: not configured. Run: claude-code-slack-anywhere setup <bot_token> <app_token>\n")
			os.Exit(1)
		}

		// Find session channel for current directory
		cwd, _ := os.Getwd()
		baseDir := getProjectsDir(config)
		message := strings.Join(os.Args[1:], " ")

		for name, channelID := range config.Sessions {
			expectedPath := filepath.Join(baseDir, name)
			if cwd == expectedPath || strings.HasSuffix(cwd, "/"+name) {
				if _, err := sendMessage(config, channelID, message); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
				return
			}
		}

		fmt.Println("Not in a session directory, notification not sent.")
	}
}
