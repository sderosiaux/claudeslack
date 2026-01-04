package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

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

// ClaudeResponse represents the JSON response from claude -p --output-format json
type ClaudeResponse struct {
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	Usage     struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	DurationMs int  `json:"duration_ms"`
	IsError    bool `json:"is_error"`
}

// claudeSessionIDs stores Claude session IDs by Slack channel ID
// This allows conversation continuity across messages
var claudeSessionIDs sync.Map // channelID (string) -> sessionID (string)

// Paths for binaries
var (
	binPath    string
	claudePath string
)

func init() {
	// Find our own binary path
	if exe, err := os.Executable(); err == nil {
		binPath = exe
	}

	// Find claude binary
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

// callClaudeJSON calls Claude in headless mode with JSON output
// It automatically resumes the session if one exists for the channel
func callClaudeJSON(prompt string, channelID string, workDir string) (*ClaudeResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if claudePath == "" {
		return nil, fmt.Errorf("claude binary not found")
	}

	args := []string{
		"-p", prompt,
		"--dangerously-skip-permissions",
		"--output-format", "json",
	}

	// Resume session if one exists for this channel
	if sid, ok := claudeSessionIDs.Load(channelID); ok {
		args = append(args, "--resume", sid.(string))
	}

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// Check if stderr has useful info
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("claude error: %w - %s", err, stderr.String())
		}
		return nil, fmt.Errorf("claude error: %w", err)
	}

	var resp ClaudeResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		// If JSON parsing fails, return raw output as result
		return &ClaudeResponse{
			Result:  stdout.String(),
			IsError: true,
		}, fmt.Errorf("JSON parse error: %w - raw: %s", err, stdout.String())
	}

	// Store session ID for future messages
	if resp.SessionID != "" {
		claudeSessionIDs.Store(channelID, resp.SessionID)
	}

	return &resp, nil
}

