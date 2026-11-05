//go:build ignore

// Disposable HTTP/WS peer for exercising the real gateway, never registered in the API.
package main

import (
	"encoding/json"
	"fmt"
	"golang.org/x/net/websocket"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) > 1 {
		cfg, _ := websocket.NewConfig("ws://127.0.0.1:19080/__d6/ws", "http://127.0.0.1")
		cfg.Dialer = &net.Dialer{Timeout: 3 * time.Second}
		c, e := websocket.DialConfig(cfg)
		if e != nil {
			panic(e)
		}
		c.SetDeadline(time.Now().Add(3 * time.Second))
		websocket.Message.Send(c, "D6")
		var v string
		e = websocket.Message.Receive(c, &v)
		if e != nil || v != "D6" {
			panic("WS echo failed")
		}
		c.Close()
		fmt.Println("PASS real APISIX WebSocket upgrade, echo and closure")
		return
	}
	mux := http.NewServeMux()
	mux.Handle("/__d6/ws", websocket.Handler(func(c *websocket.Conn) {
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		var s string
		if websocket.Message.Receive(c, &s) == nil {
			websocket.Message.Send(c, s)
			if websocket.Message.Receive(c, &s) != nil {
				os.WriteFile("work/d6-ws-closed", []byte("closed"), 0600)
			}
		}
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__d6/timeout" {
			time.Sleep(4 * time.Second)
		}
		if r.URL.Path == "/__d6/slow" {
			time.Sleep(time.Second)
		}
		if r.URL.Path == "/__d6/cancel" {
			select {
			case <-r.Context().Done():
				os.WriteFile("work/d6-cancelled", []byte("cancelled"), 0600)
				return
			case <-time.After(10 * time.Second):
				return
			}
		}
		r.Header.Set("X-CampusGo-Gateway", "present")
		json.NewEncoder(w).Encode(r.Header)
	})
	s := http.Server{Addr: ":18085", Handler: mux, ReadHeaderTimeout: time.Second}
	s.ListenAndServe()
}
