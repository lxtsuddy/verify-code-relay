package main

import (
	"fmt"
	"io"
)

func cmdValidate(configPath string, args []string, stdout, stderr io.Writer) int {
	path, ok := configFile("config validate", configPath, args, stderr)
	if !ok {
		return 2
	}
	a, err := loadApp(path)
	if err != nil {
		fmt.Fprintf(stderr, "設定檔有誤（%s）：\n%v\n", path, err)
		return 1
	}
	folders := 0
	for _, m := range a.Config.Mailboxes {
		folders += len(m.Folders)
	}
	fmt.Fprintf(stdout, "設定檔正確：Bot %d 個、目標 %d 個、信箱 %d 個（監看 %d 個資料夾）、自訂規則 %d 條\n",
		len(a.Config.Bots), len(a.Config.Targets), len(a.Config.Mailboxes), folders, len(a.Config.Rules))
	return 0
}
