package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// EnvFileVar names the variable that selects the environment files to load,
// replacing the default locations.
const EnvFileVar = "TORGE_ENV_FILE"

// LoadDotEnv loads variables from .env files into the process environment,
// like Node's dotenv. Call it first in main, before config.Load and
// torge.New, so values such as TORGE_ENV take effect:
//
//	if err := config.LoadDotEnv(); err != nil {
//	    log.Fatal(err)
//	}
//
// or import the autoload package for the same effect:
//
//	import _ "github.com/TosmimForidMehtab/torge/config/autoload"
//
// Rules, identical in every environment:
//
//   - Variables already set in the environment always win. Platforms that
//     inject configuration (Render, Heroku, Kubernetes, Docker, systemd)
//     therefore always take precedence over any file.
//   - Without arguments, .env is looked up in the working directory and then
//     next to the executable, so a binary started by systemd or a process
//     manager from another directory still finds the .env beside it. A
//     missing file is not an error, so containers without one work unchanged.
//   - With several files, the first to set a variable wins, so list the most
//     specific first: LoadDotEnv(".env.local", ".env").
//   - TORGE_ENV_FILE=/path/app.env[,/path/other.env] replaces these
//     locations (for example a platform-mounted secret file); the named files
//     must exist.
//
// Syntax: KEY=value lines; blank lines and # comments are ignored; an
// optional "export " prefix is allowed; 'single quotes' are literal;
// "double quotes" support \n, \t, \", \\ escapes and may span lines (for
// PEM keys); unquoted values end at " #". ${VAR} in unquoted and
// double-quoted values expands to a variable defined earlier in the file or
// in the environment. Commit a .env.example and keep .env out of version
// control.
func LoadDotEnv(files ...string) error {
	if explicit := os.Getenv(EnvFileVar); explicit != "" {
		var paths []string
		for p := range strings.SplitSeq(explicit, ",") {
			if p = strings.TrimSpace(p); p != "" {
				paths = append(paths, p)
			}
		}
		return loadEnvFiles(paths, true)
	}
	if len(files) == 0 {
		files = defaultEnvFiles()
	}
	return loadEnvFiles(files, false)
}

// defaultEnvFiles returns .env in the working directory and, when it is a
// different directory, .env next to the executable.
func defaultEnvFiles() []string {
	files := []string{".env"}
	exe, err := os.Executable()
	if err != nil {
		return files
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	exeDir := filepath.Dir(exe)
	if wd, err := os.Getwd(); err == nil && sameDir(wd, exeDir) {
		return files
	}
	return append(files, filepath.Join(exeDir, ".env"))
}

func sameDir(a, b string) bool {
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

func loadEnvFiles(files []string, mustExist bool) error {
	for _, name := range files {
		f, err := os.Open(name)
		if errors.Is(err, fs.ErrNotExist) && !mustExist {
			continue
		}
		if err != nil {
			return fmt.Errorf("config: open %s: %w", name, err)
		}
		vars, err := parseDotEnv(f, name, os.LookupEnv)
		f.Close()
		if err != nil {
			return err
		}
		for _, kv := range vars {
			if _, set := os.LookupEnv(kv.key); set {
				continue
			}
			if err := os.Setenv(kv.key, kv.value); err != nil {
				return fmt.Errorf("config: set %s from %s: %w", kv.key, name, err)
			}
		}
	}
	return nil
}

// ParseDotEnv parses .env content into a map without touching the
// environment. ${VAR} references resolve against earlier keys and then the
// process environment.
func ParseDotEnv(r io.Reader) (map[string]string, error) {
	vars, err := parseDotEnv(r, ".env", os.LookupEnv)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(vars))
	for _, kv := range vars {
		out[kv.key] = kv.value
	}
	return out, nil
}

type keyValue struct{ key, value string }

func parseDotEnv(r io.Reader, name string, lookup func(string) (string, bool)) ([]keyValue, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var out []keyValue
	defined := make(map[string]string)
	resolve := func(k string) string {
		if v, ok := defined[k]; ok {
			return v
		}
		v, _ := lookup(k)
		return v
	}
	lineNo := 0
	errAt := func(line int, format string, args ...any) error {
		return fmt.Errorf("config: %s:%d: %s", name, line, fmt.Sprintf(format, args...))
	}
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, raw, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !validKey(key) {
			return nil, errAt(lineNo, "expected KEY=value with a key of letters, digits and underscores")
		}
		raw = strings.TrimLeft(raw, " \t")
		start := lineNo
		var value string
		switch {
		case strings.HasPrefix(raw, "'"):
			end := strings.IndexByte(raw[1:], '\'')
			if end < 0 {
				return nil, errAt(start, "unterminated single-quoted value for %s", key)
			}
			value = raw[1 : end+1]
		case strings.HasPrefix(raw, `"`):
			body := raw[1:]
			for !closedDoubleQuote(body) {
				if !sc.Scan() {
					return nil, errAt(start, "unterminated double-quoted value for %s", key)
				}
				lineNo++
				body += "\n" + sc.Text()
			}
			v, err := unescapeDouble(body[:closingQuote(body)])
			if err != nil {
				return nil, errAt(start, "%s: %v", key, err)
			}
			value = strings.ReplaceAll(expand(v, resolve), escapedDollar, "$")
		default:
			if i := strings.Index(raw, " #"); i >= 0 {
				raw = raw[:i]
			}
			value = expand(strings.TrimSpace(raw), resolve)
		}
		defined[key] = value
		out = append(out, keyValue{key, value})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("config: read %s: %w", name, err)
	}
	return out, nil
}

func validKey(k string) bool {
	if k == "" || (k[0] >= '0' && k[0] <= '9') {
		return false
	}
	for i := range len(k) {
		c := k[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// closingQuote returns the index of the first unescaped double quote, or -1.
func closingQuote(s string) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

func closedDoubleQuote(s string) bool { return closingQuote(s) >= 0 }

// escapedDollar stands in for "\$" until ${VAR} expansion is done.
const escapedDollar = string(rune(0xE000)) // a Unicode private-use character

func unescapeDouble(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '"', '\\':
			b.WriteByte(s[i])
		case '$':
			b.WriteString(escapedDollar) // restored after ${VAR} expansion
		default:
			return "", fmt.Errorf("unknown escape \\%c", s[i])
		}
	}
	return b.String(), nil
}

// expand replaces ${VAR} references. A literal "${" can be written as "\${"
// inside double quotes.
func expand(s string, resolve func(string) string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 || !validKey(s[i+2:i+j]) {
			b.WriteString(s[:i+2])
			s = s[i+2:]
			continue
		}
		b.WriteString(s[:i])
		b.WriteString(resolve(s[i+2 : i+j]))
		s = s[i+j+1:]
	}
}
