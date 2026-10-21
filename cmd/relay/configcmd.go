package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/runtime"
)

// cmdConfig dispatches "config import|export|validate". configPath is the file given by
// the global --config or RELAY_CONFIG ("" when neither).
func cmdConfig(configPath string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "import":
		return cmdImport(configPath, args[1:], stdout, stderr)
	case "export":
		return cmdExport(args[1:], stdout, stderr)
	case "validate":
		if configPath == "" {
			configPath = "config.yaml"
		}
		return cmdValidate(configPath, args[1:], stdout, stderr)
	}
	fmt.Fprint(stderr, usage)
	return 2
}

// configFile resolves the YAML file for import and validate: a positional argument wins
// over --config, which wins over the global --config / RELAY_CONFIG passed in configPath.
// It reports ok=false (after printing the usage line) on bad arguments.
func configFile(name, configPath string, args []string, stderr io.Writer) (string, bool) {
	fs := newConfigFlags(name, &configPath, stderr)
	pos, err := parseInterspersed(fs, args)
	if err != nil || len(pos) > 1 {
		fmt.Fprintf(stderr, "用法：relay %s <檔案>\n", name)
		return "", false
	}
	if len(pos) == 1 {
		configPath = pos[0]
	}
	if configPath == "" {
		fmt.Fprintf(stderr, "用法：relay %s <檔案>\n", name)
		return "", false
	}
	return configPath, true
}

func cmdImport(configPath string, args []string, stdout, stderr io.Writer) int {
	path, ok := configFile("config import", configPath, args, stderr)
	if !ok {
		return 2
	}
	raw, err := config.LoadRaw(path)
	if err != nil {
		return fail(stderr, err)
	}
	if raw.DataDir != "" || raw.HTTPListen != "" {
		fmt.Fprintln(stdout, "已略過 data_dir、http_listen（改用環境變數 RELAY_DATA_DIR、RELAY_LISTEN）")
		raw.DataDir, raw.HTTPListen = "", ""
	}
	st, err := openDB()
	if err != nil {
		return fail(stderr, err)
	}
	defer st.Close()
	ctx := context.Background()
	// the config before, to drop the OAuth tokens the import makes stale; when the stored
	// config cannot be read, nil: every imported Microsoft mailbox loses its token
	before, _, err := runtime.LoadConfig(ctx, st)
	var invalid *runtime.InvalidConfigError
	if err != nil && !errors.As(err, &invalid) {
		return fail(stderr, err)
	}
	_, id, err := runtime.SaveConfig(ctx, st, raw,
		runtime.Meta{Source: "import", Summary: "從 " + filepath.Base(path) + " 匯入"}, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "設定檔有誤（%s）：\n%v\n", path, err)
		return 1
	}
	fmt.Fprintf(stdout, "已匯入為版本 #%d。服務執行中時，請重新啟動或在 Web 上任意存檔以套用\n", id)
	dropped, failed := runtime.DeleteOAuthTokens(ctx, st, runtime.StaleOAuthTokens(before, raw), func(name string, err error) {
		fmt.Fprintf(stderr, "信箱 %s 的 Microsoft 授權已不適用，但清除失敗：%v\n", name, err)
	})
	for _, name := range dropped {
		fmt.Fprintf(stdout, "信箱 %s 的 Microsoft 授權已清除，請重新授權\n", name)
	}
	if len(failed) > 0 {
		return 1
	}
	return 0
}

func cmdExport(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "用法：relay config export")
		return 2
	}
	st, err := openDB()
	if err != nil {
		return fail(stderr, err)
	}
	defer st.Close()
	v, ok, err := st.LatestConfigVersion(context.Background())
	if err != nil {
		return fail(stderr, err)
	}
	if !ok {
		data, err := config.Encode(config.Empty())
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintln(stderr, "資料庫中尚無設定，輸出空設定")
		stdout.Write(data)
		return 0
	}
	io.WriteString(stdout, v.YAML)
	return 0
}
