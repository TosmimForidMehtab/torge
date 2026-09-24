package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// cmdDev runs the application in development mode and restarts it when Go
// source files change. It polls modification times, which is portable and
// needs no dependencies.
func cmdDev(args []string, stdout, stderr io.Writer) error {
	pkg := "."
	if len(args) > 0 {
		pkg = args[0]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	last := snapshot(".")
	for {
		fmt.Fprintf(stdout, "torge dev: building %s\n", pkg)
		bin := filepath.Join(os.TempDir(), fmt.Sprintf("torge-dev-%d%s", os.Getpid(), exeSuffix()))
		build := exec.CommandContext(ctx, "go", "build", "-o", bin, pkg)
		build.Stdout, build.Stderr = stdout, stderr
		var proc *exec.Cmd
		if err := build.Run(); err != nil {
			fmt.Fprintln(stderr, "torge dev: build failed; waiting for changes")
		} else {
			proc = exec.Command(bin)
			proc.Env = append(os.Environ(), "TORGE_ENV=development")
			proc.Stdout, proc.Stderr = stdout, stderr
			if err := proc.Start(); err != nil {
				return err
			}
		}
		changed := waitForChange(ctx, &last)
		if proc != nil {
			stopProcess(proc)
		}
		_ = os.Remove(bin)
		if !changed {
			return nil
		}
		fmt.Fprintln(stdout, "torge dev: change detected, restarting")
	}
}

func exeSuffix() string {
	if os.PathSeparator == '\\' {
		return ".exe"
	}
	return ""
}

func stopProcess(p *exec.Cmd) {
	if p.Process == nil {
		return
	}
	// Ask for a graceful shutdown first; Windows has no SIGTERM delivery.
	if err := p.Process.Signal(os.Interrupt); err != nil {
		_ = p.Process.Kill()
	}
	done := make(chan struct{})
	go func() { _ = p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = p.Process.Kill()
		<-done
	}
}

// waitForChange blocks until a watched file changes (true) or ctx ends
// (false).
func waitForChange(ctx context.Context, last *map[string]time.Time) bool {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			cur := snapshot(".")
			if !sameSnapshot(*last, cur) {
				*last = cur
				return true
			}
		}
	}
}

func snapshot(root string) map[string]time.Time {
	out := make(map[string]time.Time)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(name, ".go") || name == "go.mod" || name == "go.sum" {
			if info, err := d.Info(); err == nil {
				out[path] = info.ModTime()
			}
		}
		return nil
	})
	return out
}

func sameSnapshot(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !b[k].Equal(v) {
			return false
		}
	}
	return true
}
