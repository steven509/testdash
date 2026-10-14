package profile

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var escapes = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// ParseStats reads a line of docker stats --format '{{json .}}', which a streaming docker stats starts with
// escape codes that move the cursor.
func ParseStats(line string, at time.Time) (Usage, bool) {
	line = strings.TrimSpace(escapes.ReplaceAllString(line, ""))
	var raw struct {
		CPUPerc, MemUsage, NetIO, BlockIO, PIDs string
	}
	if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &raw) != nil {
		return Usage{}, false
	}
	u := Usage{At: at}
	u.CPU, _ = strconv.ParseFloat(strings.TrimSuffix(raw.CPUPerc, "%"), 64)
	u.Memory, u.MemoryLimit = pair(raw.MemUsage)
	u.NetIn, u.NetOut = pair(raw.NetIO)
	u.DiskRead, u.DiskWritten = pair(raw.BlockIO)
	u.Processes, _ = strconv.Atoi(raw.PIDs)
	return u, raw.CPUPerc != ""
}

// pair reads docker's "12.5MiB / 7.7GiB".
func pair(text string) (int64, int64) {
	a, b, _ := strings.Cut(text, "/")
	return Size(a), Size(b)
}

var units = map[string]float64{
	"b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
}

// Size reads a size as docker prints it: decimal units (kB, MB) for network and disk, binary ones (MiB, GiB) for
// memory.
func Size(text string) int64 {
	text = strings.ToLower(strings.TrimSpace(text))
	end := strings.IndexFunc(text, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if end < 0 {
		end = len(text)
	}
	n, err := strconv.ParseFloat(text[:end], 64)
	if err != nil {
		return 0
	}
	if unit, ok := units[strings.TrimSpace(text[end:])]; ok {
		n *= unit
	}
	return int64(n)
}
