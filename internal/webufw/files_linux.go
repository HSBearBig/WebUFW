//go:build linux

package webufw

import (
	"fmt"
	"os"
	"path/filepath"
)

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
