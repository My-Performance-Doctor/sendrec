package com.mpd.sendrec.infrastructure;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.function.Function;
import software.constructs.Construct;

/**
 * The pipeline's inputs. Nothing here is committed: the Build step loads three SSM parameters of
 * the deployment account into the environment (design D2), and the release comes from the image
 * the step just built. Each context object becomes context on its own stage only.
 *
 * @param deploymentAccount the account that runs the pipeline
 * @param connectionArn the deployment account's GitHub connection
 * @param stagingContext CDK context for SendRecStaging, as a staging release passes it
 * @param productionContext CDK context for SendRecProduction, without its image
 * @param revision the full commit being released
 * @param digest the image digest built for that commit
 */
record PipelineConfig(String deploymentAccount, String connectionArn, Map<String, Object> stagingContext,
    Map<String, Object> productionContext, String revision, String digest) {
  static final String SETTINGS_VARIABLE = "SENDREC_PIPELINE_SETTINGS";
  static final String STAGING_VARIABLE = "SENDREC_STAGING_CONTEXT";
  static final String PRODUCTION_VARIABLE = "SENDREC_PRODUCTION_CONTEXT";
  /** The SSM parameters the Build step loads into those variables, in the deployment account. */
  static final Map<String, String> PARAMETERS = new LinkedHashMap<>(Map.of(
      SETTINGS_VARIABLE, "/sendrec/pipeline/settings",
      STAGING_VARIABLE, "/sendrec/pipeline/staging-context",
      PRODUCTION_VARIABLE, "/sendrec/pipeline/production-context"));
  static final String REGION = "ap-southeast-2";
  static final String IMAGE_REPOSITORY = "sendrec";
  /** Keys only the pipeline sets, and bootstrap mode, which a pipeline must never deploy. */
  static final List<String> REFUSED_KEYS =
      List.of("releaseImage", "productionImage", "targetEnvironment", "bootstrap", "bootstrapCidr");
  private static final ObjectMapper JSON = new ObjectMapper();

  PipelineConfig {
    if (deploymentAccount == null || !deploymentAccount.matches("[0-9]{12}") || deploymentAccount.equals(StagingConfig.ACCOUNT))
      throw new IllegalArgumentException("deploymentAccount must be the deployment account's 12-digit ID");
    if (connectionArn == null || !connectionArn.matches(
        "arn:aws:(codeconnections|codestar-connections):" + REGION + ":" + deploymentAccount + ":connection/[0-9a-f-]{36}"))
      throw new IllegalArgumentException("connectionArn must be a GitHub connection in the deployment account");
    checkContext("staging", stagingContext);
    checkContext("production", productionContext);
    Object account = productionContext.get("productionAccount");
    if (account == null || account.toString().equals(deploymentAccount) || account.toString().equals(StagingConfig.ACCOUNT))
      throw new IllegalArgumentException("productionAccount must be production's own account");
    if (revision == null || !revision.matches("[0-9a-f]{40}"))
      throw new IllegalArgumentException("releaseRevision must be a full commit SHA");
    if (digest == null || !digest.matches("sha256:[0-9a-f]{64}"))
      throw new IllegalArgumentException("releaseDigest must be a sha256 image digest");
    stagingContext = Map.copyOf(stagingContext);
    productionContext = Map.copyOf(productionContext);
  }

  private static void checkContext(String environment, Map<String, Object> context) {
    if (context == null) throw new IllegalArgumentException("Missing the " + environment + " context");
    for (String key : REFUSED_KEYS) {
      if (context.containsKey(key)) throw new IllegalArgumentException(
          "The " + environment + " context must not set " + key + "; the pipeline sets the image and never bootstraps");
    }
  }

  /** Reads the settings and contexts from the given environment, and the release from context. */
  static PipelineConfig load(Construct scope, Function<String, String> environment) {
    JsonNode settings = object(SETTINGS_VARIABLE, environment.apply(SETTINGS_VARIABLE));
    return new PipelineConfig(text(settings, "deploymentAccount"), text(settings, "connectionArn"),
        context(STAGING_VARIABLE, environment.apply(STAGING_VARIABLE)),
        context(PRODUCTION_VARIABLE, environment.apply(PRODUCTION_VARIABLE)),
        contextText(scope, "releaseRevision"), contextText(scope, "releaseDigest"));
  }

  /** The release's version, as the image's VERSION build argument and /api/health report it. */
  String version() {
    return revision.substring(0, 8);
  }

  String stagingImage() {
    return image(StagingConfig.ACCOUNT);
  }

  String productionImage() {
    return image(productionContext.get("productionAccount").toString());
  }

  private String image(String account) {
    return account + ".dkr.ecr." + REGION + ".amazonaws.com/" + IMAGE_REPOSITORY + "@" + digest;
  }

  /** What later steps read from the cloud assembly: never committed, never in the pipeline's definition. */
  Map<String, String> release() {
    Map<String, String> release = new LinkedHashMap<>();
    release.put("revision", revision);
    release.put("version", version());
    release.put("digest", digest);
    release.put("stagingImage", stagingImage());
    release.put("productionImage", productionImage());
    return release;
  }

  private static JsonNode object(String name, String raw) {
    if (raw == null || raw.isBlank()) throw new IllegalArgumentException("Missing " + name + " (" + PARAMETERS.get(name) + ")");
    try {
      JsonNode node = JSON.readTree(raw);
      if (node == null || !node.isObject()) throw new IllegalArgumentException(name + " must be a JSON object");
      return node;
    } catch (com.fasterxml.jackson.core.JsonProcessingException error) {
      // The message would quote the value, so it is left out.
      throw new IllegalArgumentException(name + " is not valid JSON");
    }
  }

  /** A context object's values: strings, numbers and booleans only, as CDK context takes them. */
  private static Map<String, Object> context(String name, String raw) {
    Map<String, Object> values = new LinkedHashMap<>();
    object(name, raw).properties().forEach(entry -> {
      JsonNode value = entry.getValue();
      if (value.isTextual()) values.put(entry.getKey(), value.asText());
      else if (value.isBoolean()) values.put(entry.getKey(), value.asBoolean());
      else if (value.isNumber()) values.put(entry.getKey(), value.numberValue());
      else throw new IllegalArgumentException(name + "." + entry.getKey() + " must be a string, number or boolean");
    });
    return values;
  }

  private static String text(JsonNode settings, String key) {
    JsonNode value = settings.get(key);
    return value == null || !value.isTextual() ? null : value.asText();
  }

  private static String contextText(Construct scope, String key) {
    Object value = scope.getNode().tryGetContext(key);
    return value == null ? null : value.toString();
  }
}
