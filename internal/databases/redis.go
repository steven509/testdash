package databases

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/steven509/testdash/internal/runner"
	"github.com/steven509/testdash/internal/services"
)

// redisLog follows a Redis server's commands with MONITOR, which redis-cli runs in the container. MONITOR costs
// the server some speed, which a test's Redis can spare.
type redisLog struct {
	errorHolder
	service *services.Service
}

var (
	redisLine = regexp.MustCompile(`^(\d+)\.(\d+) \[(\d+) ([^\]]+)\] (.*)$`)
	// redisChatter is what clients send to set up and check a connection.
	redisChatter = map[string]bool{"PING": true, "ECHO": true, "INFO": true, "CLIENT": true, "HELLO": true, "AUTH": true,
		"SELECT": true, "COMMAND": true, "QUIT": true, "RESET": true, "READONLY": true, "READWRITE": true}
)

func (r *redisLog) Follow(ctx context.Context, onQueries func([]Query)) {
	for ctx.Err() == nil {
		if container := r.service.ContainerID(); r.service.State() == "running" && container != "" {
			r.followOnce(ctx, container, onQueries)
		}
		sleep(ctx, 2*time.Second)
	}
}

func (r *redisLog) followOnce(ctx context.Context, container string, onQueries func([]Query)) {
	password := services.ContainerEnv(ctx, container)["REDIS_PASSWORD"] // as Bitnami's image takes it
	if password == "" {
		password = requirePass(r.service.Command())
	}
	args := []string{"exec"}
	if password != "" {
		args = append(args, "-e", "REDISCLI_AUTH="+password)
	}
	args = append(args, container, "redis-cli", "MONITOR")
	refused := ""
	parse := func(line []byte) (Query, bool) {
		text := strings.TrimSpace(string(line))
		if strings.HasPrefix(text, "(error)") || strings.HasPrefix(text, "NOAUTH") || strings.HasPrefix(text, "ERR") {
			refused = text // redis-cli prints the server's errors with its output
		}
		return redisCommand(text)
	}
	gotAny, stderr, _ := runner.Batched(ctx, exec.CommandContext(ctx, "docker", args...), parse,
		func() bool { return r.service.ContainerID() == container },
		func(batch []Query) {
			r.setError("")
			onQueries(batch)
		})
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	last := lines[len(lines)-1]
	switch {
	case gotAny || ctx.Err() != nil:
	case refused != "":
		r.setError(refused[:min(200, len(refused))])
	case strings.Contains(last, "executable file not found"):
		r.setError("the container has no redis-cli")
	case last != "" && !strings.Contains(last, "Connection refused"):
		r.setError(last[:min(200, len(last))])
	}
}

// requirePass is the password a redis-server command line sets, if it does.
func requirePass(command []string) string {
	words := strings.Fields(strings.Join(command, " "))
	for i, word := range words {
		if word == "--requirepass" && i+1 < len(words) {
			return strings.Trim(words[i+1], `"'`)
		}
	}
	return ""
}

// redisCommand reads a MONITOR line: 1760313600.123456 [0 172.18.0.5:52342] "SET" "cart:42" "{...}".
func redisCommand(line string) (Query, bool) {
	match := redisLine.FindStringSubmatch(line)
	if match == nil {
		return Query{}, false
	}
	unix, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return Query{}, false
	}
	micros, _ := strconv.ParseInt((match[2] + "000000")[:6], 10, 64)
	args := redisArgs(match[5])
	if len(args) == 0 {
		return Query{}, false
	}
	client := match[4]
	if match[3] != "0" {
		client += " db " + match[3]
	}
	command := strings.ToUpper(args[0])
	return Query{At: time.Unix(unix, micros*1000), Connection: client, Kind: command, Text: RedisCommandLine(args),
		Args: args, chatter: redisChatter[command]}, true
}

// redisArgs reads MONITOR's quoted arguments, which escape \\, \", \n, \r, \t, \a, \b and other bytes as \xHH.
func redisArgs(text string) []string {
	var args []string
	for i := 0; i < len(text); {
		if text[i] != '"' {
			i++
			continue
		}
		var arg strings.Builder
		i++
		for i < len(text) && text[i] != '"' {
			if text[i] == '\\' && i+1 < len(text) {
				i++
				switch text[i] {
				case 'n':
					arg.WriteByte('\n')
				case 'r':
					arg.WriteByte('\r')
				case 't':
					arg.WriteByte('\t')
				case 'a':
					arg.WriteByte('\a')
				case 'b':
					arg.WriteByte('\b')
				case 'x':
					if i+2 < len(text) {
						if b, err := strconv.ParseUint(text[i+1:i+3], 16, 8); err == nil {
							arg.WriteByte(byte(b))
							i += 2
						}
					}
				default:
					arg.WriteByte(text[i])
				}
			} else {
				arg.WriteByte(text[i])
			}
			i++
		}
		args = append(args, arg.String())
		i++
	}
	return args
}

// RedisCommandLine writes a command as redis-cli takes it, quoting the arguments that need it.
func RedisCommandLine(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = QuoteRedisArg(arg)
	}
	return strings.Join(quoted, " ")
}

var plainRedisArg = regexp.MustCompile(`^[^\s"'\\]+$`)

// QuoteRedisArg is an argument as redis-cli reads it: as it is, or in double quotes with escapes.
func QuoteRedisArg(arg string) string {
	if plainRedisArg.MatchString(arg) && strconv.IsPrint(rune(arg[0])) {
		return arg
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(arg); i++ {
		switch c := arg[i]; {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
