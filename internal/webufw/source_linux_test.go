//go:build linux

package webufw

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceSelectionAndPrivateInstall(t *testing.T) {
	if _, err := fetchSource(context.Background(), "unknown"); err == nil {
		t.Fatal("unknown source accepted")
	}
	if _, err := fetchSource(context.Background(), "webufw"); err == nil {
		t.Fatal("removed patched source accepted")
	}
	if options := sourceOptions(); len(options) != 2 || options[0]["id"] != "hsbearbig" || options[1]["id"] != "chaifeng" {
		t.Fatalf("unexpected sources: %+v", options)
	}
	dir := t.TempDir()
	script, meta := filepath.Join(dir, "ufw-docker"), filepath.Join(dir, "source.json")
	if _, installed, _ := installedSourceAt(script, meta); installed {
		t.Fatal("script installed before choice")
	}
	content := []byte("#!/bin/bash\nfunction ufw-docker--allow() { :; }\n")
	stage := &sourceStage{sourceState: sourceState{ID: "hsbearbig", Commit: strings.Repeat("a", 40), SHA256: digestBytes(content), URL: "https://raw.githubusercontent.com/HSBearBig/ufw-docker/a/ufw-docker"}, Bytes: content, Expires: time.Now().Add(time.Minute)}
	if _, err := installSourceAt(script, meta, stage); err != nil {
		t.Fatal(err)
	}
	if st, installed, compatible := installedSourceAt(script, meta); !installed || compatible || st.ID != "hsbearbig" {
		t.Fatalf("wrong installed state: %+v %v %v", st, installed, compatible)
	}
	upstream := &sourceStage{sourceState: sourceState{ID: "chaifeng", Commit: strings.Repeat("a", 40), URL: "https://raw.githubusercontent.com/chaifeng/ufw-docker/a/ufw-docker"}, Bytes: append([]byte{}, content...), Expires: time.Now().Add(time.Minute)}
	upstream.Bytes = append(upstream.Bytes, []byte("\n# test upstream variant\n")...)
	upstream.SHA256 = digestBytes(upstream.Bytes)
	backup, err := installSourceAt(script, meta, upstream)
	if err != nil || backup == "" {
		t.Fatalf("backup missing: %q %v", backup, err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatal(err)
	}
	if st, installed, compatible := installedSourceAt(script, meta); !installed || compatible || st.ID != "chaifeng" {
		t.Fatalf("upstream must be read-only: %+v %v %v", st, installed, compatible)
	}
	upstream.Bytes[0] ^= 1
	if _, err := installSourceAt(script, meta, upstream); err == nil {
		t.Fatal("changed staged bytes accepted")
	}
}
