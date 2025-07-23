package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Manager struct {
	pidFile string
	refFile string
	mu      sync.RWMutex
}

func NewManager(baseDir string) *Manager {
	// Determine which PID filename to use based on baseDir
	pidFilename := ".claude-code-open.pid"
	if strings.Contains(baseDir, "claude-code-router") {
		pidFilename = ".claude-code-router.pid"
	}

	return &Manager{
		pidFile: filepath.Join(baseDir, pidFilename),
		refFile: filepath.Join(os.TempDir(), "claude-code-reference-count.txt"),
	}
}

func (m *Manager) WritePID() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(m.pidFile), 0750); err != nil {
		return fmt.Errorf("create pid directory: %w", err)
	}

	pid := strconv.Itoa(os.Getpid())

	return os.WriteFile(m.pidFile, []byte(pid), 0600)
}

func (m *Manager) ReadPID() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	data, err := os.ReadFile(m.pidFile)
	if err != nil {
		return 0
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0 // Invalid PID format
	}

	return pid
}

func (m *Manager) IsRunning() bool {
	pid := m.ReadPID()
	if pid == 0 {
		return false
	}

	if err := syscall.Kill(pid, 0); err != nil {
		m.CleanupPID()
		return false
	}

	return true
}

func (m *Manager) Stop() error {
	pid := m.ReadPID()
	if pid == 0 {
		return nil
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to send SIGTERM to process %d: %w", pid, err)
	}

	// Wait for process to exit
	for i := 0; i < 50; i++ { // 5 seconds timeout
		if !m.IsRunning() {
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	m.CleanupPID()

	return nil
}

func (m *Manager) CleanupPID() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.Remove(m.pidFile); err != nil && !os.IsNotExist(err) {
		// Log error only if file exists but can't be removed
		fmt.Printf("Warning: failed to remove PID file: %v\n", err)
	}
}

func (m *Manager) IncrementRef() {
	m.writeRef(m.ReadRef() + 1)
}

func (m *Manager) DecrementRef() {
	if c := m.ReadRef(); c > 0 {
		m.writeRef(c - 1)
	}
}

func (m *Manager) ReadRef() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	data, err := os.ReadFile(m.refFile)
	if err != nil {
		return 0
	}

	count, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0 // Invalid count format
	}

	return count
}

func (m *Manager) writeRef(count int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.WriteFile(m.refFile, []byte(strconv.Itoa(count)), 0600); err != nil {
		fmt.Printf("Warning: failed to write reference file: %v\n", err)
	}
}

func (m *Manager) CleanupRef() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.Remove(m.refFile); err != nil && !os.IsNotExist(err) {
		fmt.Printf("Warning: failed to remove reference file: %v\n", err)
	}
}

func (m *Manager) WaitForService(timeout time.Duration) bool {
	expire := time.Now().Add(timeout)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for time.Now().Before(expire) {
		if m.IsRunning() {
			return true
		}

		<-ticker.C
	}

	return false
}

func (m *Manager) StartServiceIfNeeded() (bool, error) {
	if m.IsRunning() {
		return false, nil // Service was already running
	}

	// Start service in background
	cmd := exec.Command(os.Args[0], "start")
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("failed to start service: %w", err)
	}

	// Wait for service to be ready
	if !m.WaitForService(10 * time.Second) {
		return false, errors.New("service startup timeout")
	}

	return true, nil // Service was started by us
}
