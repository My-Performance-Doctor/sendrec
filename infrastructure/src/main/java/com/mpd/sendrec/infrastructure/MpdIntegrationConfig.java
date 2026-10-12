package com.mpd.sendrec.infrastructure;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import software.amazon.awscdk.Stack;
import software.amazon.awscdk.services.ecs.Secret;

/** Settings are dormant until a separately reviewed activation supplies every value. */
final class MpdIntegrationConfig {
  private MpdIntegrationConfig() {}

  static void apply(Stack scope, Map<String, String> environment, Map<String, Secret> secrets) {
    environment.put("MPD_IDENTITY_ENABLED", "false");
    environment.put("MPD_SERVICE_ENABLED", "false");
    environment.put("MPD_EVENTS_ENABLED", "false");
    environment.put("MPD_REQUIRE_PASSWORD", "true");
    Object activate = scope.getNode().tryGetContext("activateMpdIntegration");
    if (activate == null || "false".equals(activate.toString())) return;
    if (!"true".equals(activate.toString())) throw new IllegalArgumentException("activateMpdIntegration must be true or false");
    String secretArn = required(scope, "mpdIntegrationSecretArn");
    if (!secretArn.matches("arn:aws:secretsmanager:ap-southeast-2:" + scope.getAccount() + ":secret:[A-Za-z0-9/_+=.@-]+"))
      throw new IllegalArgumentException("MPD integration secret must be explicitly supplied in the target account and region");
    var secret = software.amazon.awscdk.services.secretsmanager.Secret.fromSecretCompleteArn(scope, "MpdIntegrationSecret", secretArn);
    for (String name : List.of("MPD_COGNITO_ISSUER", "MPD_COGNITO_CLIENT_ID", "MPD_ACCESS_URL", "MPD_WORKSPACE_ID", "MPD_TENANT_ID",
        "MPD_ALLOWED_CLIENT_IDS", "MPD_ALLOWED_ORIGINS", "MPD_EVENT_RECEIVER_URL", "MPD_EVENT_KEY_ID")) {
      environment.put(name, required(scope, name));
    }
    for (String name : List.of("MPD_COGNITO_CLIENT_SECRET", "MPD_SESSION_ENCRYPTION_KEY", "MPD_SERVICE_TOKEN_HASHES", "MPD_EVENT_SIGNING_KEY")) {
      secrets.put(name, Secret.fromSecretsManager(secret, name));
    }
    environment.put("MPD_IDENTITY_ENABLED", "true");
    environment.put("MPD_SERVICE_ENABLED", "true");
    environment.put("MPD_EVENTS_ENABLED", "true");
  }

  /**
   * Exact browser origins of approved MPD clients, such as Staff OS. They upload straight to the
   * recordings bucket, so the bucket CORS rule must list them. Empty while the integration is dormant.
   */
  static List<String> browserOrigins(Stack scope) {
    Object activate = scope.getNode().tryGetContext("activateMpdIntegration");
    if (activate == null || !"true".equals(activate.toString())) return List.of();
    List<String> origins = new ArrayList<>();
    for (String raw : required(scope, "MPD_ALLOWED_ORIGINS").split(",")) {
      String origin = raw.trim();
      if (origin.isEmpty()) continue;
      // The server refuses anything but exact https origins, and browsers send lowercase hosts,
      // so reject anything else here before a deploy.
      if (!origin.matches("https://[a-z0-9-]+(\\.[a-z0-9-]+)*(:[0-9]{1,5})?"))
        throw new IllegalArgumentException("MPD_ALLOWED_ORIGINS must list exact lowercase https origins");
      String host = origin.substring("https://".length()).replaceFirst(":[0-9]+$", "");
      // Production accepts named hosts only: no localhost and no IP address literals.
      boolean local = host.equals("localhost") || host.endsWith(".localhost") || host.matches("[0-9]+(\\.[0-9]+){3}");
      if (local && !StagingConfig.ACCOUNT.equals(scope.getAccount()))
        throw new IllegalArgumentException("localhost and IP address origins are allowed only in staging");
      origins.add(origin);
    }
    return origins;
  }

  private static String required(Stack scope, String name) {
    Object value = scope.getNode().tryGetContext(name);
    if (value == null || value.toString().isBlank()) throw new IllegalArgumentException("Missing activation setting " + name);
    return value.toString();
  }
}
