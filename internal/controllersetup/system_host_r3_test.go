package controllersetup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// These characterization tests run SystemInstallerHost against fake getent,
// useradd, chown and systemctl programs placed first on PATH, and pin the
// exact commands run and the exact error for each outcome.

const setupR3FakeTool = `#!/bin/sh
name=${0##*/}
echo "$name $*" >> "$R3_LOG"
case "$name $1" in
"getent passwd") [ -n "$R3_PASSWD" ] || exit 2; printf '%s\n' "$R3_PASSWD"; exit 0;;
"getent group") [ -n "$R3_GROUP" ] || exit 2; printf '%s\n' "$R3_GROUP"; exit 0;;
esac
printf 'noise on stdout\n'
printf 'noise on stderr\n' >&2
exit "${R3_RC:-0}"
`

// setupR3FakeTools installs the fake programs and returns the call log path.
func setupR3FakeTools(t *testing.T, passwd, group, rc string) string {
	t.Helper()
	bin := t.TempDir()
	for _, name := range []string{"getent", "useradd", "chown", "systemctl"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(setupR3FakeTool), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("PATH", bin)
	t.Setenv("R3_LOG", log)
	t.Setenv("R3_PASSWD", passwd)
	t.Setenv("R3_GROUP", group)
	t.Setenv("R3_RC", rc)
	return log
}

func setupR3ReadLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func setupR3ErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestSetupR3SystemEnsureServiceAccount(t *testing.T) {
	const (
		name   = "gameserver-controller"
		home   = "/var/lib/gameserver-controller"
		good   = name + ":x:998:997::" + home + ":/usr/sbin/nologin"
		group  = name + ":x:997:"
		lookup = "getent passwd " + name + "\n"
		both   = lookup + "getent group " + name + "\n"
		create = lookup + "useradd --system --user-group --home-dir " + home + " --shell /usr/sbin/nologin " + name + "\n"
	)
	tests := []struct {
		name, passwd, group, rc string
		want, log               string
	}{
		{name: "matching account", passwd: good, group: group, log: both},
		{name: "matching account with surrounding space", passwd: "  " + good + "  ", group: group + " ", log: both},
		{name: "too few fields", passwd: name + ":x:998:997::" + home, group: group, want: "existing service account has unexpected identity", log: lookup},
		{name: "too many fields", passwd: good + ":extra", group: group, want: "existing service account has unexpected identity", log: lookup},
		{name: "another name", passwd: "other:x:998:997::" + home + ":/usr/sbin/nologin", group: group, want: "existing service account has unexpected identity", log: lookup},
		{name: "another home", passwd: name + ":x:998:997::/home/gsc:/usr/sbin/nologin", group: group, want: "existing service account has unexpected identity", log: lookup},
		{name: "login shell", passwd: name + ":x:998:997::" + home + ":/bin/bash", group: group, want: "existing service account has unexpected identity", log: lookup},
		{name: "root UID", passwd: name + ":x:0:997::" + home + ":/usr/sbin/nologin", group: group, want: "existing service account has invalid UID", log: lookup},
		{name: "non-numeric UID", passwd: name + ":x:abc:997::" + home + ":/usr/sbin/nologin", group: group, want: "existing service account has invalid UID", log: lookup},
		{name: "missing group", passwd: good, want: "existing service account group is unavailable", log: both},
		{name: "group with another name", passwd: good, group: "other:x:997:", want: "existing service account group does not match", log: both},
		{name: "group with another GID", passwd: good, group: name + ":x:996:", want: "existing service account group does not match", log: both},
		{name: "group with too few fields", passwd: good, group: name + ":x", want: "existing service account group does not match", log: both},
		{name: "new account", log: create},
		{name: "new account fails", rc: "9", want: "create controller service account", log: create},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log := setupR3FakeTools(t, test.passwd, test.group, test.rc)
			err := SystemInstallerHost{}.EnsureServiceAccount(context.Background(), name, home)
			if got := setupR3ErrorText(err); got != test.want {
				t.Fatalf("EnsureServiceAccount = %q, want %q", got, test.want)
			}
			if got := setupR3ReadLog(t, log); got != test.log {
				t.Fatalf("commands = %q, want %q", got, test.log)
			}
		})
	}
	t.Run("canceled context", func(t *testing.T) {
		log := setupR3FakeTools(t, good, group, "")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := SystemInstallerHost{}.EnsureServiceAccount(ctx, name, home)
		if got := setupR3ErrorText(err); got != "create controller service account" {
			t.Fatalf("EnsureServiceAccount = %q", got)
		}
		if got := setupR3ReadLog(t, log); got != "" {
			t.Fatalf("commands ran with a canceled context: %q", got)
		}
	})
}

func TestSetupR3SystemChown(t *testing.T) {
	tests := []struct {
		name, owner, group, rc string
		want, log              string
	}{
		{name: "success", owner: "root", group: "gameserver-controller", log: "chown -- root:gameserver-controller /etc/x\n"},
		{name: "failure", owner: "root", group: "root", rc: "1", want: "set controller file ownership", log: "chown -- root:root /etc/x\n"},
		{name: "invalid owner", owner: "Root", group: "root", want: "invalid installation file owner or group"},
		{name: "invalid group", owner: "root", group: "-root", want: "invalid installation file owner or group"},
		{name: "empty group", owner: "root", group: "", want: "invalid installation file owner or group"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log := setupR3FakeTools(t, "", "", test.rc)
			err := SystemInstallerHost{}.Chown(context.Background(), "/etc/x", test.owner, test.group)
			if got := setupR3ErrorText(err); got != test.want {
				t.Fatalf("Chown = %q, want %q", got, test.want)
			}
			if got := setupR3ReadLog(t, log); got != test.log {
				t.Fatalf("commands = %q, want %q", got, test.log)
			}
		})
	}
}

func TestSetupR3SystemDaemonReload(t *testing.T) {
	for rc, want := range map[string]string{"": "", "1": "reload systemd unit files"} {
		log := setupR3FakeTools(t, "", "", rc)
		err := SystemInstallerHost{}.DaemonReload(context.Background())
		if got := setupR3ErrorText(err); got != want {
			t.Fatalf("DaemonReload (rc %q) = %q, want %q", rc, got, want)
		}
		if got := setupR3ReadLog(t, log); got != "systemctl daemon-reload\n" {
			t.Fatalf("commands = %q", got)
		}
	}
}
