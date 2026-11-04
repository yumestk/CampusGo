package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

func probeGateway(ctx context.Context, runID string) error {
	listener, err := net.Listen("tcp", ":18083")
	if err != nil {
		return err
	}
	slowEntered := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__probe/headers", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(r.Header)
	})
	mux.HandleFunc("GET /__probe/rate", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /__probe/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case slowEntered <- struct{}{}:
		default:
		}
		select {
		case <-time.After(500 * time.Millisecond):
			w.WriteHeader(200)
		case <-r.Context().Done():
		}
	})
	mux.Handle("/__probe/ws", websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		var message string
		if websocket.Message.Receive(conn, &message) == nil {
			_ = websocket.Message.Send(conn, message)
		}
	}))
	upstream := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	done := make(chan error, 1)
	go func() { done <- upstream.Serve(listener) }()
	defer func() { upstream.Close(); <-done }()
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(path string, headers map[string]string) (int, http.Header, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:19080"+path, nil)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("X-Probe-Run", runID)
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		response, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		var observed http.Header
		if path == "/__probe/headers" && response.StatusCode == 200 {
			err = json.NewDecoder(response.Body).Decode(&observed)
		}
		return response.StatusCode, observed, err
	}
	status, _, err := request("/livez", nil)
	if err != nil || status != 200 {
		return fmt.Errorf("HTTP forwarding status=%d: %v", status, err)
	}
	status, _, err = request("/api/internal/secret", nil)
	if err != nil || status != 404 {
		return fmt.Errorf("internal path status=%d: %v", status, err)
	}
	spoofed := map[string]string{"X-User-Id": "forged", "X-Runner-Id": "forged", "X-Campus-Id": "forged", "X-Internal-Token": "forged", "Forwarded": "for=203.0.113.1", "X-Forwarded-For": "203.0.113.1"}
	status, headers, err := request("/__probe/headers", spoofed)
	if err != nil || status != 200 {
		return fmt.Errorf("header forwarding status=%d: %v", status, err)
	}
	for key, value := range spoofed {
		if strings.Contains(headers.Get(key), value) {
			return fmt.Errorf("spoofed header survived: %s", key)
		}
	}
	for _, expected := range []int{200, 200, 429} {
		status, _, err := request("/__probe/rate", nil)
		if err != nil || status != expected {
			return fmt.Errorf("Redis rate limit status=%d want=%d: %v", status, expected, err)
		}
	}
	first := make(chan error, 1)
	go func() {
		status, _, err := request("/__probe/slow", nil)
		if err == nil && status != 200 {
			err = fmt.Errorf("first concurrent request status=%d", status)
		}
		first <- err
	}()
	select {
	case <-slowEntered:
	case err := <-first:
		return fmt.Errorf("concurrent request never reached upstream: %v", err)
	case <-time.After(6 * time.Second):
		return fmt.Errorf("concurrent request did not reach upstream within 6 seconds")
	case <-ctx.Done():
		return ctx.Err()
	}
	status, _, err = request("/__probe/slow", nil)
	if firstErr := <-first; firstErr != nil {
		return firstErr
	}
	if err != nil || status != 503 {
		return fmt.Errorf("concurrency rejection status=%d: %v", status, err)
	}
	wsCfg, err := websocket.NewConfig("ws://127.0.0.1:19080/__probe/ws", "http://127.0.0.1")
	if err != nil {
		return err
	}
	wsCfg.Dialer = &net.Dialer{Timeout: 5 * time.Second}
	conn, err := websocket.DialConfig(wsCfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := websocket.Message.Send(conn, runID); err != nil {
		return err
	}
	var echoed string
	if err := websocket.Message.Receive(conn, &echoed); err != nil {
		return err
	}
	if echoed != runID {
		return fmt.Errorf("WebSocket echo mismatch")
	}
	return nil
}
