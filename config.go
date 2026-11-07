package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Freq struct {
	Label string
	Hz    int64
}

type Message struct {
	Name string
	Text string
}

type Config struct {
    Callsign  string
	RadioName string
	Host      string
	Port      int

	Freqs      []Freq
	MinDwell   time.Duration
	MaxDwell   time.Duration
	QuietTime  time.Duration
	Offset     int64

	LogDir      string
	AllFile     string
	DirectedFile string

	TXEnabled   bool
	SendOn      map[string]bool // lowercase labels
	SendList    []string        // message names
	MinInterval time.Duration
	Messages    map[string]string
}

type kv struct{ k, v string }

// parseINI: minimal INI reader. Section and key names are lowercased;
// order within a section is preserved. ';' and '#' start comment lines.
func parseINI(path string) (map[string][]kv, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string][]kv{}
	sec := ""
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		if line[0] == '[' && line[len(line)-1] == ']' {
			sec = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		i := strings.IndexByte(line, '=')
		if i < 0 {
			return nil, fmt.Errorf("%s:%d: expected key = value", path, n)
		}
		k := strings.ToLower(strings.TrimSpace(line[:i]))
		v := strings.TrimSpace(line[i+1:])
		out[sec] = append(out[sec], kv{k, v})
	}
	return out, sc.Err()
}

func get(ini map[string][]kv, sec, key, def string) string {
	for _, e := range ini[sec] {
		if e.k == key {
			return e.v
		}
	}
	return def
}

func getSecs(ini map[string][]kv, sec, key string, def int) time.Duration {
	v := get(ini, sec, key, "")
	if v == "" {
		return time.Duration(def) * time.Second
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return time.Duration(def) * time.Second
	}
	return time.Duration(n) * time.Second
}

func parseFreq(s string) (int64, error) {
	if strings.Contains(s, ".") {
		mhz, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, err
		}
		return int64(mhz*1e6 + 0.5), nil
	}
	return strconv.ParseInt(s, 10, 64)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func LoadConfig(path string) (*Config, error) {
	ini, err := parseINI(path)
	if err != nil {
		return nil, err
	}
	c := &Config{
		Callsign:     get(ini, "radio", "callsign", ""),
		RadioName:    get(ini, "radio", "name", "radio"),
		Host:         get(ini, "radio", "host", "127.0.0.1"),
		MinDwell:     getSecs(ini, "scan", "min_dwell", 120),
		MaxDwell:     getSecs(ini, "scan", "max_dwell", 600),
		QuietTime:    getSecs(ini, "scan", "quiet_time", 60),
		LogDir:       expandHome(get(ini, "logs", "dir", "")),
		AllFile:      get(ini, "logs", "all", "ALL.TXT"),
		DirectedFile: get(ini, "logs", "directed", "DIRECTED.TXT"),
		TXEnabled:    strings.EqualFold(get(ini, "tx", "enabled", "false"), "true"),
		SendOn:       map[string]bool{},
		MinInterval:  getSecs(ini, "tx", "min_interval", 900),
		Messages:     map[string]string{},
	}
	if c.Port, err = strconv.Atoi(get(ini, "radio", "port", "2442")); err != nil {
		return nil, fmt.Errorf("[radio] port: %w", err)
	}
	if c.Offset, err = strconv.ParseInt(get(ini, "scan", "offset", "1500"), 10, 64); err != nil {
		return nil, fmt.Errorf("[scan] offset: %w", err)
	}
	for _, e := range ini["frequencies"] {
		hz, err := parseFreq(e.v)
		if err != nil {
			return nil, fmt.Errorf("[frequencies] %s: %w", e.k, err)
		}
		c.Freqs = append(c.Freqs, Freq{Label: e.k, Hz: hz})
	}
	if len(c.Freqs) == 0 {
		return nil, fmt.Errorf("no [frequencies] defined")
	}
	if c.MaxDwell < c.MinDwell {
		c.MaxDwell = c.MinDwell
	}
	for _, l := range splitList(get(ini, "tx", "send_on", "")) {
		c.SendOn[strings.ToLower(l)] = true
	}
	for _, n := range splitList(get(ini, "tx", "send", "")) {
		c.SendList = append(c.SendList, strings.ToLower(n))
	}
	for _, e := range ini["messages"] {
		c.Messages[e.k] = e.v
	}
	for _, n := range c.SendList {
		if _, ok := c.Messages[n]; !ok {
			return nil, fmt.Errorf("[tx] send references unknown message %q", n)
		}
	}
	return c, nil
}
