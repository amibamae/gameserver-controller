package io.github.amibamae.gsc.velocity;

/**
 * A request the bridge refuses. The message is a fixed, safe string: it must
 * never contain session IDs or player data.
 */
final class BridgeException extends RuntimeException {
  private static final long serialVersionUID = 1L;

  final int status;
  final String code;

  BridgeException(int status, String code, String message) {
    super(message, null, false, false);
    this.status = status;
    this.code = code;
  }
}
