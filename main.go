package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "check the local server is healthy and exit (for Docker HEALTHCHECK)")
	flag.Parse()

	port := cmp.Or(os.Getenv("PORT"), "8090")
	if *healthcheck {
		os.Exit(checkHealth(port))
	}

	dataFile := cmp.Or(os.Getenv("DATA_FILE"), "data/devices.json")
	store, err := NewStore(dataFile)
	if err != nil {
		slog.Error("failed to load devices", "file", dataFile, "error", err)
		os.Exit(1)
	}

	slog.Info("listening", "port", port, "data", dataFile)
	if err := http.ListenAndServe(":"+port, NewServer(store).Handler()); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

// checkHealth returns the process exit code for -healthcheck.
func checkHealth(port string) int {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

type Server struct {
	store *Store
	// wake is swapped out in tests.
	wake func(mac, broadcast string, port int) error
	// online is swapped out in tests.
	online func(ctx context.Context, host string) bool
}

func NewServer(store *Store) *Server {
	return &Server{store: store, wake: Wake, online: Online}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(StaticFS()))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/devices", s.list)
	mux.HandleFunc("POST /api/devices", s.create)
	mux.HandleFunc("PUT /api/devices/{id}", s.update)
	mux.HandleFunc("DELETE /api/devices/{id}", s.delete)
	mux.HandleFunc("POST /api/devices/{id}/wake", s.wakeDevice)
	mux.HandleFunc("GET /api/status", s.status)
	return mux
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.List())
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var d Device
	if !readJSON(w, r, &d) {
		return
	}
	d, err := s.store.Create(d)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) update(w http.ResponseWriter, r *http.Request) {
	var d Device
	if !readJSON(w, r, &d) {
		return
	}
	d, err := s.store.Update(r.PathValue("id"), d)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// wakeDevice accepts a device ID or name.
func (s *Server) wakeDevice(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.wake(d.MAC, d.Broadcast, d.Port); err != nil {
		slog.Error("wake failed", "device", d.Name, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "send magic packet: " + err.Error()})
		return
	}
	slog.Info("magic packet sent", "device", d.Name, "mac", d.MAC, "broadcast", d.Broadcast, "port", d.Port)
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// status probes every device that has a host and returns {id: online}.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	result := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, d := range s.store.List() {
		if d.Host == "" {
			continue
		}
		wg.Go(func() {
			up := s.online(r.Context(), d.Host)
			mu.Lock()
			result[d.ID] = up
			mu.Unlock()
		})
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, result)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, errInvalid):
		code = http.StatusBadRequest
	default:
		slog.Error("request failed", "error", err)
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
