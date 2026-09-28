package main

import (
	"log"
	"os"
	"runtime/debug"

	"webufw/internal/webufw"
)

func main() {
	debug.SetMemoryLimit(24 << 20)
	var err error
	// Process roles are private to the service; there is no user-facing CLI.
	switch os.Getenv("WEBUFW_PROCESS") {
	case "web":
		err = webufw.ServeChild()
	case "ufw":
		args := os.Args[1:]
		// Older verified ufw-docker scripts prepend this private helper marker.
		// It is only accepted in the inherited UFW process role; LockedUFW
		// still checks the root agent's lock and descriptor before execution.
		if len(args) > 0 && args[0] == "_ufw" {
			args = args[1:]
		}
		err = webufw.LockedUFW(args)
	default:
		if len(os.Args) != 1 || os.Getenv("WEBUFW_PROCESS") != "" {
			log.Fatal("WebUFW 由 systemd 管理，請使用 systemctl 管理 webufw 服務")
		}
		err = webufw.Run()
	}
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
