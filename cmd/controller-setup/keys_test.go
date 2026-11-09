package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestRunKeyIssueShowsTokenOnceAndRevokesOnlyAfterExactConfirmation(t *testing.T) {
	database := createExistingDatabase(t)
	var stdout, stderr bytes.Buffer
	args := []string{
		"issue", "--database", database, "--actor", "automation", "--audience", "controller-test",
		"--expires-in", "24h", "--scope", "server.inspect:staging:test-server",
	}
	if err := runKeyCommand(args, &stdout, &stderr, true, 1001); err != nil {
		t.Fatalf("issue: %v; stderr=%s", err, stderr.String())
	}
	var issued struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &issued); err != nil {
		t.Fatalf("decode issue output: %v", err)
	}
	if issued.ID == "" || !strings.HasPrefix(issued.Token, "gsc1_") {
		t.Fatalf("issue output missing key metadata or one-time token: %#v", issued)
	}
	if strings.Count(stdout.String(), issued.Token) != 1 || strings.Contains(stderr.String(), issued.Token) {
		t.Fatalf("key output invariant failed: stdout token occurrences=%d, stderr contains token=%v", strings.Count(stdout.String(), issued.Token), strings.Contains(stderr.String(), issued.Token))
	}

	stdout.Reset()
	stderr.Reset()
	if err := runKeyCommand([]string{"revoke", "--database", database, "--key-id", issued.ID}, &stdout, &stderr, true, 1001); err == nil {
		t.Fatal("revocation without exact confirmation succeeded")
	}
	if stdout.Len() != 0 {
		t.Fatalf("unconfirmed revocation produced output: %q", stdout.String())
	}
	if err := runKeyCommand([]string{"revoke", "--database", database, "--key-id", issued.ID, "--confirm-key-id", issued.ID}, &stdout, &stderr, true, 1001); err != nil {
		t.Fatalf("confirmed revoke: %v; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "API key revoked") || strings.Contains(stdout.String(), issued.Token) {
		t.Fatalf("unexpected revoke output: %q", stdout.String())
	}
}

func TestRunKeyCommandRequiresRootAndPrivateExistingDatabase(t *testing.T) {
	var stdout, stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "missing.db")
	args := []string{"issue", "--database", missing, "--actor", "automation", "--audience", "controller-test", "--expires-in", "24h", "--scope", "server.inspect:staging:test-server"}
	if err := runKeyCommand(args, &stdout, &stderr, false, 1001); err == nil || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("non-root key administration was not rejected: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("command created a database before authorization: stat error=%v", err)
	}

	database := createExistingDatabase(t)
	if err := os.Chmod(database, 0o640); err != nil {
		t.Fatal(err)
	}
	databaseArgs := []string{"issue", "--database", database, "--actor", "automation", "--audience", "controller-test", "--expires-in", "24h", "--scope", "server.inspect:staging:test-server"}
	if err := runKeyCommand(databaseArgs, &stdout, &stderr, true, 0); err == nil || !strings.Contains(err.Error(), "group or world permissions") {
		t.Fatalf("database with broad permissions was accepted: %v", err)
	}
	if err := os.Remove(database); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(createExistingDatabase(t), database); err != nil {
		t.Fatal(err)
	}
	if err := runKeyCommand(databaseArgs, &stdout, &stderr, true, 0); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink database was accepted: %v", err)
	}

	sharedDirectoryDatabase := createExistingDatabase(t)
	if err := os.Chmod(filepath.Dir(sharedDirectoryDatabase), 0o777); err != nil {
		t.Fatal(err)
	}
	sharedDirectoryArgs := []string{"issue", "--database", sharedDirectoryDatabase, "--actor", "automation", "--audience", "controller-test", "--expires-in", "24h", "--scope", "server.inspect:staging:test-server"}
	if err := runKeyCommand(sharedDirectoryArgs, &stdout, &stderr, true, 0); err == nil || !strings.Contains(err.Error(), "directory must not be writable") {
		t.Fatalf("database in a group/world-writable directory was accepted: %v", err)
	}
}

func TestRunKeyIssueRejectsInvalidScopeAndLifetimeBeforeCreatingKey(t *testing.T) {
	database := createExistingDatabase(t)
	for _, extra := range [][]string{
		{"--expires-in", "91d"},
		{"--expires-in", "24h", "--scope", "server.inspect:*:test-server"},
		{"--expires-in", "24h", "--scope", "server.inspect:staging:test-server", "--scope", "server.inspect:staging:test-server"},
	} {
		args := []string{"issue", "--database", database, "--actor", "automation", "--audience", "controller-test", "--scope", "server.inspect:staging:test-server"}
		args = append(args, extra...)
		var stdout, stderr bytes.Buffer
		if err := runKeyCommand(args, &stdout, &stderr, true, 0); err == nil {
			t.Fatalf("invalid issuance arguments were accepted: %v", extra)
		}
		if stdout.Len() != 0 {
			t.Fatalf("invalid issuance exposed key output: %q", stdout.String())
		}
	}
}

func createExistingDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "controller.db")
	db, err := store.OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
