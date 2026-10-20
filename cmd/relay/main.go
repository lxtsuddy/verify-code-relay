// Command relay forwards verification codes and links from IMAP mailboxes to Telegram.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const usage = `用法：relay <指令> [參數]

指令：
  run                              啟動服務與 Web 管理介面
  config import <檔案>             匯入 YAML 設定（展開 ${環境變數}），存成新版本
  config export                    輸出目前設定（含機密），用於備份
  config validate [檔案]           檢查 YAML 設定檔
  admin reset                      重設管理員密碼並清除 TOTP
  login <信箱>                     Microsoft 信箱授權（備援；建議在 Web 上授權）
  test --eml <檔案> [--mailbox <信箱>] [--send <目標>]
  send-test <目標>
  healthcheck [--url 網址]

環境變數：RELAY_DATA_DIR、RELAY_LISTEN、RELAY_HEALTH_LISTEN、RELAY_ADMIN_PASSWORD、RELAY_TRUSTED_PROXIES
`

func main() { os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr)) }

func realMain(args []string, stdout, stderr io.Writer) int {
	// The YAML file (--config before the subcommand, or RELAY_CONFIG) is only used by
	// config import and config validate; everything else reads the database.
	configPath, explicit, args := globalConfig(os.Getenv("RELAY_CONFIG"), args)
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	if explicit && cmd != "config" {
		fmt.Fprintf(stderr, "--config 只適用於 config import／validate；%s 改從資料庫（RELAY_DATA_DIR）讀取設定\n", cmd)
		return 2
	}
	switch cmd {
	case "run":
		return cmdRun(rest, stdout, stderr)
	case "config":
		return cmdConfig(configPath, rest, stdout, stderr)
	case "admin":
		return cmdAdmin(rest, stdout, stderr)
	case "login":
		return cmdLogin(rest, stdout, stderr)
	case "test":
		return cmdTest(rest, stdout, stderr)
	case "send-test":
		return cmdSendTest(rest, stdout, stderr)
	case "healthcheck":
		return cmdHealthcheck(rest, stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "未知的指令：%s\n\n%s", cmd, usage)
		return 2
	}
}

// globalConfig consumes the global --config flags before the subcommand and returns the
// resulting config path, whether a flag was given, and the remaining arguments.
func globalConfig(configPath string, args []string) (string, bool, []string) {
	explicit := false
	for len(args) > 0 {
		switch {
		case len(args) >= 2 && (args[0] == "--config" || args[0] == "-config"):
			configPath, args = args[1], args[2:]
		case strings.HasPrefix(args[0], "--config="):
			configPath, args = strings.TrimPrefix(args[0], "--config="), args[1:]
		case strings.HasPrefix(args[0], "-config="):
			configPath, args = strings.TrimPrefix(args[0], "-config="), args[1:]
		default:
			return configPath, explicit, args
		}
		explicit = true
	}
	return configPath, explicit, args
}
