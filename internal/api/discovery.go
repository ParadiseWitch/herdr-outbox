package api

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	PortFile = ".server-port"
	PIDFile  = ".server-pid"
)

func WriteDiscovery(dir string, port int) error {
	if err := os.WriteFile(filepath.Join(dir, PortFile), []byte(strconv.Itoa(port)), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, PIDFile), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

func RemoveDiscovery(dir string) {
	os.Remove(filepath.Join(dir, PortFile))
	os.Remove(filepath.Join(dir, PIDFile))
}

func ReadDiscovery(dir string) (port int, pid int, err error) {
	portB, err := os.ReadFile(filepath.Join(dir, PortFile))
	if err != nil {
		return 0, 0, err
	}
	port, err = strconv.Atoi(strings.TrimSpace(string(portB)))
	if err != nil {
		return 0, 0, fmt.Errorf("bad port file: %w", err)
	}
	pidB, err := os.ReadFile(filepath.Join(dir, PIDFile))
	if err != nil {
		return 0, 0, err
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(pidB)))
	if err != nil {
		return 0, 0, fmt.Errorf("bad pid file: %w", err)
	}
	return port, pid, nil
}

// IsRunning checks whether a server is already listening on the discovered port.
func IsRunning(dir string) bool {
	port, _, err := ReadDiscovery(dir)
	if err != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// BaseURL returns the server URL from the discovery file.
func BaseURL(dir string) (string, error) {
	port, _, err := ReadDiscovery(dir)
	if err != nil {
		return "", fmt.Errorf("server not running (no discovery files in %s)", dir)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), nil
}

// ProcessAlive reports whether a process with the given PID exists.
func ProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p != nil
}

// Stop sends a request to the server's stop endpoint.
func Stop(dir string) error {
	base, err := BaseURL(dir)
	if err != nil {
		return err
	}
	resp, err := http.Post(base+"/api/server/stop", "", nil)
	if err != nil {
		return fmt.Errorf("server not responding: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %s", resp.Status)
	}
	return nil
}
