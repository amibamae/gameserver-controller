package capturestage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/captureinvoke"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
)

func testScope() capturestream.Scope {
	return capturestream.Scope{OperationID: "op", StepID: "capture", AdapterID: "game.paper", ServerID: "server", EnvironmentID: "test", InstanceAdapterID: "hosting.fake", InstanceID: "vm", DataGenerationID: "generation", DataSetID: "world", CaptureID: "capture-id"}
}
func testReceipt(scope capturestream.Scope, data string) capturestream.Receipt {
	h := sha256.Sum256([]byte(data))
	return capturestream.Receipt{Scope: scope, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(h[:])}
}
func newFiles(t *testing.T) *Files {
	t.Helper()
	f, err := New(filepath.Join(t.TempDir(), "staging"))
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func TestSealSurvivesReopenAndVerifiesBytes(t *testing.T) {
	f := newFiles(t)
	scope := testScope()
	s, err := f.Open(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(s, "abc"); err != nil {
		t.Fatal(err)
	}
	receipt := testReceipt(scope, "abc")
	if err = s.Seal(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(f.root)
	if err != nil {
		t.Fatal(err)
	}
	file, got, err := reopened.OpenVerified(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "abc" || got != receipt {
		t.Fatalf("reopened bytes=%q receipt=%#v error=%v", data, got, err)
	}
	info, _ := file.Stat()
	if info.Mode().Perm() != 0400 {
		t.Fatal("data permissions are not private/read-only")
	}
	if _, err = f.Open(context.Background(), scope); !errors.Is(err, captureinvoke.ErrStagingNeedsReconciliation) {
		t.Fatal("sealed capture could be overwritten")
	}
	if err = s.Discard(); !errors.Is(err, captureinvoke.ErrStagingNeedsReconciliation) {
		t.Fatal("published staging could be discarded")
	}
}
func TestCompetingSealsNeverReplacePublishedCapture(t *testing.T) {
	f := newFiles(t)
	scope := testScope()
	first, _ := f.Open(context.Background(), scope)
	second, _ := f.Open(context.Background(), scope)
	_, _ = io.WriteString(first, "first")
	_, _ = io.WriteString(second, "second")
	if err := first.Seal(context.Background(), testReceipt(scope, "first")); err != nil {
		t.Fatal(err)
	}
	if err := second.Seal(context.Background(), testReceipt(scope, "second")); !errors.Is(err, captureinvoke.ErrStagingNeedsReconciliation) {
		t.Fatal(err)
	}
	if err := second.Discard(); err != nil {
		t.Fatal(err)
	}
	file, _, err := f.OpenVerified(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, _ := io.ReadAll(file)
	if string(data) != "first" {
		t.Fatal("published capture replaced")
	}
}
func TestUnsealedFailedOrCanceledStagesHaveNoEvidence(t *testing.T) {
	for _, scenario := range []string{"partial", "receipt", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFiles(t)
			scope := testScope()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, _ := f.Open(ctx, scope)
			_, _ = io.WriteString(s, "abc")
			if scenario == "receipt" {
				if err := s.Seal(ctx, testReceipt(scope, "wrong")); err == nil {
					t.Fatal("mismatched receipt sealed")
				}
			}
			if scenario == "canceled" {
				cancel()
				if err := s.Seal(ctx, testReceipt(scope, "abc")); err == nil {
					t.Fatal("canceled capture sealed")
				}
			}
			if _, _, err := f.OpenVerified(context.Background(), scope); err == nil {
				t.Fatal("unsealed data became evidence")
			}
			if err := s.Discard(); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Write([]byte("late")); err == nil {
				t.Fatal("discarded stage accepted writes")
			}
			entries, _ := os.ReadDir(f.root)
			if len(entries) != 0 {
				t.Fatal("failed stage was not cleaned up")
			}
		})
	}
}
func TestCorruptionAndSymlinksFailClosed(t *testing.T) {
	for _, scenario := range []string{"data", "metadata", "directory", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFiles(t)
			scope := testScope()
			s, _ := f.Open(context.Background(), scope)
			_, _ = io.WriteString(s, "abc")
			if err := s.Seal(context.Background(), testReceipt(scope, "abc")); err != nil {
				t.Fatal(err)
			}
			local := s.(*stage)
			name, _ := key(scope)
			switch scenario {
			case "data":
				_ = os.Chmod(filepath.Join(local.dir, "data"), 0600)
				_ = os.WriteFile(filepath.Join(local.dir, "data"), []byte("bad"), 0600)
			case "metadata":
				_ = os.WriteFile(filepath.Join(f.root, name), []byte(`{"Version":999}`), 0600)
			case "directory":
				encoded, _ := json.Marshal(manifest{Version: 1, Directory: "../outside", Receipt: testReceipt(scope, "abc")})
				_ = os.WriteFile(filepath.Join(f.root, name), encoded, 0600)
			case "symlink":
				outside := filepath.Join(t.TempDir(), "outside")
				_ = os.WriteFile(outside, []byte("abc"), 0600)
				_ = os.Remove(filepath.Join(local.dir, "data"))
				if err := os.Symlink(outside, filepath.Join(local.dir, "data")); err != nil {
					t.Fatal(err)
				}
			}
			if file, _, err := f.OpenVerified(context.Background(), scope); err == nil {
				file.Close()
				t.Fatal("corrupt staging accepted")
			}
		})
	}
}
func TestRootMustBePrivateAndNotSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root); err == nil {
		t.Fatal("public staging root accepted")
	}
	private := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(link); err == nil {
		t.Fatal("symlink staging root accepted")
	}
}

var _ captureinvoke.Staging = (*Files)(nil)

func TestPublicationSyncFailurePreservesCaptureForReconciliation(t *testing.T) {
	f := newFiles(t)
	scope := testScope()
	s, err := f.Open(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(s, "abc")
	f.syncRoot = func() error { return errors.New("simulated publication sync failure") }
	if err := s.Seal(context.Background(), testReceipt(scope, "abc")); !errors.Is(err, captureinvoke.ErrStagingNeedsReconciliation) {
		t.Fatal("publication uncertainty was hidden")
	}
	if err := s.Discard(); !errors.Is(err, captureinvoke.ErrStagingNeedsReconciliation) {
		t.Fatal("uncertain published data was deleted")
	}
	reopened, err := New(f.root)
	if err != nil {
		t.Fatal(err)
	}
	file, _, err := reopened.OpenVerified(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
}
