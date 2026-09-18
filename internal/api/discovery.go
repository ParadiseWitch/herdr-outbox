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
	if pid <= 0 {
		return false
	}
	return processAlive(pid)
}

// Stop asks the server to exit and waits until it really is gone.
//
// The HTTP reply alone proves nothing: a shutdown that wedges on a stuck handler
// still answers 200, removes the discovery files and then keeps ticking. So the
// PID from the discovery file is escalated to a direct signal if the process
// outlives the graceful path.
func Stop(dir string) error {
	port, pid, err := ReadDiscovery(dir)
	if err != nil {
		return fmt.Errorf("没有可停止的服务: %w", err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if resp, perr := http.Post(base+"/api/server/stop", "", nil); perr == nil {
		resp.Body.Close()
	}
	if waitGone(port, pid, 8*time.Second) {
		return nil
	}
	if terr := terminate(pid); terr != nil {
		return fmt.Errorf("服务进程 %d 仍在运行，强制停止失败: %w", pid, terr)
	}
	if waitGone(port, pid, 5*time.Second) {
		return nil
	}
	return fmt.Errorf("服务进程 %d 仍在运行", pid)
}

// waitGone needs both the listener and the process to disappear. A closed port
// on its own can just mean the graceful drain finished while the scheduler loop
// kept running.
func waitGone(port, pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !listening(port) && !ProcessAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func listening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
