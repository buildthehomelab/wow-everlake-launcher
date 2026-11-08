package tact

import (
	"errors"
	"fmt"
	"strings"
)

// Config is a build or CDN config: "name = value value ..." lines.
type Config map[string][]string

// ParseConfig reads a build or CDN config. Comment lines (#) and blank lines are skipped.
func ParseConfig(text string) Config {
	c := Config{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, " = ")
		if !ok {
			continue
		}
		c[name] = strings.Fields(value)
	}
	return c
}

// One is the first value of a field, or "".
func (c Config) One(name string) string {
	if v := c[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// KeyPair reads a "content key [encoded key]" field such as "install" or "encoding". The encoded key is zero when
// the config gives only the content key (older builds); then it has to be looked up in the encoding file.
func (c Config) KeyPair(name string) (ckey, ekey Key, err error) {
	v := c[name]
	if len(v) == 0 {
		return ckey, ekey, fmt.Errorf("config has no %q", name)
	}
	if ckey, err = ParseKey(v[0]); err != nil {
		return ckey, ekey, fmt.Errorf("config %q: %w", name, err)
	}
	if len(v) > 1 {
		if ekey, err = ParseKey(v[1]); err != nil {
			return ckey, ekey, fmt.Errorf("config %q: %w", name, err)
		}
	}
	return ckey, ekey, nil
}

// Keys reads a field that is a list of keys, such as "archives".
func (c Config) Keys(name string) ([]Key, error) {
	out := make([]Key, 0, len(c[name]))
	for _, s := range c[name] {
		k, err := ParseKey(s)
		if err != nil {
			return nil, fmt.Errorf("config %q: %w", name, err)
		}
		out = append(out, k)
	}
	return out, nil
}

// VFSNames lists the build's TVFS manifests in order: vfs-root, then vfs-1, vfs-2, ...
func (c Config) VFSNames() []string {
	var out []string
	if _, ok := c["vfs-root"]; ok {
		out = append(out, "vfs-root")
	}
	for i := 1; ; i++ {
		name := fmt.Sprintf("vfs-%d", i)
		if _, ok := c[name]; !ok {
			return out
		}
		out = append(out, name)
	}
}

// ParseTable reads a version server answer (the "versions" and "cdns" files): a pipe-separated table whose header
// names each column with a type ("Region!STRING:0"). Each row comes back as a map keyed by the plain column name.
func ParseTable(text string) ([]map[string]string, error) {
	var head []string
	var rows []map[string]string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cells := strings.Split(line, "|")
		if head == nil {
			for _, h := range cells {
				name, _, _ := strings.Cut(h, "!")
				head = append(head, name)
			}
			continue
		}
		if len(cells) != len(head) {
			return nil, fmt.Errorf("table row has %d cells, header has %d: %q", len(cells), len(head), line)
		}
		row := make(map[string]string, len(head))
		for i, h := range head {
			row[h] = cells[i]
		}
		rows = append(rows, row)
	}
	if head == nil {
		return nil, fmt.Errorf("empty table")
	}
	return rows, nil
}

// BuildFiles lists a build's own stored files by name: encoding, root, install, download, size and the TVFS
// manifests, each as {content key, encoded key}. Fields the build config doesn't have are left out; an encoded key
// the config doesn't give is looked up in enc. The encoding file must name its encoded key in the config.
func (c Config) BuildFiles(enc *Encoding) (map[string][2]Key, error) {
	encC, encE, err := c.KeyPair("encoding")
	if err != nil {
		return nil, err
	}
	if encE.IsZero() {
		return nil, errors.New("the build config names no encoded key for the encoding file")
	}
	out := map[string][2]Key{"encoding": {encC, encE}}
	for _, name := range append([]string{"root", "install", "download", "size"}, c.VFSNames()...) {
		if _, ok := c[name]; !ok {
			continue
		}
		ck, ek, err := c.KeyPair(name)
		if err != nil {
			return nil, err
		}
		if ek.IsZero() {
			ent, ok := enc.Lookup(ck)
			if !ok {
				return nil, fmt.Errorf("build file %s (%s) is not in the encoding file", name, ck)
			}
			ek = ent.EKeys[0]
		}
		out[name] = [2]Key{ck, ek}
	}
	return out, nil
}
