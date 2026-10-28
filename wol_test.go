package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMAC(t *testing.T) {
	for in, want := range map[string]string{
		"52:54:00:12:34:56": "52:54:00:12:34:56",
		"52-54-00-12-34-56": "52:54:00:12:34:56",
		"5254.0012.3456":    "52:54:00:12:34:56",
		" 525400123456 ":    "52:54:00:12:34:56",
	} {
		got, err := ParseMAC(in)
		if err != nil || got != want {
			t.Errorf("ParseMAC(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "52:54:00:12:34", "zz:54:00:12:34:56", "00:00:00:00:fe:80:00:00:00:00:00:00:02:00:5e:10:00:00:00:01"} {
		if _, err := ParseMAC(in); err == nil {
			t.Errorf("ParseMAC(%q) succeeded, want error", in)
		}
	}
}

func TestMagicPacket(t *testing.T) {
	p, err := MagicPacket("01:02:03:04:05:06")
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte{0xFF}, 6)
	for range 16 {
		want = append(want, 1, 2, 3, 4, 5, 6)
	}
	if !bytes.Equal(p, want) {
		t.Errorf("packet = %x, want %x", p, want)
	}
}

func TestWakeSendsPacket(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	port := conn.LocalAddr().(*net.UDPAddr).Port

	if err := Wake("01:02:03:04:05:06", "127.0.0.1", port); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 200)
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := MagicPacket("01:02:03:04:05:06")
	if !bytes.Equal(buf[:n], want) {
		t.Errorf("received %x", buf[:n])
	}
}

func TestOnline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if !Online(context.Background(), addr) {
		t.Error("listening port reported offline")
	}
	ln.Close()
	// Nothing listens now, but the host still answers with a refusal.
	if !Online(context.Background(), addr) {
		t.Error("refused port reported offline")
	}
	// TEST-NET-1 is unroutable, so the probe times out.
	if Online(context.Background(), "192.0.2.1:9") {
		t.Error("unroutable host reported online")
	}
}

func TestStorePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "devices.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Create(Device{Name: "nas", MAC: "525400abcd01"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Broadcast != "255.255.255.255" || d.Port != 9 || d.MAC != "52:54:00:AB:CD:01" {
		t.Errorf("defaults not applied: %+v", d)
	}
	if _, err := s.Create(Device{Name: "NAS", MAC: "52:54:00:AB:CD:02"}); err == nil {
		t.Error("duplicate name accepted")
	}
	if _, err := s.Update(d.ID, Device{Name: "nas2", MAC: d.MAC, Port: 7}); err != nil {
		t.Fatal(err)
	}

	s2, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.Get("NAS2")
	if err != nil || got.ID != d.ID || got.Port != 7 {
		t.Errorf("reloaded = %+v, %v", got, err)
	}
	if err := s2.Delete(d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s2.Delete(d.ID); err != ErrNotFound {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
}

func TestAPI(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "devices.json"))
	var woke []string
	srv := NewServer(store)
	srv.wake = func(mac, broadcast string, port int) error { woke = append(woke, mac); return nil }
	srv.online = func(ctx context.Context, host string) bool { return host == "up" }
	h := srv.Handler()

	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := do("POST", "/api/devices", `{"name":"nas","mac":"52:54:00:ab:cd:01","host":"up"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var d Device
	json.NewDecoder(rec.Body).Decode(&d)

	if rec := do("POST", "/api/devices", `{"name":"bad","mac":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid create: %d", rec.Code)
	}
	if rec := do("POST", "/api/devices/nas/wake", ""); rec.Code != http.StatusOK || len(woke) != 1 {
		t.Errorf("wake by name: %d, woke %v", rec.Code, woke)
	}
	if rec := do("POST", "/api/devices/missing/wake", ""); rec.Code != http.StatusNotFound {
		t.Errorf("wake missing: %d", rec.Code)
	}

	rec = do("GET", "/api/status", "")
	var status map[string]bool
	json.NewDecoder(rec.Body).Decode(&status)
	if !status[d.ID] {
		t.Errorf("status = %v", status)
	}

	if rec := do("PUT", "/api/devices/"+d.ID, `{"name":"nas","mac":"52:54:00:ab:cd:01","port":7}`); rec.Code != http.StatusOK {
		t.Errorf("update: %d %s", rec.Code, rec.Body)
	}
	if rec := do("DELETE", "/api/devices/"+d.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec := do("GET", "/", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Wake on LAN") {
		t.Errorf("index: %d", rec.Code)
	}
}
