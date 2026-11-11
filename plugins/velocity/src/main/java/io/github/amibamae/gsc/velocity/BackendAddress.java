package io.github.amibamae.gsc.velocity;

import java.net.InetSocketAddress;
import java.util.Locale;
import java.util.regex.Pattern;

/**
 * A backend's host and port. The host is an IP literal or a DNS hostname.
 * Velocity receives an unresolved address, so a hostname is looked up when
 * the proxy connects and follows DNS changes.
 */
record BackendAddress(String host, int port) {
  private static final Pattern HOSTNAME =
      Pattern.compile("^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$");
  private static final Pattern IPV4 = Pattern.compile("^(?:(?:25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)\\.){3}(?:25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)$");
  private static final Pattern IPV6 = Pattern.compile("^[0-9a-f:.]{2,45}$");

  BackendAddress {
    if (port < 1 || port > 65535) {
      throw new IllegalArgumentException("port out of range");
    }
  }

  /** Parses {@code host:port} or {@code [ipv6]:port}. */
  static BackendAddress parse(String value) {
    if (value == null || value.length() > 300) {
      throw invalid();
    }
    String host;
    String portText;
    if (value.startsWith("[")) {
      int end = value.indexOf("]:");
      if (end < 0) {
        throw invalid();
      }
      host = value.substring(1, end).toLowerCase(Locale.ROOT);
      portText = value.substring(end + 2);
      if (!IPV6.matcher(host).matches() || !host.contains(":")) {
        throw invalid();
      }
    } else {
      int colon = value.lastIndexOf(':');
      if (colon <= 0 || value.indexOf(':') != colon) {
        throw invalid();
      }
      host = value.substring(0, colon).toLowerCase(Locale.ROOT);
      if (host.endsWith(".")) {
        host = host.substring(0, host.length() - 1);
      }
      portText = value.substring(colon + 1);
      boolean numeric = host.matches("^[0-9.]+$");
      if (numeric ? !IPV4.matcher(host).matches() : !(HOSTNAME.matcher(host).matches() && host.length() <= 253)) {
        throw invalid();
      }
    }
    int port;
    try {
      port = Integer.parseInt(portText);
    } catch (NumberFormatException e) {
      throw invalid();
    }
    if (port < 1 || port > 65535 || !portText.equals(Integer.toString(port))) {
      throw invalid();
    }
    return new BackendAddress(host, port);
  }

  InetSocketAddress toUnresolved() {
    return InetSocketAddress.createUnresolved(host, port);
  }

  @Override
  public String toString() {
    return host.contains(":") ? "[" + host + "]:" + port : host + ":" + port;
  }

  private static BridgeException invalid() {
    return new BridgeException(400, "invalid_address", "Address must be host:port.");
  }
}
