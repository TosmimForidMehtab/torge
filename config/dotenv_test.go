package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TosmimForidMehtab/torge/config"
)

func TestParseDotEnv(t *testing.T) {
	t.Setenv("FROM_PROCESS", "proc")
	src := `# comment
export TORGE_ENV=development
PLAIN=value # trailing comment
HASH_IN_VALUE=abc#def
SPACES =  padded
SINGLE='literal ${PLAIN} \n'
DOUBLE="line1\nline2 \"quoted\" \${PLAIN}"
EXPANDED=${PLAIN}-${FROM_PROCESS}-${MISSING}
URL=postgres://${PLAIN}@db/app
PEM="-----BEGIN KEY-----
abc
-----END KEY-----"
EMPTY=
`
	got, err := config.ParseDotEnv(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"TORGE_ENV":     "development",
		"PLAIN":         "value",
		"HASH_IN_VALUE": "abc#def",
		"SPACES":        "padded",
		"SINGLE":        `literal ${PLAIN} \n`,
		"DOUBLE":        "line1\nline2 \"quoted\" ${PLAIN}",
		"EXPANDED":      "value-proc-",
		"URL":           "postgres://value@db/app",
		"PEM":           "-----BEGIN KEY-----\nabc\n-----END KEY-----",
		"EMPTY":         "",
	}
	if !reflect.DeepEqual(got, want) {
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: got %q want %q", k, got[k], v)
			}
		}
	}
}

func TestParseDotEnvErrors(t *testing.T) {
	for src, want := range map[string]string{
		"GOOD=1\nnot a pair":   ".env:2: expected KEY=value",
		"1BAD=x":               ".env:1: expected KEY=value",
		"K='open":              ".env:1: unterminated single-quoted value for K",
		"A=1\nK=\"open\nstill": ".env:2: unterminated double-quoted value for K",
		`K="bad \q escape"`:    `.env:1: K: unknown escape \q`,
	} {
		_, err := config.ParseDotEnv(strings.NewReader(src))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", src, err, want)
		}
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDotEnvPrecedence(t *testing.T) {
	dir := t.TempDir()
	local := writeFile(t, dir, ".env.local", "SHARED=local\nONLY_LOCAL=1\n")
	base := writeFile(t, dir, ".env", "SHARED=base\nONLY_BASE=2\nALREADY_SET=from-file\n")
	for _, k := range []string{"SHARED", "ONLY_LOCAL", "ONLY_BASE", "TORGE_ENV", "APP_ENV"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("ALREADY_SET", "from-process")

	if err := config.LoadDotEnv(local, filepath.Join(dir, "missing.env"), base); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"SHARED": "local", "ONLY_LOCAL": "1", "ONLY_BASE": "2", "ALREADY_SET": "from-process",
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestLoadDotEnvFeedsConfigAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, ".env", "TORGE_ENV=development\nREDIS_URL=redis://localhost:6379/0\nSENDGRID_KEY=SG.dev\n")
	for _, k := range []string{"TORGE_ENV", "APP_ENV", "REDIS_URL", "SENDGRID_KEY"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if err := config.LoadDotEnv(file); err != nil {
		t.Fatal(err)
	}
	if env, _ := config.CurrentEnv(); env != config.Development {
		t.Fatalf("TORGE_ENV from .env must apply, got %s", env)
	}
	var cfg AppConfig
	if err := config.Load(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Redis == nil || cfg.SendGridKey.Value() != "SG.dev" {
		t.Fatalf("config must see .env values: %+v", cfg)
	}
}

func TestLoadDotEnvWorksInEveryEnvironment(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, ".env", "FROM_FILE=1\nINJECTED=from-file\n")
	t.Setenv("FROM_FILE", "")
	os.Unsetenv("FROM_FILE")
	t.Setenv("INJECTED", "from-platform") // as Render or Kubernetes would set it
	t.Setenv("TORGE_ENV", "production")
	t.Setenv(config.EnvFileVar, "")
	if err := config.LoadDotEnv(file); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FROM_FILE") != "1" || os.Getenv("INJECTED") != "from-platform" {
		t.Fatal(".env must load in production too, never overriding injected variables")
	}
}

func TestLoadDotEnvFindsFileNextToExecutable(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	exeEnv := filepath.Join(filepath.Dir(exe), ".env")
	if _, err := os.Stat(exeEnv); err == nil {
		t.Skip("a .env already exists next to the test binary")
	}
	if err := os.WriteFile(exeEnv, []byte("BESIDE_BINARY=yes\n"), 0o600); err != nil {
		t.Skip("cannot write next to the test binary:", err)
	}
	t.Cleanup(func() { os.Remove(exeEnv) })
	t.Chdir(t.TempDir()) // like systemd starting the service in /
	t.Setenv("BESIDE_BINARY", "")
	os.Unsetenv("BESIDE_BINARY")
	t.Setenv(config.EnvFileVar, "")
	if err := config.LoadDotEnv(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("BESIDE_BINARY") != "yes" {
		t.Fatal(".env next to the executable must be found from any working directory")
	}
}

func TestLoadDotEnvMissingDefaultFile(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("TORGE_ENV", "")
	if err := config.LoadDotEnv(); err != nil {
		t.Fatalf("a missing .env must not be an error: %v", err)
	}
}

func TestExplicitEnvFileLoadsInProduction(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "app.env", "VPS_SECRET=from-file\nSHARED=a\n")
	b := writeFile(t, dir, "extra.env", "SHARED=b\nEXTRA=1\n")
	for _, k := range []string{"VPS_SECRET", "SHARED", "EXTRA"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("TORGE_ENV", "production")
	t.Setenv(config.EnvFileVar, a+", "+b)
	if err := config.LoadDotEnv(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("VPS_SECRET") != "from-file" || os.Getenv("SHARED") != "a" || os.Getenv("EXTRA") != "1" {
		t.Fatal("TORGE_ENV_FILE must load its files even in production, first file winning")
	}

	t.Setenv(config.EnvFileVar, filepath.Join(dir, "missing.env"))
	if err := config.LoadDotEnv(); err == nil {
		t.Fatal("an explicitly named file that does not exist must be an error")
	}
}
