package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
	defer func() {
		if r := recover(); r != nil {
			logf("PANIC in handlePermissionHook: %v", r)
		}
	}()

	stdinData := make(chan []byte, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logf("PANIC reading stdin in hook: %v", r)
			}
		}()
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
			defer func() {
				if r := recover(); r != nil {
					logf("PANIC in AskUserQuestion handler: %v", r)
				}
			}()
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
		defer func() {
			if r := recover(); r != nil {
				logf("PANIC in permission notification: %v", r)
			}
		}()
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
