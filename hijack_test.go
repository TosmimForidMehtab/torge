package torge_test

import (
	"bufio"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

// TestHijackForWebSockets verifies that connection hijacking (used by
// WebSocket libraries such as coder/websocket and gorilla/websocket) works
// through Torge's response writer, both directly and via
// http.ResponseController.
func TestHijackForWebSockets(t *testing.T) {
	app := torgetest.NewApp(t)
	upgrade := func(c *torge.Context) error {
		conn, rw, err := http.NewResponseController(c.Response()).Hijack()
		if err != nil {
			return err
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()
		line, _ := rw.ReadString('\n')
		_, _ = rw.WriteString("echo:" + line)
		return rw.Flush()
	}
	app.GET("/ws", upgrade)
	app.GET("/ws-direct", func(c *torge.Context) error {
		if _, ok := any(c.Response()).(http.Hijacker); !ok {
			t.Error("ResponseWriter must implement http.Hijacker")
		}
		return upgrade(c)
	})
	srv := torgetest.Server(t, app)

	for _, path := range []string{"/ws", "/ws-direct"} {
		conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write([]byte("GET " + path + " HTTP/1.1\r\nHost: x\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\nhello\n"))
		r := bufio.NewReader(conn)
		status, _ := r.ReadString('\n')
		if !strings.Contains(status, "101") {
			t.Fatalf("%s: status line %q", path, status)
		}
		for {
			l, err := r.ReadString('\n')
			if err != nil || l == "\r\n" {
				break
			}
		}
		if echo, _ := r.ReadString('\n'); echo != "echo:hello\n" {
			t.Fatalf("%s: got %q", path, echo)
		}
		conn.Close()
	}
}