// StreamEvent represents a JSON event from Claude's stream-json output
type StreamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		} `json:"content"`
	} `json:"message,omitempty"`
	Result    string `json:"result,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	Usage     struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
	DurationMs int `json:"duration_ms,omitempty"`
}

// SlackUpdater handles batched updates to a Slack message
type SlackUpdater struct {
	config       *Config
	channelID    string
	threadTS     string
	messageTS    string
	content      strings.Builder
	lastUpdate   time.Time
	updateCount  int
	mu           sync.Mutex
}

// Update adds content and triggers Slack update if needed
func (su *SlackUpdater) Update(text string) {
	su.mu.Lock()
	defer su.mu.Unlock()

	su.content.WriteString(text)

	// Micro-batch: update every 500ms or 300 chars
	// Also update on first content to show immediate progress
	sinceLastUpdate := time.Since(su.lastUpdate)
	contentLen := su.content.Len()

	isFirstUpdate := su.updateCount == 0
	shouldUpdate := isFirstUpdate ||
		sinceLastUpdate >= 500*time.Millisecond ||
		(contentLen >= 300 && sinceLastUpdate >= 200*time.Millisecond)

	if shouldUpdate && contentLen > 0 {
		su.flush()
	}
}

// flush sends current content to Slack
func (su *SlackUpdater) flush() {
	content := su.content.String()
	if content == "" {
		return
	}

	// Convert markdown for display
	displayContent := markdownToSlack(content) + "\n\n_streaming..._"

	if su.messageTS == "" {
		// First message - post new
		ts, err := sendMessageToThreadGetTS(su.config, su.channelID, su.threadTS, displayContent)
		if err == nil && ts != "" {
			su.messageTS = ts
		}
	} else {
		// Update existing message
		updateMessage(su.config, su.channelID, su.messageTS, displayContent)
	}

	su.lastUpdate = time.Now()
	su.updateCount++
}

// Finalize sends the final content with stats
func (su *SlackUpdater) Finalize(resp *ClaudeResponse) {
	su.mu.Lock()
	defer su.mu.Unlock()

	content := resp.Result
	if content == "" {
		content = su.content.String()
	}
	if content == "" {
		content = "(no response)"
	}

	// Convert markdown and add footer
	displayContent := markdownToSlack(content)
	footer := fmt.Sprintf("\n\n_tokens: %d in / %d out | %dms_",
		resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.DurationMs)

	finalContent := displayContent + footer

	if su.messageTS == "" {
		sendMessageToThread(su.config, su.channelID, su.threadTS, finalContent)
	} else {
		updateMessage(su.config, su.channelID, su.messageTS, finalContent)
	}
}

// GetContent returns accumulated content
func (su *SlackUpdater) GetContent() string {
	su.mu.Lock()
	defer su.mu.Unlock()
	return su.content.String()
}

// callClaudeStreaming calls Claude with streaming output and updates Slack in real-time
func callClaudeStreaming(prompt string, channelID string, threadTS string, workDir string, config *Config) (*ClaudeResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if claudePath == "" {
		return nil, fmt.Errorf("claude binary not found")
	}

	args := []string{
		"-p", prompt,
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
	}

	// Resume session if one exists for this channel
	if sid, ok := claudeSessionIDs.Load(channelID); ok {
		args = append(args, "--resume", sid.(string))
	}

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start claude: %w", err)
	}

	// Create Slack updater for batched updates
	updater := &SlackUpdater{
		config:     config,
		channelID:  channelID,
		threadTS:   threadTS,
		lastUpdate: time.Now(),
	}

	// Post initial "thinking" message immediately
	ts, _ := sendMessageToThreadGetTS(config, channelID, threadTS, ":hourglass_flowing_sand: _Claude is thinking..._")
	if ts != "" {
		updater.messageTS = ts
	}

	var finalResponse ClaudeResponse
	scanner := bufio.NewScanner(stdout)
	// Increase buffer for long lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event StreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue // Skip malformed lines
		}

		switch event.Type {
		case "assistant":
			// Extract text from content array
			for _, content := range event.Message.Content {
				if content.Type == "text" && content.Text != "" {
					updater.Update(content.Text)
				}
			}
			if event.SessionID != "" {
				finalResponse.SessionID = event.SessionID
				claudeSessionIDs.Store(channelID, event.SessionID)
			}

		case "result":
			// Final result with stats
			finalResponse.Result = event.Result
			finalResponse.SessionID = event.SessionID
			finalResponse.IsError = event.IsError
			finalResponse.Usage.InputTokens = event.Usage.InputTokens
			finalResponse.Usage.OutputTokens = event.Usage.OutputTokens
			finalResponse.DurationMs = event.DurationMs

			if event.SessionID != "" {
				claudeSessionIDs.Store(channelID, event.SessionID)
			}
		}
	}

	cmd.Wait()

	// Use accumulated content if result is empty
	if finalResponse.Result == "" {
		finalResponse.Result = updater.GetContent()
	}

	// Send final update with stats
	updater.Finalize(&finalResponse)

	return &finalResponse, nil
}

// resetClaudeSession removes the stored session ID for a channel
// Next message will start a fresh conversation
func resetClaudeSession(channelID string) {
	claudeSessionIDs.Delete(channelID)
}

// getClaudeSessionID returns the current session ID for a channel, if any
func getClaudeSessionID(channelID string) (string, bool) {
	if sid, ok := claudeSessionIDs.Load(channelID); ok {
		return sid.(string), true
	}
	return "", false
}

// markdownToSlack converts GitHub-flavored markdown to Slack mrkdwn
func markdownToSlack(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	inCodeBlock := false

	for _, line := range lines {
		// Track code blocks (don't modify content inside)
		if strings.HasPrefix(line, "```") {
			inCodeBlock = !inCodeBlock
			result = append(result, line)
			continue
		}
		if inCodeBlock {
			result = append(result, line)
			continue
		}

		// Convert markdown headers (## Header -> *Header*)
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// Count # symbols and extract header text
			headerText := strings.TrimLeft(trimmed, "#")
			headerText = strings.TrimSpace(headerText)
			if headerText != "" {
				result = append(result, "*"+headerText+"*")
				continue
			}
		}

		// Detect and convert markdown tables
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
			// Skip separator lines (|---|---|)
			if strings.Contains(trimmed, "---") {
				continue
			}
			// Convert table row to simple format
			cells := strings.Split(trimmed, "|")
			var cleanCells []string
			for _, cell := range cells {
				cell = strings.TrimSpace(cell)
				if cell != "" {
					cleanCells = append(cleanCells, cell)
				}
			}
			if len(cleanCells) >= 2 {
				// Format as "key: value" for 2-column tables
				result = append(result, fmt.Sprintf("*%s*: %s", cleanCells[0], strings.Join(cleanCells[1:], " | ")))
			} else if len(cleanCells) == 1 {
				result = append(result, cleanCells[0])
			}
			continue
		}

		// Convert **bold** to *bold* (Slack style)
		// But preserve single * (already Slack bold)
		for strings.Contains(line, "**") {
			start := strings.Index(line, "**")
			end := strings.Index(line[start+2:], "**")
			if end == -1 {
				break
			}
			end += start + 2
			content := line[start+2 : end]
			line = line[:start] + "*" + content + "*" + line[end+2:]
		}

		result = append(result, line)
	}

	return strings.Join(result, "\n")
}

// sendClaudeResponse formats and sends a Claude response to Slack
// It handles long messages by splitting them into chunks
func sendClaudeResponse(config *Config, channelID, threadTS string, resp *ClaudeResponse) {
	result := resp.Result
	if result == "" {
		result = "(no response)"
	}

	// Convert markdown to Slack format
	result = markdownToSlack(result)

	// Footer with usage stats
	footer := fmt.Sprintf("\n\n_tokens: %d in / %d out | %dms_",
		resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.DurationMs)

	// Slack message limit is ~4000 chars, use 3500 to be safe
	const maxLen = 3500

	if len(result)+len(footer) < maxLen {
		sendMessageToThread(config, channelID, threadTS, result+footer)
		return
	}

	// Split into chunks
	chunks := splitMessageIntoChunks(result, maxLen)
	for i, chunk := range chunks {
		msg := chunk
		if i == len(chunks)-1 {
			msg += footer
		}
		sendMessageToThread(config, channelID, threadTS, msg)
	}
}

// splitMessageIntoChunks splits a message into chunks of maxLen
// It tries to split on newlines or spaces when possible
func splitMessageIntoChunks(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	remaining := text

	for len(remaining) > 0 {
		if len(remaining) <= maxLen {
			chunks = append(chunks, remaining)
			break
		}

		// Find a good break point
		breakPoint := maxLen

		// Try to find a newline
		if idx := strings.LastIndex(remaining[:maxLen], "\n"); idx > maxLen/2 {
			breakPoint = idx + 1
		} else if idx := strings.LastIndex(remaining[:maxLen], " "); idx > maxLen/2 {
			// Try to find a space
			breakPoint = idx + 1
		}

		chunks = append(chunks, remaining[:breakPoint])
		remaining = remaining[breakPoint:]
	}

	return chunks
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
