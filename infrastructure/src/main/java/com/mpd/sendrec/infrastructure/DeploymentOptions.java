package com.mpd.sendrec.infrastructure;

import java.io.IOException;
import java.io.UncheckedIOException;
import java.nio.file.Files;
import java.nio.file.Path;
import software.constructs.Construct;

/** Owner setup is opt-in and accepts only one explicit IPv4 address. */
final class DeploymentOptions {
  final boolean bootstrap;
  final String bootstrapIp;

  DeploymentOptions(Construct scope) {
    Object mode = scope.getNode().tryGetContext("bootstrap");
    if (mode != null && !"true".equals(mode.toString()) && !"false".equals(mode.toString())) {
      throw new IllegalArgumentException("bootstrap must be true or false");
    }
    bootstrap = mode != null && "true".equals(mode.toString());
    Object cidr = scope.getNode().tryGetContext("bootstrapCidr");
    String value = cidr == null ? "" : cidr.toString();
    if ((bootstrap || !value.isEmpty()) && !validHostCidr(value)) {
      throw new IllegalArgumentException("bootstrapCidr must be an explicit IPv4 /32 and is required for bootstrap");
    }
    // Keep the restriction during the first shutdown deploy. Remove it only after
    // the replacement task has demonstrably disabled registration.
    bootstrapIp = value.isEmpty() ? "" : value.substring(0, value.length() - 3);
  }

  private static boolean validHostCidr(String value) {
    if (!value.matches("(?:[0-9]{1,3}\\.){3}[0-9]{1,3}/32")) return false;
    String ip = value.substring(0, value.length() - 3);
    for (String part : ip.split("\\.")) {
      if (Integer.parseInt(part) > 255 || (part.length() > 1 && part.startsWith("0"))) return false;
    }
    return !ip.equals("0.0.0.0");
  }

  String accessCode() {
    try {
      return Files.readString(Path.of("bootstrap-access.js")).replace("__BOOTSTRAP_IP__", bootstrapIp);
    } catch (IOException error) {
      throw new UncheckedIOException(error);
    }
  }
}
