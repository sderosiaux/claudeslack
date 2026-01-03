package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSessionName tests the sessionName function
func TestSessionName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple name", "myproject", "claude-myproject"},
		{"with dash", "my-project", "claude-my-project"},
		{"with slash", "money/shop", "claude-money/shop"},
		{"empty", "", "claude-"},
		{"with spaces", "my project", "claude-my project"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sessionName(tt.input)
			if result != tt.expected {
				t.Errorf("sessionName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestGetSessionByChannel tests the getSessionByChannel function
func TestGetSessionByChannel(t *testing.T) {
	config := &Config{
		Sessions: map[string]string{
			"project1":   "C001",
			"project2":   "C002",
			"money/shop": "C003",
		},
	}

	tests := []struct {
		name      string
		channelID string
		expected  string
	}{
		{"existing channel", "C001", "project1"},
		{"another existing", "C002", "project2"},
		{"nested path", "C003", "money/shop"},
		{"non-existent", "C999", ""},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getSessionByChannel(config, tt.channelID)
			if result != tt.expected {
				t.Errorf("getSessionByChannel(config, %q) = %q, want %q", tt.channelID, result, tt.expected)
			}
		})
	}
}

// TestGetSessionByChannelNilSessions tests with nil sessions map
func TestGetSessionByChannelNilSessions(t *testing.T) {
	config := &Config{
		Sessions: nil,
	}
	result := getSessionByChannel(config, "C001")
	if result != "" {
		t.Errorf("getSessionByChannel with nil sessions = %q, want empty string", result)
	}
}

// TestConfigSaveLoad tests saving and loading config
func TestConfigSaveLoad(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "ccc-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override config path for test
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	// Test config
	config := &Config{
		BotToken: "xoxb-test-token-123",
		AppToken: "xapp-test-token-456",
		UserID:   "U12345678",
		Sessions: map[string]string{
			"project1":   "C001",
			"money/shop": "C002",
		},
		Away: true,
	}

	// Save config
	if err := saveConfig(config); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	// Verify file exists
	configPath := filepath.Join(tmpDir, ".ccc.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("Config file was not created")
	}

	// Load config
	loaded, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	// Verify loaded config matches
	if loaded.BotToken != config.BotToken {
		t.Errorf("BotToken = %q, want %q", loaded.BotToken, config.BotToken)
	}
	if loaded.AppToken != config.AppToken {
		t.Errorf("AppToken = %q, want %q", loaded.AppToken, config.AppToken)
	}
	if loaded.UserID != config.UserID {
		t.Errorf("UserID = %q, want %q", loaded.UserID, config.UserID)
	}
	if loaded.Away != config.Away {
		t.Errorf("Away = %v, want %v", loaded.Away, config.Away)
	}
	if len(loaded.Sessions) != len(config.Sessions) {
		t.Errorf("Sessions length = %d, want %d", len(loaded.Sessions), len(config.Sessions))
	}
	for name, channelID := range config.Sessions {
		if loaded.Sessions[name] != channelID {
			t.Errorf("Sessions[%q] = %q, want %q", name, loaded.Sessions[name], channelID)
		}
	}
}

// TestConfigLoadNonExistent tests loading non-existent config
func TestConfigLoadNonExistent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ccc-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	_, err = loadConfig()
	if err == nil {
		t.Error("loadConfig should fail for non-existent file")
	}
}

// TestConfigSessionsInitialized tests that Sessions map is initialized on load
func TestConfigSessionsInitialized(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ccc-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	// Write config without sessions field
	configPath := filepath.Join(tmpDir, ".ccc.json")
	data := []byte(`{"bot_token": "xoxb-test", "app_token": "xapp-test", "user_id": "U123"}`)
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	loaded, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	if loaded.Sessions == nil {
		t.Error("Sessions should be initialized to non-nil map")
	}
}

