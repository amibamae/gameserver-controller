package io.github.amibamae.gsc.velocity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import org.junit.jupiter.api.Test;

class BackendAddressTest {
  @Test
  void parsesIpHostnameAndIpv6() {
    assertEquals("203.0.113.20:25565", BackendAddress.parse("203.0.113.20:25565").toString());
    assertEquals("paper-main.backends.example.com:25565", BackendAddress.parse("Paper-Main.Backends.Example.com.:25565").toString());
    assertEquals("[2001:db8::1]:25565", BackendAddress.parse("[2001:db8::1]:25565").toString());
  }

  @Test
  void keepsHostnamesUnresolvedSoDnsChangesApply() {
    assertTrue(BackendAddress.parse("paper-main.backends.example.com:25565").toUnresolved().isUnresolved());
  }

  @Test
  void rejectsInvalidAddresses() {
    for (String value : new String[] {"", "203.0.113.20", "203.0.113.20:0", "203.0.113.20:70000", "bad_host.example.com:25565",
        "2001:db8::1:25565", "[not-v6]:25565", "host:25565:1", "999.1.1.1:25565", "example.com:+25565", "localhost:25565"}) {
      assertThrows(BridgeException.class, () -> BackendAddress.parse(value), value);
    }
  }
}
