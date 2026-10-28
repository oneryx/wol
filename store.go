package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

type Device struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MAC       string `json:"mac"`
	Broadcast string `json:"broadcast"`
	Port      int    `json:"port"`
	// Host is optional; when set it is probed to show online status.
	Host string `json:"host,omitempty"`
}

var (
	ErrNotFound = errors.New("device not found")
	errInvalid  = errors.New("invalid device")
)

// Normalize validates d and fills in defaults. Returned errors wrap errInvalid.
func (d *Device) Normalize() error {
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		return fmt.Errorf("%w: name is required", errInvalid)
	}
	mac, err := ParseMAC(d.MAC)
	if err != nil {
		return fmt.Errorf("%w: %v", errInvalid, err)
	}
	d.MAC = mac

	d.Broadcast = strings.TrimSpace(d.Broadcast)
	if d.Broadcast == "" {
		d.Broadcast = "255.255.255.255"
	}
	if ip := net.ParseIP(d.Broadcast); ip == nil || ip.To4() == nil {
		return fmt.Errorf("%w: broadcast must be an IPv4 address", errInvalid)
	}

	if d.Port == 0 {
		d.Port = 9
	}
	if d.Port < 1 || d.Port > 65535 {
		return fmt.Errorf("%w: port must be 1-65535", errInvalid)
	}

	d.Host = strings.TrimSpace(d.Host)
	if strings.ContainsAny(d.Host, " /") {
		return fmt.Errorf("%w: host must be a hostname or IP, optionally with :port", errInvalid)
	}
	return nil
}

// Store keeps devices in a JSON file, rewritten atomically on every change.
type Store struct {
	mu      sync.RWMutex
	path    string
	devices []Device
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path, devices: []Device{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.devices); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) List() []Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.devices)
}

// Get finds a device by ID, or by name (case-insensitive) so scripts can use
// the friendly name.
func (s *Store) Get(key string) (Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if i := s.index(key); i >= 0 {
		return s.devices[i], nil
	}
	for _, d := range s.devices {
		if strings.EqualFold(d.Name, key) {
			return d, nil
		}
	}
	return Device{}, ErrNotFound
}

func (s *Store) Create(d Device) (Device, error) {
	if err := d.Normalize(); err != nil {
		return d, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkUnique(d); err != nil {
		return d, err
	}
	d.ID = newID()
	devices := append(slices.Clone(s.devices), d)
	return d, s.save(devices)
}

func (s *Store) Update(id string, d Device) (Device, error) {
	if err := d.Normalize(); err != nil {
		return d, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return d, ErrNotFound
	}
	d.ID = id
	if err := s.checkUnique(d); err != nil {
		return d, err
	}
	devices := slices.Clone(s.devices)
	devices[i] = d
	return d, s.save(devices)
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return ErrNotFound
	}
	return s.save(slices.Delete(slices.Clone(s.devices), i, i+1))
}

func (s *Store) index(id string) int {
	return slices.IndexFunc(s.devices, func(d Device) bool { return d.ID == id })
}

// checkUnique keeps names unique, since devices can be woken by name.
func (s *Store) checkUnique(d Device) error {
	for _, o := range s.devices {
		if o.ID != d.ID && strings.EqualFold(o.Name, d.Name) {
			return fmt.Errorf("%w: a device named %q already exists", errInvalid, o.Name)
		}
	}
	return nil
}

// save writes devices to disk and only then makes them current, so a failed
// write leaves memory and file in agreement.
func (s *Store) save(devices []Device) error {
	data, err := json.MarshalIndent(devices, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".devices-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	// CreateTemp makes the file 0600; let other host users (e.g. backups) read it.
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	s.devices = devices
	return nil
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
