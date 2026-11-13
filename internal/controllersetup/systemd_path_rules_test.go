package controllersetup

import "testing"

// TestSystemdSafePathCharacters pins which paths can be written into a unit
// file without quoting.
func TestSystemdSafePathCharacters(t *testing.T) {
	for value, want := range map[string]bool{
		"/etc/gameserver-controller/controller.yaml": true, "/var/lib/gsc_2/state.db": true, "": true, "relative/Path-1.0": true,
		"/etc/game server": false, "/etc/gsc;rm": false, "/etc/$HOME": false, "/etc/%i": false, "/etc/é": false, "/etc/a\nb": false, `/etc/a"b`: false,
	} {
		if got := systemdSafePath(value); got != want {
			t.Errorf("systemdSafePath(%q) = %t, want %t", value, got, want)
		}
	}
}
