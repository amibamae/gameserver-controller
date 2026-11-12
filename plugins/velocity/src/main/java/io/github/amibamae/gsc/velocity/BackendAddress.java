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
    HostPort parts = value.startsWith("[") ? bracketed(value) : plain(value);
    return new BackendAddress(parts.host(), port(parts.port()));
  }

  /** The two halves of an address, before the port is parsed. */
  private record HostPort(String host, String port) {}

  /** {@code [ipv6]:port}: the brackets must hold an IPv6 literal. */
  private static HostPort bracketed(String value) {
    int end = value.indexOf("]:");
    if (end < 0) {
      throw invalid();
    }
    String host = value.substring(1, end).toLowerCase(Locale.ROOT);
    if (!IPV6.matcher(host).matches() || !host.contains(":")) {
      throw invalid();
    }
    return new HostPort(host, value.substring(end + 2));
  }

  /**
   * {@code host:port} with exactly one colon. The host is an IPv4 literal or
   * a DNS hostname, lower-cased and without a trailing dot.
   */
  private static HostPort plain(String value) {
    int colon = value.lastIndexOf(':');
    if (colon <= 0 || value.indexOf(':') != colon) {
      throw invalid();
    }
    String host = value.substring(0, colon).toLowerCase(Locale.ROOT);
    if (host.endsWith(".")) {
      host = host.substring(0, host.length() - 1);
    }
    boolean valid = host.matches("^[0-9.]+$")
        ? IPV4.matcher(host).matches()
        : HOSTNAME.matcher(host).matches() && host.length() <= 253;
    if (!valid) {
      throw invalid();
    }
    return new HostPort(host, value.substring(colon + 1));
  }

  /** A canonical decimal port from 1 to 65535: no sign or leading zeros. */
  private static int port(String text) {
    int port;
    try {
      port = Integer.parseInt(text);
    } catch (NumberFormatException e) {
      throw invalid();
    }
    if (port < 1 || port > 65535 || !text.equals(Integer.toString(port))) {
      throw invalid();
    }
    return port;
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
