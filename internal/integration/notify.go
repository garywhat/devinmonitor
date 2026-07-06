package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// ---- Webhook Notifications (#85) ----

// webhookType auto-detects the webhook provider from the URL.
func webhookType(url string) string {
	switch {
	case strings.Contains(url, "discord.com") || strings.Contains(url, "discordapp.com"):
		return "discord"
	case strings.Contains(url, "hooks.slack.com"):
		return "slack"
	case strings.Contains(url, "api.telegram.org"):
		return "telegram"
	default:
		return "generic"
	}
}

// sendWebhook sends a notification to the configured webhook URL.
// The payload format is auto-adapted for Discord, Slack, and Telegram.
func sendWebhook(url string, n model.Notification) error {
	if url == "" {
		return fmt.Errorf("no webhook URL configured")
	}
	wt := webhookType(url)
	var payload interface{}

	switch wt {
	case "discord":
		payload = map[string]interface{}{
			"embeds": []map[string]interface{}{
				{
					"title":       n.Title,
					"description": n.Body,
					"color":       discordColor(n.Level),
				},
			},
		}
	case "slack":
		payload = map[string]interface{}{
			"attachments": []map[string]interface{}{
				{
					"fallback": n.Title + ": " + n.Body,
					"title":    n.Title,
					"text":     n.Body,
					"color":    slackColor(n.Level),
				},
			},
		}
	case "telegram":
		text := fmt.Sprintf("*%s*\n%s", n.Title, n.Body)
		// Extract chat_id from URL path: /bot<token>/sendMessage?chat_id=...
		payload = map[string]interface{}{
			"text":      text,
			"parse_mode": "Markdown",
		}
		// If URL already has chat_id, use as-is; otherwise we need to add it.
		if !strings.Contains(url, "chat_id") {
			return fmt.Errorf("telegram webhook URL must include chat_id parameter")
		}
	default:
		payload = map[string]interface{}{
			"title": n.Title,
			"body":  n.Body,
			"level": n.Level,
		}
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}

func discordColor(level string) int {
	switch level {
	case "critical":
		return 0xFF0000
	case "warning":
		return 0xFFA500
	default:
		return 0x00FF00
	}
}

func slackColor(level string) string {
	switch level {
	case "critical":
		return "danger"
	case "warning":
		return "warning"
	default:
		return "good"
	}
}

// ---- Desktop Notifications (#86) ----

// sendDesktopNotification sends an OS-native notification.
func sendDesktopNotification(n model.Notification) error {
	switch runtime.GOOS {
	case "windows":
		// Use Windows Toast Notification via WinRT (goes to Action Center).
		// Requires Windows 10+ and PowerShell 5.0+.
		psScript := fmt.Sprintf(`
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom, ContentType = WindowsRuntime] | Out-Null
$template = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$textNodes = $template.GetElementsByTagName("text")
$textNodes.Item(0).AppendChild($template.CreateTextNode("%s")) | Out-Null
$textNodes.Item(1).AppendChild($template.CreateTextNode("%s")) | Out-Null
$notifier = [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier("DevinMonitor")
$toast = [Windows.UI.Notifications.ToastNotification]::new($template)
$notifier.Show($toast)
`, escapeXML(n.Title), escapeXML(n.Body))
		cmd := exec.Command("powershell", "-NoProfile", "-Command", psScript)
		return cmd.Run()
	case "darwin":
		script := fmt.Sprintf(`display notification %q with title %q`, n.Body, n.Title)
		cmd := exec.Command("osascript", "-e", script)
		return cmd.Run()
	default: // linux, freebsd, etc.
		cmd := exec.Command("notify-send", n.Title, n.Body)
		if err := cmd.Run(); err != nil {
			// notify-send not installed; try with explicit icon.
			cmd2 := exec.Command("notify-send", "--icon=dialog-information", n.Title, n.Body)
			return cmd2.Run()
		}
		return nil
	}
}

// escapeXML escapes XML special characters for safe embedding in toast XML.
func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// ---- Notify Command ----

var cmdNotify = func() *cobra.Command {
	var test bool
	c := &cobra.Command{
		Use:   "notify",
		Short: i18n.T("cmd.notify"),
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Global()

			if test {
				n := model.Notification{
					Title: "DevinMonitor",
					Body:  "Test notification — notifications are working!",
					Level: "info",
				}
				if cfg.NotifyDesktop {
					if err := sendDesktopNotification(n); err != nil {
						fmt.Fprintf(os.Stderr, "desktop notification failed: %v\n", err)
					} else {
						fmt.Println("Desktop notification sent.")
					}
				} else {
					fmt.Println("Desktop notifications disabled. Enable with: config set notifyDesktop true")
				}
				if cfg.NotifyWebhook != "" {
					if err := sendWebhook(cfg.NotifyWebhook, n); err != nil {
						fmt.Fprintf(os.Stderr, "webhook notification failed: %v\n", err)
					} else {
						fmt.Printf("Webhook notification sent (%s).\n", webhookType(cfg.NotifyWebhook))
					}
				} else {
					fmt.Println("No webhook configured. Set with: config set notifyWebhook <url>")
				}
				return
			}

			// Without --test: check for real alerts and send notifications.
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			alerts := detectAlerts(ss)
			if len(alerts) == 0 {
				fmt.Println("No alerts to notify.")
				return
			}
			sent := 0
			for _, a := range alerts {
				n := model.Notification{
					Title:   fmt.Sprintf("DevinMonitor Alert: %s", a.Kind),
					Body:    a.Message,
					Level:   a.Severity,
				}
				if cfg.NotifyDesktop {
					_ = sendDesktopNotification(n)
				}
				if cfg.NotifyWebhook != "" {
					_ = sendWebhook(cfg.NotifyWebhook, n)
				}
				sent++
			}
			fmt.Printf("Sent %d notification(s).\n", sent)
		},
	}
	c.Flags().BoolVar(&test, "test", false, "send a test notification")
	return c
}

// checkAndNotify is a helper that checks for budget/session alerts and sends
// notifications if configured. Used by the web dashboard poller.
func checkAndNotify(ss []model.Session) {
	cfg := config.Global()
	alerts := detectAlerts(ss)
	if len(alerts) == 0 {
		return
	}
	for _, a := range alerts {
		n := model.Notification{Title: a.Kind, Body: a.Message, Level: a.Severity}
		if cfg.NotifyDesktop {
			_ = sendDesktopNotification(n)
		}
		if cfg.NotifyWebhook != "" {
			_ = sendWebhook(cfg.NotifyWebhook, n)
		}
	}
}

// triggerSessionCompleteNotify sends a notification when a session completes.
func triggerSessionCompleteNotify(s *model.Session) {
	cfg := config.Global()
	cost, _ := report.SessionCost(s)
	n := model.Notification{
		Title: "Session Complete",
		Body:  fmt.Sprintf("Session %s completed. Cost: $%.2f", s.ID, cost),
		Level: "info",
	}
	if cfg.NotifyDesktop {
		_ = sendDesktopNotification(n)
	}
	if cfg.NotifyWebhook != "" {
		_ = sendWebhook(cfg.NotifyWebhook, n)
	}
}

// Ensure time is used (for potential future timestamp logic).
var _ = time.Now
