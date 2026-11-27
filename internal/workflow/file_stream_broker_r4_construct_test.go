package workflow

// Round-4 characterization of NewFileServerBackupTransferBroker: every limit,
// every rejection message, and the order in which they are checked.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const (
	brokerR4ErrConfig    = "private staging path, bounded operation bytes, transfer lifetime (1ns..5m), and longer artifact lifetime are required"
	brokerR4ErrRealDir   = "stream staging path must be a real directory"
	brokerR4ErrPrivate   = "stream staging directory must not be accessible by group or other users"
	brokerR4MaxSafeBytes = int64(9007199254740991)
)

// brokerR4Config is one set of constructor arguments.
type brokerR4Config struct {
	root     string
	maxBytes int64
	transfer time.Duration
	artifact time.Duration
}

func brokerR4ValidConfig(t *testing.T) brokerR4Config {
	t.Helper()
	return brokerR4Config{root: filepath.Join(t.TempDir(), "staging"), maxBytes: 16, transfer: time.Minute, artifact: 10 * time.Minute}
}

func (c brokerR4Config) build() (*FileServerBackupTransferBroker, error) {
	return NewFileServerBackupTransferBroker(c.root, c.maxBytes, c.transfer, c.artifact)
}

func brokerR4RequireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("test requires Linux filesystem permission semantics")
	}
}

func TestBrokerR4ConstructorLimits(t *testing.T) {
	brokerR4RequireLinux(t)
	cases := []struct {
		name   string
		mutate func(*brokerR4Config)
		want   string
	}{
		{"valid", func(*brokerR4Config) {}, ""},
		{"empty root", func(c *brokerR4Config) { c.root = "" }, brokerR4ErrConfig},
		{"zero bytes", func(c *brokerR4Config) { c.maxBytes = 0 }, brokerR4ErrConfig},
		{"negative bytes", func(c *brokerR4Config) { c.maxBytes = -1 }, brokerR4ErrConfig},
		{"one byte", func(c *brokerR4Config) { c.maxBytes = 1 }, ""},
		{"largest safe integer", func(c *brokerR4Config) { c.maxBytes = brokerR4MaxSafeBytes }, ""},
		{"above safe integer", func(c *brokerR4Config) { c.maxBytes = brokerR4MaxSafeBytes + 1 }, brokerR4ErrConfig},
		{"zero transfer lifetime", func(c *brokerR4Config) { c.transfer = 0 }, brokerR4ErrConfig},
		{"negative transfer lifetime", func(c *brokerR4Config) { c.transfer = -time.Second }, brokerR4ErrConfig},
		{"one nanosecond transfer", func(c *brokerR4Config) { c.transfer = time.Nanosecond }, ""},
		{"five minute transfer", func(c *brokerR4Config) { c.transfer = 5 * time.Minute }, ""},
		{"above five minutes", func(c *brokerR4Config) { c.transfer = 5*time.Minute + 1 }, brokerR4ErrConfig},
		{"artifact equals transfer", func(c *brokerR4Config) { c.artifact = c.transfer }, ""},
		{"artifact below transfer", func(c *brokerR4Config) { c.artifact = c.transfer - 1 }, brokerR4ErrConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := brokerR4ValidConfig(t)
			tc.mutate(&config)
			broker, err := config.build()
			brokerR3WantResult(t, err, tc.want)
			if tc.want != "" {
				if broker != nil {
					t.Fatalf("broker = %+v, want nil", broker)
				}
				brokerR3Exists(t, config.root, false)
				return
			}
			brokerR4WantBroker(t, broker, config)
		})
	}
}

func brokerR4WantBroker(t *testing.T, broker *FileServerBackupTransferBroker, config brokerR4Config) {
	t.Helper()
	if broker == nil {
		t.Fatal("broker = nil, want a broker")
	}
	got := brokerR4Config{root: broker.root, maxBytes: broker.maxStagingBytes, transfer: broker.transferLifetime, artifact: broker.artifactLifetime}
	if got != config {
		t.Fatalf("broker = %+v, want %+v", got, config)
	}
	info, err := os.Lstat(config.root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("staging root = %v, %v; want a 0700 directory", info, err)
	}
}

func TestBrokerR4ConstructorChecksConfigBeforeTouchingTheDisk(t *testing.T) {
	brokerR4RequireLinux(t)
	config := brokerR4ValidConfig(t)
	brokerR3Write(t, config.root, []byte("not a directory"))
	config.maxBytes = 0
	_, err := config.build()
	brokerR3WantErr(t, err, brokerR4ErrConfig)
}

func TestBrokerR4ConstructorFilesystemRejections(t *testing.T) {
	brokerR4RequireLinux(t)
	t.Run("root is a file", func(t *testing.T) {
		config := brokerR4ValidConfig(t)
		brokerR3Write(t, config.root, []byte("file"))
		brokerR4WantMkdirErr(t, config)
	})
	t.Run("parent is a file", func(t *testing.T) {
		config := brokerR4ValidConfig(t)
		brokerR3Write(t, config.root, []byte("file"))
		config.root = filepath.Join(config.root, "child")
		brokerR4WantMkdirErr(t, config)
	})
	t.Run("root is a symlink", func(t *testing.T) {
		config := brokerR4ValidConfig(t)
		target := filepath.Join(filepath.Dir(config.root), "target")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, config.root); err != nil {
			t.Fatal(err)
		}
		_, err := config.build()
		brokerR3WantErr(t, err, brokerR4ErrRealDir)
	})
	for _, mode := range []os.FileMode{0710, 0701, 0750, 0705, 0755} {
		t.Run(fmt.Sprintf("mode %o", mode), func(t *testing.T) {
			config := brokerR4ValidConfig(t)
			brokerR4MkdirMode(t, config.root, mode)
			_, err := config.build()
			brokerR3WantErr(t, err, brokerR4ErrPrivate)
		})
	}
	t.Run("existing private directory", func(t *testing.T) {
		config := brokerR4ValidConfig(t)
		brokerR4MkdirMode(t, config.root, 0700)
		broker, err := config.build()
		brokerR3WantResult(t, err, "")
		brokerR4WantBroker(t, broker, config)
	})
}

func TestBrokerR4ConstructorMakesRootAbsolute(t *testing.T) {
	brokerR4RequireLinux(t)
	dir := t.TempDir()
	t.Chdir(dir)
	broker, err := NewFileServerBackupTransferBroker("relative", 16, time.Minute, time.Minute)
	brokerR3WantResult(t, err, "")
	want, err := filepath.Abs("relative")
	if err != nil {
		t.Fatal(err)
	}
	if broker.root != want || !filepath.IsAbs(broker.root) {
		t.Fatalf("root = %q, want %q", broker.root, want)
	}
}

// brokerR4WantMkdirErr pins the wrapped MkdirAll error for an unusable root.
func brokerR4WantMkdirErr(t *testing.T, config brokerR4Config) {
	t.Helper()
	wantErr := os.MkdirAll(config.root, 0700)
	if wantErr == nil {
		t.Fatal("MkdirAll unexpectedly succeeded")
	}
	_, err := config.build()
	brokerR3WantErr(t, err, "create private stream staging path: "+wantErr.Error())
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("error %v does not wrap the MkdirAll path error", err)
	}
}

func brokerR4MkdirMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
