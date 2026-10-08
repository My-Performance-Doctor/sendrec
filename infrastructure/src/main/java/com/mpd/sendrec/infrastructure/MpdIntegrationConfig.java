package com.mpd.sendrec.infrastructure;

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

  private static String required(Stack scope, String name) {
    Object value = scope.getNode().tryGetContext(name);
    if (value == null || value.toString().isBlank()) throw new IllegalArgumentException("Missing activation setting " + name);
    return value.toString();
  }
}