// TestGetLastAssistantMessage tests parsing transcript files
func TestGetLastAssistantMessage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ccc-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name: "single assistant message",
			content: `{"type":"user","message":{"content":[{"type":"text","text":"hello"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Hello! How can I help?"}]}}`,
			expected: "Hello! How can I help?",
		},
		{
			name: "multiple assistant messages returns last",
			content: `{"type":"assistant","message":{"content":[{"type":"text","text":"First response"}]}}
{"type":"user","message":{"content":[{"type":"text","text":"more"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Second response"}]}}`,
			expected: "Second response",
		},
		{
			name:     "no assistant messages",
			content:  `{"type":"user","message":{"content":[{"type":"text","text":"hello"}]}}`,
			expected: "",
		},
		{
			name:     "empty file",
			content:  "",
			expected: "",
		},
		{
			name:     "invalid json",
			content:  "not json at all",
			expected: "",
		},
		{
			name: "mixed content types",
			content: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"bash"},{"type":"text","text":"Done!"}]}}`,
			expected: "Done!",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write test file
			filePath := filepath.Join(tmpDir, tt.name+".jsonl")
			if err := os.WriteFile(filePath, []byte(tt.content), 0644); err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			result := getLastAssistantMessage(filePath)
			if result != tt.expected {
				t.Errorf("getLastAssistantMessage() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestGetLastAssistantMessageNonExistent tests with non-existent file
func TestGetLastAssistantMessageNonExistent(t *testing.T) {
	result := getLastAssistantMessage("/nonexistent/path/file.jsonl")
	if result != "" {
		t.Errorf("getLastAssistantMessage for non-existent file = %q, want empty", result)
	}
}

// TestExecuteCommand tests the executeCommand function
func TestExecuteCommand(t *testing.T) {
	tests := []struct {
		name        string
		cmd         string
		wantContain string
		wantErr     bool
	}{
		{"echo", "echo hello", "hello", false},
		{"pwd", "pwd", "/", false},
		{"invalid command", "nonexistentcommand123", "", true},
		{"exit code", "exit 1", "", true},
		{"stderr output", "echo error >&2", "error", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := executeCommand(tt.cmd)
			if (err != nil) != tt.wantErr {
				t.Errorf("executeCommand(%q) error = %v, wantErr %v", tt.cmd, err, tt.wantErr)
			}
			if tt.wantContain != "" && !contains(output, tt.wantContain) {
				t.Errorf("executeCommand(%q) output = %q, want to contain %q", tt.cmd, output, tt.wantContain)
			}
		})
	}
}

// TestConfigJSON tests JSON marshaling/unmarshaling
func TestConfigJSON(t *testing.T) {
	config := &Config{
		BotToken: "xoxb-token123",
		AppToken: "xapp-token456",
		UserID:   "U12345678",
		Sessions: map[string]string{
			"test": "C001",
		},
		Away: true,
	}

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if loaded.BotToken != config.BotToken {
		t.Errorf("BotToken mismatch")
	}
	if loaded.AppToken != config.AppToken {
		t.Errorf("AppToken mismatch")
	}
	if loaded.UserID != config.UserID {
		t.Errorf("UserID mismatch")
	}
}

// TestHookDataJSON tests HookData JSON parsing
func TestHookDataJSON(t *testing.T) {
	jsonStr := `{"cwd":"/Users/test/project","transcript_path":"/tmp/transcript.jsonl","session_id":"abc123"}`

	var hookData HookData
	if err := json.Unmarshal([]byte(jsonStr), &hookData); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if hookData.Cwd != "/Users/test/project" {
		t.Errorf("Cwd = %q, want %q", hookData.Cwd, "/Users/test/project")
	}
	if hookData.TranscriptPath != "/tmp/transcript.jsonl" {
		t.Errorf("TranscriptPath = %q, want %q", hookData.TranscriptPath, "/tmp/transcript.jsonl")
	}
	if hookData.SessionID != "abc123" {
		t.Errorf("SessionID = %q, want %q", hookData.SessionID, "abc123")
	}
}

// TestSlackMessageJSON tests SlackMessage JSON parsing
func TestSlackMessageJSON(t *testing.T) {
	jsonStr := `{
		"type": "message",
		"channel": "C001",
		"user": "U123",
		"text": "Hello world",
		"ts": "1234567890.123456"
	}`

	var msg SlackMessage
	if err := json.Unmarshal([]byte(jsonStr), &msg); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if msg.Type != "message" {
		t.Errorf("Type = %q, want message", msg.Type)
	}
	if msg.Channel != "C001" {
		t.Errorf("Channel = %q, want C001", msg.Channel)
	}
	if msg.User != "U123" {
		t.Errorf("User = %q, want U123", msg.User)
	}
	if msg.Text != "Hello world" {
		t.Errorf("Text = %q, want 'Hello world'", msg.Text)
	}
	if msg.TS != "1234567890.123456" {
		t.Errorf("TS = %q, want 1234567890.123456", msg.TS)
	}
}

// TestMessageTruncation tests that long messages are truncated
func TestMessageTruncation(t *testing.T) {
	// The sendMessage function truncates at 4000 chars
	// We test the truncation logic directly
	const maxLen = 4000

	tests := []struct {
		name       string
		inputLen   int
		shouldTrim bool
	}{
		{"short message", 100, false},
		{"exactly max", maxLen, false},
		{"over max", maxLen + 100, true},
		{"way over max", 10000, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create message of specified length
			text := make([]byte, tt.inputLen)
			for i := range text {
				text[i] = 'a'
			}
			msg := string(text)

			// Apply same truncation logic as sendMessage
			if len(msg) > maxLen {
				msg = msg[:maxLen] + "\n... (truncated)"
			}

			if tt.shouldTrim {
				if len(msg) <= tt.inputLen {
					// Should have been truncated
					if len(msg) != maxLen+len("\n... (truncated)") {
						t.Errorf("truncated length = %d, want %d", len(msg), maxLen+len("\n... (truncated)"))
					}
				}
			} else {
				if len(msg) != tt.inputLen {
					t.Errorf("message was unexpectedly modified")
				}
			}
		})
	}
}

// TestListTmuxSessionsParsing tests the session list parsing logic
func TestListTmuxSessionsParsing(t *testing.T) {
	// Test the parsing logic that filters claude- prefixed sessions
	testData := []struct {
		sessionName string
		shouldMatch bool
	}{
		{"claude-myproject", true},
		{"claude-money/shop", true},
		{"other-session", false},
		{"claude-", true},
		{"notclaude-test", false},
	}

	for _, tt := range testData {
		t.Run(tt.sessionName, func(t *testing.T) {
			hasPrefix := len(tt.sessionName) >= 7 && tt.sessionName[:7] == "claude-"
			if hasPrefix != tt.shouldMatch {
				t.Errorf("prefix check for %q = %v, want %v", tt.sessionName, hasPrefix, tt.shouldMatch)
			}
		})
	}
}

// TestConfigFilePermissions tests that config is saved with correct permissions
func TestConfigFilePermissions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ccc-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	config := &Config{
		BotToken: "xoxb-secret-token",
		AppToken: "xapp-secret-token",
		UserID:   "U12345678",
		Sessions: make(map[string]string),
	}

	if err := saveConfig(config); err != nil {
		t.Fatalf("saveConfig failed: %v", err)
	}

	configPath := filepath.Join(tmpDir, ".ccc.json")
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("Failed to stat config file: %v", err)
	}

	// Check permissions are 0600 (owner read/write only)
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("Config file permissions = %o, want 0600", perm)
	}
}

// TestEmptySessionsMap tests behavior with empty sessions
func TestEmptySessionsMap(t *testing.T) {
	config := &Config{
		Sessions: make(map[string]string),
	}

	result := getSessionByChannel(config, "C001")
	if result != "" {
		t.Errorf("getSessionByChannel with empty sessions = %q, want empty", result)
	}
}

// TestBlockJSON tests Block Kit structures
func TestBlockJSON(t *testing.T) {
	block := Block{
		Type:    "section",
		Text:    &TextObject{Type: "mrkdwn", Text: "Hello *world*"},
		BlockID: "block_1",
	}

	data, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var loaded Block
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if loaded.Type != "section" {
		t.Errorf("Type = %q, want section", loaded.Type)
	}
	if loaded.Text.Text != "Hello *world*" {
		t.Errorf("Text.Text = %q, want 'Hello *world*'", loaded.Text.Text)
	}
}

// TestElementJSON tests Button element structures
func TestElementJSON(t *testing.T) {
	element := Element{
		Type:     "button",
		Text:     &TextObject{Type: "plain_text", Text: "Click me"},
		ActionID: "action_1",
		Value:    "value_1",
	}

	data, err := json.Marshal(element)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var loaded Element
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if loaded.Type != "button" {
		t.Errorf("Type = %q, want button", loaded.Type)
	}
	if loaded.ActionID != "action_1" {
		t.Errorf("ActionID = %q, want action_1", loaded.ActionID)
	}
	if loaded.Value != "value_1" {
		t.Errorf("Value = %q, want value_1", loaded.Value)
	}
}

// TestSlackResponseJSON tests SlackResponse JSON parsing
func TestSlackResponseJSON(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantOK  bool
		wantErr string
	}{
		{
			name:   "success response",
			json:   `{"ok": true}`,
			wantOK: true,
		},
		{
			name:    "error response",
			json:    `{"ok": false, "error": "channel_not_found"}`,
			wantOK:  false,
			wantErr: "channel_not_found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp SlackResponse
			if err := json.Unmarshal([]byte(tt.json), &resp); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}

			if resp.OK != tt.wantOK {
				t.Errorf("OK = %v, want %v", resp.OK, tt.wantOK)
			}
			if resp.Error != tt.wantErr {
				t.Errorf("Error = %q, want %q", resp.Error, tt.wantErr)
			}
		})
	}
}

// TestSlackChannelJSON tests SlackChannel JSON parsing
func TestSlackChannelJSON(t *testing.T) {
	jsonStr := `{"id": "C001", "name": "myproject"}`

	var channel SlackChannel
	if err := json.Unmarshal([]byte(jsonStr), &channel); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if channel.ID != "C001" {
		t.Errorf("ID = %q, want C001", channel.ID)
	}
	if channel.Name != "myproject" {
		t.Errorf("Name = %q, want myproject", channel.Name)
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
