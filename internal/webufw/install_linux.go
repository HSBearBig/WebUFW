//go:build linux

package webufw

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

const systemdUnit = `[Unit]
Description=WebUFW firewall management
After=network.target ufw.service docker.service

[Service]
Type=simple
ExecStart=/usr/local/bin/webufw run
Restart=on-failure
RestartSec=3
KillMode=mixed
TimeoutStopSec=180
UMask=0077
RuntimeDirectory=webufw
RuntimeDirectoryMode=0750
StateDirectory=webufw
StateDirectoryMode=0700
Environment=GOMEMLIMIT=24MiB
Environment=GOGC=80
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=full
ReadWritePaths=-/etc/ufw -/etc/default/ufw /etc/webufw
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK

[Install]
WantedBy=multi-user.target
`

func Install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	passwordFile := fs.String("password-file", "", "read initial password from a private file")
	dry := fs.Bool("dry-run", false, "顯示安裝內容，不修改系統")
	noStart := fs.Bool("no-start", false, "只安裝檔案，不啟用或啟動服務")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if *dry {
		fmt.Println("將安裝 /usr/local/bin/webufw、webufw 服務帳號與 webufw.service；管理者密碼可於首次啟動後由網頁設定。")
		if *noStart {
			fmt.Println("依 --no-start，服務不會啟用或啟動。")
		} else {
			fmt.Println("安裝後會啟用並啟動 WebUFW；重新安裝會重新啟動服務。")
		}
		fmt.Println("不會安裝 UFW、Docker 或 ufw-docker，也不會修改防火牆規則。")
		return nil
	}
	if e := ensureRoot(); e != nil {
		return e
	}
	var e error
	c, configErr := readConfig(configLocation())
	if configErr != nil && !os.IsNotExist(configErr) {
		return configErr
	}
	first := os.IsNotExist(configErr)
	if first {
		c = Config{Listen: "127.0.0.1:8088"}
		if *passwordFile != "" {
			info, err := os.Stat(*passwordFile)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				return errors.New("密碼檔案權限必須為 0600 或更嚴格")
			}
			b, err := readBounded(*passwordFile)
			if err != nil {
				return err
			}
			password := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
			hash, err := passwordHash(password)
			if err != nil {
				return err
			}
			c.PasswordHash = hash
		}
	}
	account, e := user.Lookup("webufw")
	if e != nil {
		if _, e = runCommand(context.Background(), "/usr/sbin/useradd", []string{"--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "webufw"}, nil, nil); e != nil {
			return e
		}
	} else if account.Uid == "0" || account.Gid == "0" {
		return errors.New("webufw 既有帳號不可為 root")
	}
	for _, p := range []string{"/etc/webufw", "/var/lib/webufw"} {
		if e = os.MkdirAll(p, 0700); e != nil {
			return e
		}
		if e = os.Chmod(p, 0700); e != nil {
			return e
		}
		if e = os.Chown(p, 0, 0); e != nil {
			return e
		}
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	binary, e := os.ReadFile(exe)
	if e != nil {
		return e
	}
	if e = installFile("/usr/local/bin/webufw", binary, 0755); e != nil {
		return e
	}
	if first && c.PasswordHash != "" {
		if e = atomicJSON(configLocation(), c, 0600); e != nil {
			return e
		}
	}
	if e = installFile("/etc/systemd/system/webufw.service", []byte(systemdUnit), 0644); e != nil {
		return e
	}
	if e = systemctl("daemon-reload"); e != nil {
		return e
	}
	if !*noStart {
		if e = systemctl("enable", "webufw.service"); e != nil {
			return fmt.Errorf("已安裝檔案，但無法啟用服務：%w", e)
		}
		if e = systemctl("restart", "webufw.service"); e != nil {
			return fmt.Errorf("已安裝並啟用服務，但無法啟動：%w", e)
		}
	}
	if *noStart {
		fmt.Println("WebUFW 已安裝；服務尚未啟用或啟動。\n前景執行：sudo webufw run")
	} else {
		fmt.Println("WebUFW 已安裝並啟用 systemd 服務。\n服務狀態：sudo systemctl status webufw\n首次設定碼：sudo journalctl -u webufw -n 30 --no-pager")
	}
	fmt.Println("網址：http://" + c.Listen + "\n安裝未修改任何 UFW 規則。Docker 整合設定請參閱專案 docs/deployment.md。")
	return nil
}
func systemctl(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	out := &limitedOutput{limit: 8192}
	cmd.Stdout, cmd.Stderr = out, out
	if e := cmd.Run(); e != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), e, strings.TrimSpace(out.String()))
	}
	return nil
}
func installFile(path string, b []byte, mode os.FileMode) error {
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("拒絕取代非一般檔案：%s", path)
	}
	dir := filepath.Dir(path)
	if e := os.MkdirAll(dir, 0755); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".webufw-install-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = os.Rename(tmp, path)
	}
	if e == nil {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		e = d.Sync()
		d.Close()
	}
	return e
}
