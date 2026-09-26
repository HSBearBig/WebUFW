package webufw

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Lock is shared with the native UFW CLI. Every write checks the revision while
// holding it, and keeps it until all commands and their receipts are persisted.
type Backend interface {
	Lock(context.Context) (func(), error)
	Snapshot(context.Context) (Snapshot, error)
	Execute(context.Context, Step) error
	Logs(context.Context, int) ([]string, error)
}

func enrich(s *Snapshot) {
	pos := map[int]int{}
	for i := range s.Rules {
		r := &s.Rules[i]
		pos[r.Family]++
		r.Position = pos[r.Family]
		r.PendingSync = false
		if r.Kind != "docker" {
			continue
		}
		found := false
		for _, c := range s.Containers {
			if c.Name != r.Container {
				continue
			}
			for _, n := range c.Networks {
				if n.Name != r.Network {
					continue
				}
				ip := n.IPv4
				if r.Family == 6 {
					ip = n.IPv6
				}
				found = ip == r.Destination
			}
		}
		r.PendingSync = !found
	}
	s.CheckedAt = time.Now()
}
func atomicJSON(path string, v any, mode os.FileMode) error {
	if path == "" {
		return nil
	}
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	dir := filepath.Dir(path)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".webufw-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(append(b, '\n'))
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = os.Rename(name, path)
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
func sortedRules(rs []Rule) []Rule {
	v := append([]Rule{}, rs...)
	sort.SliceStable(v, func(i, j int) bool {
		if v[i].Family != v[j].Family {
			return v[i].Family < v[j].Family
		}
		return v[i].Position < v[j].Position
	})
	return v
}
