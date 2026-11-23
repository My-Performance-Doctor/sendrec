package com.mpd.sendrec.infrastructure;

import com.fasterxml.jackson.databind.ObjectMapper;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import software.amazon.awscdk.App;
import software.amazon.awscdk.AppProps;
import software.amazon.awscdk.Environment;
import software.amazon.awscdk.StackProps;

/** A synthetic deployment, staging and production configuration for the pipeline tests. No real IDs. */
final class PipelineFixture {
  static final String DEPLOYMENT = "999988887777";
  static final String PRODUCTION = "111122223333";
  static final String PRODUCTION_VPC = "vpc-0abc0abc0abc0abc0";
  static final String REVISION = "0123456789abcdef0123456789abcdef01234567";
  static final String DIGEST = "sha256:" + "c".repeat(64);
  static final String CONNECTION =
      "arn:aws:codeconnections:ap-southeast-2:" + DEPLOYMENT + ":connection/00000000-0000-4000-8000-000000000003";
  private static final ObjectMapper JSON = new ObjectMapper();

  private PipelineFixture() {}

  static Map<String, Object> integration(String account, String watchShare) {
    Map<String, Object> context = new LinkedHashMap<>();
    context.put("activateMpdIntegration", "true");
    context.put("mpdIntegrationSecretArn", "arn:aws:secretsmanager:ap-southeast-2:" + account + ":secret:synthetic/mpd-AbCdEf");
    context.put("MPD_COGNITO_ISSUER", "https://cognito-idp.ap-southeast-2.amazonaws.com/ap-southeast-2_synthetic");
    context.put("MPD_COGNITO_CLIENT_ID", "synthetic-client");
    context.put("MPD_ACCESS_URL", "https://api.example.test/api/v1/staff/video-access");
    context.put("MPD_WORKSPACE_ID", "00000000-0000-4000-8000-000000000001");
    context.put("MPD_TENANT_ID", "00000000-0000-4000-8000-000000000002");
    context.put("MPD_ALLOWED_CLIENT_IDS", "synthetic-client");
    context.put("MPD_ALLOWED_ORIGINS", "https://staff.example.test");
    context.put("MPD_EVENT_RECEIVER_URL", "https://api.example.test/webhook");
    context.put("MPD_EVENT_KEY_ID", "synthetic-1");
    context.put("mpdProcessingProgress", true);
    context.put("mpdWatchShare", watchShare);
    return context;
  }

  /** The network each stage names in its context, instead of a lookup. */
  static void network(Map<String, Object> context, int network) {
    context.put("vpcAvailabilityZones", "ap-southeast-2a,ap-southeast-2b");
    context.put("vpcPrivateSubnetIds", "subnet-" + network + "1a0000000000000,subnet-" + network + "1b0000000000000");
    context.put("vpcIsolatedSubnetIds", "subnet-" + network + "2a0000000000000,subnet-" + network + "2b0000000000000");
  }

  static Map<String, Object> stagingContext() {
    Map<String, Object> context = integration(StagingConfig.ACCOUNT, "false");
    network(context, 1);
    return context;
  }

  static Map<String, Object> productionContext() {
    Map<String, Object> context = integration(PRODUCTION, "true");
    context.put("productionAccount", PRODUCTION);
    context.put("productionVpcId", PRODUCTION_VPC);
    context.put("productionCloudFrontPrefixListId", "pl-12345678");
    context.put("productionBucket", "synthetic-production-recordings");
    context.put("productionAlertTopicArn", "arn:aws:sns:ap-southeast-2:" + PRODUCTION + ":synthetic-operations");
    context.put("productionDomainCertificateArn",
        "arn:aws:acm:us-east-1:" + PRODUCTION + ":certificate/00000000-0000-4000-8000-000000000004");
    context.put("productionTaskCount", 2);
    context.put("productionDatabaseSize", "small");
    network(context, 2);
    return context;
  }

  static Map<String, String> environment(Map<String, Object> staging, Map<String, Object> production) {
    try {
      Map<String, String> environment = new HashMap<>();
      environment.put(PipelineConfig.SETTINGS_VARIABLE,
          JSON.writeValueAsString(Map.of("deploymentAccount", DEPLOYMENT, "connectionArn", CONNECTION)));
      environment.put(PipelineConfig.STAGING_VARIABLE, JSON.writeValueAsString(staging));
      environment.put(PipelineConfig.PRODUCTION_VARIABLE, JSON.writeValueAsString(production));
      return environment;
    } catch (com.fasterxml.jackson.core.JsonProcessingException error) {
      throw new IllegalStateException(error);
    }
  }

  static Map<String, String> environment() {
    return environment(stagingContext(), productionContext());
  }

  static App app(String revision, String digest, Map<String, Object> extra) {
    // No lookup answers: the pipeline must synthesize from its stage contexts alone.
    Map<String, Object> context = new HashMap<>();
    context.put("releaseRevision", revision);
    context.put("releaseDigest", digest);
    context.putAll(extra);
    return new App(AppProps.builder().context(context).build());
  }

  static PipelineStack pipeline(App app, Map<String, String> environment) {
    PipelineConfig config = PipelineConfig.load(app, environment::get);
    return new PipelineStack(app, "SendRecPipeline", StackProps.builder()
        .env(Environment.builder().account(config.deploymentAccount()).region("ap-southeast-2").build()).build(), config);
  }

  static PipelineStack pipeline() {
    return pipeline(app(REVISION, DIGEST, Map.of()), environment());
  }

  static SendRecStack stageStack(PipelineStack pipeline, String stage, String stack) {
    return (SendRecStack) pipeline.getNode().findChild(stage).getNode().findChild(stack);
  }
}
