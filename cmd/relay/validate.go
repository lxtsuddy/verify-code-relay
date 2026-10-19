package main

import (
	"fmt"
	"io"
)

func cmdValidate(configPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("config validate", &configPath, stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	a, err := loadApp(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "設定檔有誤（%s）：\n%v\n", configPath, err)
		return 1
	}
	folders := 0
	for _, m := range a.cfg.Mailboxes {
		folders += len(m.Folders)
	}
	fmt.Fprintf(stdout, "設定檔正確：Bot %d 個、目標 %d 個、信箱 %d 個（監看 %d 個資料夾）、自訂規則 %d 條\n",
		len(a.cfg.Bots), len(a.cfg.Targets), len(a.cfg.Mailboxes), folders, len(a.cfg.Rules))
	return 0
}
