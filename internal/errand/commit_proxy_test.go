package errand

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yumestk/CampusGo/internal/platform/database"
)

// This isolated, plaintext local MySQL proxy drops the server's OK packet for COMMIT.
// Unlike returning a synthetic service error, the real SQL driver sees the lost acknowledgment.
func commitAckProxy(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	upstream := os.Getenv("CAMPUSGO_MYSQL_ADDR")
	if upstream == "" {
		upstream = "127.0.0.1:13306"
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dropped := make(chan struct{})
	var once sync.Once
	var stopped atomic.Bool
	var mu sync.Mutex
	connections := []net.Conn{}
	t.Cleanup(func() {
		stopped.Store(true)
		listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range connections {
			c.Close()
		}
	})
	go func() {
		for {
			client, e := listener.Accept()
			if e != nil {
				return
			}
			server, e := net.DialTimeout("tcp", upstream, time.Second)
			if e != nil {
				client.Close()
				continue
			}
			mu.Lock()
			if stopped.Load() {
				client.Close()
				server.Close()
				mu.Unlock()
				return
			}
			connections = append(connections, client, server)
			mu.Unlock()
			var committing atomic.Bool
			pump := func(dst, src net.Conn, fromClient bool) {
				defer client.Close()
				defer server.Close()
				for {
					var header [4]byte
					if _, e := io.ReadFull(src, header[:]); e != nil {
						return
					}
					length := int(binary.LittleEndian.Uint32(header[:]) & 0xffffff)
					payload := make([]byte, length)
					if _, e := io.ReadFull(src, payload); e != nil {
						return
					}
					if fromClient && len(payload) > 1 && payload[0] == 3 && strings.EqualFold(strings.TrimSpace(string(payload[1:])), "COMMIT") {
						committing.Store(true)
					}
					if !fromClient && committing.Load() && len(payload) > 0 && payload[0] == 0 {
						once.Do(func() { close(dropped) })
						return
					}
					if _, e := io.Copy(dst, bytes.NewReader(append(header[:], payload...))); e != nil {
						return
					}
				}
			}
			go pump(server, client, true)
			go pump(client, server, false)
		}
	}()
	return listener.Addr().String(), dropped
}
func databaseViaProxy(t *testing.T, addr string) *sql.DB {
	t.Helper()
	previous, present := os.LookupEnv("CAMPUSGO_MYSQL_ADDR")
	if err := os.Setenv("CAMPUSGO_MYSQL_ADDR", addr); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), "campusgo_verification")
	if present {
		os.Setenv("CAMPUSGO_MYSQL_ADDR", previous)
	} else {
		os.Unsetenv("CAMPUSGO_MYSQL_ADDR")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
