package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Setup and installation functions

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
	resp, err := httpClient.Do(req)
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
