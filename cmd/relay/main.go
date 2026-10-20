// Command relay forwards verification codes and links from IMAP mailboxes to Telegram.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const usage = `用法：relay [--config 路徑] <指令> [參數]

指令：
  run                         啟動服務
  config validate             驗證設定檔
  login <信箱>                Microsoft 信箱的 OAuth 授權（device code）
  test --eml <檔案> [--mailbox <信箱>] [--send <目標>]
                              對 .eml 檔執行辨識並顯示判斷過程與訊息
  send-test <目標>            發送測試訊息到指定目標

設定檔預設為 $RELAY_CONFIG 或 ./config.yaml。
`

func main() { os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr)) }

func realMain(args []string, stdout, stderr io.Writer) int {
	configPath := os.Getenv("RELAY_CONFIG")
	if configPath == "" {
		configPath = "config.yaml"
	}
	configPath, args = globalConfig(configPath, args)
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "run":
		return cmdRun(configPath, rest, stdout, stderr)
	case "config":
		if len(rest) == 0 || rest[0] != "validate" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		return cmdValidate(configPath, rest[1:], stdout, stderr)
	case "login":
		return cmdLogin(configPath, rest, stdout, stderr)
	case "test":
		return cmdTest(configPath, rest, stdout, stderr)
	case "send-test":
		return cmdSendTest(configPath, rest, stdout, stderr)
	case "healthcheck":
		return cmdHealthcheck(configPath, rest, stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "未知的指令：%s\n\n%s", cmd, usage)
		return 2
	}
}

// globalConfig consumes the global --config flags before the subcommand and
// returns the resulting config path and the remaining arguments.
func globalConfig(configPath string, args []string) (string, []string) {
	for len(args) > 0 {
		switch {
		case len(args) >= 2 && (args[0] == "--config" || args[0] == "-config"):
			configPath, args = args[1], args[2:]
		case strings.HasPrefix(args[0], "--config="):
			configPath, args = strings.TrimPrefix(args[0], "--config="), args[1:]
		case strings.HasPrefix(args[0], "-config="):
			configPath, args = strings.TrimPrefix(args[0], "-config="), args[1:]
		default:
			return configPath, args
		}
	}
	return configPath, args
}
