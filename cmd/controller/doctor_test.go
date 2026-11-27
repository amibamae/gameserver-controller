package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunDoctorCLI(t *testing.T) {
	example := filepath.Join("..", "..", "configs", "controller.example.yaml")
	dir := t.TempDir()
	badConfig := filepath.Join(dir, "invalid.yaml")
	if err := os.WriteFile(badConfig, []byte("unexpected: secret-marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("CONFIG_PATH", example)
	for _, tc := range []struct {
		name, path, wantCode, wantError, wantMessage string
	}{
		{"environment fallback", "", "ok", "", "Configuration passed validation; credentials and services were not tested"},
		{"explicit path", example, "ok", "", "Configuration passed validation; credentials and services were not tested"},
		{"invalid configuration", badConfig, "error", "doctor found errors", "Configuration could not be loaded and validated; check it with config-check"},
		{"missing configuration", filepath.Join(dir, "missing"), "error", "doctor found errors", "Configuration file does not exist"},
		{"directory instead of file", dir, "error", "doctor found errors", "Configuration path is not a regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"doctor"}
			if tc.path != "" {
				args = append(args, "--config", tc.path)
			}
			results := runDoctorTest(t, args, tc.wantError)
			want := doctorResult{"configuration", tc.wantCode, tc.wantMessage}
			if len(results) != 2 || results[0] != want || results[1].Code != "info" {
				t.Fatalf("unexpected report: %+v", results)
			}
		})
	}
}

func TestRunDoctorArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"doctor", "--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Usage of controller doctor:") {
		t.Fatalf("unexpected help: stdout=%q stderr=%q", &stdout, &stderr)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"doctor", "--bad"}, "flag provided but not defined: -bad"},
		{[]string{"doctor", "unexpected"}, "doctor requires a nonempty configuration path and accepts no positional arguments"},
		{[]string{"doctor", "--config="}, "doctor requires a nonempty configuration path and accepts no positional arguments"},
	} {
		checkDoctorError(t, run(context.Background(), tc.args, io.Discard, io.Discard), tc.want)
	}
}

func TestRunDoctorCredentialDirectory(t *testing.T) {
	valid := filepath.Join("..", "..", "configs", "controller.example.yaml")
	args := []string{"doctor", "--config", valid}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "credential")
	if err := os.WriteFile(file, []byte("secret-marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	privateCode := "ok"
	privateMessage := "Credential directory has no group or other permission bits; no credential files were read"
	if runtime.GOOS == "windows" {
		privateCode = "info"
		privateMessage = "Credential directory exists; Windows access controls were not checked"
	}
	for _, tc := range []struct {
		path, code, wantError, message string
	}{
		{dir, privateCode, "", privateMessage},
		{file, "error", "doctor found errors", "CREDENTIALS_DIRECTORY is not a directory"},
		{filepath.Join(dir, "missing"), "error", "doctor found errors", "Credential directory does not exist"},
	} {
		t.Setenv("CREDENTIALS_DIRECTORY", tc.path)
		results := runDoctorTest(t, args, tc.wantError)
		if want := (doctorResult{"filesystem", tc.code, tc.message}); results[1] != want {
			t.Fatalf("credential check: got %+v, want %+v", results[1], want)
		}
	}
}

func TestDoctorCredentialStatErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fs.ErrPermission, "Credential directory cannot be inspected: permission denied"},
		{errors.New("stat failed"), "Credential directory could not be inspected"},
	} {
		stat := func(string) (fs.FileInfo, error) { return nil, tc.err }
		got := checkDoctorCredentials("test-dir", stat)
		if want := (doctorResult{"filesystem", "error", tc.want}); got != want {
			t.Fatalf("stat diagnostic: got %+v, want %+v", got, want)
		}
	}
}

func TestDoctorCredentialWarningIsReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	got := checkDoctorCredentials(dir, os.Stat)
	want := doctorResult{"filesystem", "warning", "Credential directory has group or other permission bits; review the service configuration"}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || info.Mode().Perm() != 0o750 {
		t.Fatalf("check got %+v, directory mode is %o", got, info.Mode().Perm())
	}
}

func TestDoctorReportWriteFailure(t *testing.T) {
	checkDoctorError(t, reportDoctor(failingDoctorWriter{}, nil), "write doctor results: io: read/write on closed pipe")
}

type failingDoctorWriter struct{}

func (failingDoctorWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func checkDoctorError(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" && err == nil {
		return
	}
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func runDoctorTest(t *testing.T, args []string, wantError string) []doctorResult {
	t.Helper()
	var stdout bytes.Buffer
	checkDoctorError(t, run(context.Background(), args, &stdout, io.Discard), wantError)
	if strings.Contains(stdout.String(), "secret-marker") {
		t.Fatal("doctor printed private fixture contents")
	}
	var results []doctorResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("invalid doctor JSON: %v", err)
	}
	return results
}
