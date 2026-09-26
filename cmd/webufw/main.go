package main

import (
	"fmt"
	"log"
	"os"
	"runtime/debug"

	"webufw/internal/webufw"
)

func main() {
	debug.SetMemoryLimit(24 << 20)
	if len(os.Args) < 2 {
		usage()
		return
	}
	var err error
	switch os.Args[1] {
	case "install":
		err = webufw.Install(os.Args[2:])
	case "run":
		err = webufw.Run(os.Args[2:])
	case "resolve":
		err = webufw.Resolve(os.Args[2:])
	case "_web":
		err = webufw.ServeChild(os.Args[2:])
	case "_ufw":
		err = webufw.LockedUFW(os.Args[2:])
	case "version":
		fmt.Println("WebUFW 0.1.0-dev")
	case "help", "--help", "-h":
		usage()
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func usage() {
	fmt.Print(`WebUFW — 原生 Linux 防火牆管理

  sudo webufw install         安裝、啟用並啟動 systemd 服務
  sudo webufw install --no-start  只安裝檔案
  sudo webufw run             前景執行
  webufw install --dry-run    檢視安裝內容
  webufw version             顯示版本

服務狀態：sudo systemctl status webufw
`)
}
