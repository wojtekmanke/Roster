// Package agents holds the example agent specs that ship with Roster.
package agents

import (
	"embed"
	"io/fs"
	"sort"
)

//go:embed *.yaml
var files embed.FS

// Examples returns the example specs, keyed by file name.
func Examples() (map[string][]byte, error) {
	names, err := fs.Glob(files, "*.yaml")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := make(map[string][]byte, len(names))
	for _, name := range names {
		data, err := files.ReadFile(name)
		if err != nil {
			return nil, err
		}
		out[name] = data
	}
	return out, nil
}
