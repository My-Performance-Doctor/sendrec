package com.mpd.sendrec.infrastructure;

import static org.junit.jupiter.api.Assertions.*;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import software.amazon.awscdk.*;
import software.amazon.awscdk.assertions.*;
import software.amazon.awscdk.services.ec2.*;

class SendRecStackTest {
  private static final String PRODUCTION_TOPIC = "arn:aws:sns:ap-southeast-2:537421187871:synthetic-operations";
  private final StackProps props = StackProps.builder().env(Environment.builder()
      .account(StagingConfig.ACCOUNT).region(StagingConfig.REGION).build()).build();

  private SendRecStack stack(Map<String, Object> context) {
    App app = new App(AppProps.builder().context(context).build());
    return new SendRecStack(app, "SendRecStaging", props, StagingConfig.INSTANCE, syntheticVpc(app, "SyntheticNetwork"));
  }

  private IVpc syntheticVpc(App app, String networkName) {
    Stack network = new Stack(app, networkName, props);
    return Vpc.Builder.create(network, "Vpc").maxAzs(2).subnetConfiguration(List.of(
        SubnetConfiguration.builder().name("public").subnetType(SubnetType.PUBLIC).build(),
        SubnetConfiguration.builder().name("service").subnetType(SubnetType.PRIVATE_WITH_EGRESS).build(),
        SubnetConfiguration.builder().name("data").subnetType(SubnetType.PRIVATE_ISOLATED).build())).build();
  }

  @Test void normalDeploymentHasPrivateOriginsAndStorage() {
    SendRecStack stack = stack(Map.of());
    Template template = Template.fromStack(stack);
    assertEquals(StagingConfig.ACCOUNT, stack.getAccount());
    assertEquals(StagingConfig.REGION, stack.getRegion());
    template.resourceCountIs("AWS::CloudFront::VpcOrigin", 1);
    template.hasResourceProperties("AWS::ElasticLoadBalancingV2::LoadBalancer", Map.of("Scheme", "internal",
        "LoadBalancerAttributes", Match.arrayWith(List.of(Map.of("Key", "routing.http.xff_header_processing.mode", "Value", "preserve")))));
    template.hasResourceProperties("AWS::CloudFront::Distribution", Map.of("DistributionConfig", Match.objectLike(Map.of(
        // With no ViewerCertificate or aliases, CloudFront uses its managed certificate.
        "ViewerCertificate", Match.absent(), "Aliases", Match.absent(),
        "DefaultCacheBehavior", Match.objectLike(Map.of("ViewerProtocolPolicy", "redirect-to-https",
            "CachePolicyId", "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"))))));
    template.hasResource("AWS::S3::Bucket", Match.objectLike(Map.of("DeletionPolicy", "Retain", "Properties", Match.objectLike(Map.of(
        "PublicAccessBlockConfiguration", Map.of("BlockPublicAcls", true, "BlockPublicPolicy", true, "IgnorePublicAcls", true, "RestrictPublicBuckets", true),
        "BucketEncryption", Match.anyValue())))));
    template.hasResourceProperties("AWS::S3::Bucket", Map.of("VersioningConfiguration", Match.absent()));
    template.hasResourceProperties("AWS::S3::Bucket", Map.of("LifecycleConfiguration", Map.of("Rules", List.of(Map.of("Id", "ExpireOldVersions", "Status", "Enabled",
        "NoncurrentVersionExpiration", Map.of("NoncurrentDays", 30), "AbortIncompleteMultipartUpload", Map.of("DaysAfterInitiation", 7))))));
    template.hasResource("AWS::RDS::DBInstance", Match.objectLike(Map.of("DeletionPolicy", "Retain", "Properties", Match.objectLike(Map.of(
        "StorageEncrypted", true, "PubliclyAccessible", false, "DBName", "sendrec",
        "EngineVersion", "18.6", "AllowMajorVersionUpgrade", true, "BackupRetentionPeriod", 7)))));
    template.resourceCountIs("AWS::IAM::User", 0);
    for (Object resource : template.findResources("AWS::SecretsManager::Secret").values()) {
      assertEquals("Retain", ((Map<?, ?>) resource).get("DeletionPolicy"));
    }
    template.hasResourceProperties("AWS::ECS::TaskDefinition", Map.of("Cpu", "2048", "Memory", "4096",
        "ContainerDefinitions", Match.arrayWith(List.of(Match.objectLike(Map.of(
            "Environment", Match.arrayWith(List.of(Map.of("Name", "REGISTRATION_ENABLED", "Value", "false")))))))));
    for (String field : List.of("DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "JWT_SECRET")) {
      template.hasResourceProperties("AWS::ECS::TaskDefinition", Map.of("ContainerDefinitions", Match.arrayWith(List.of(Match.objectLike(Map.of(
          "Secrets", Match.arrayWith(List.of(Match.objectLike(Map.of("Name", field, "ValueFrom", Match.anyValue()))))))))));
    }
    template.hasResourceProperties("AWS::EC2::SecurityGroupIngress", Map.of("SourcePrefixListId", "pl-b8a742d1", "FromPort", 80));
    template.hasResourceProperties("AWS::ECS::Service", Map.of("NetworkConfiguration", Map.of("AwsvpcConfiguration", Match.objectLike(Map.of("AssignPublicIp", "DISABLED")))));
    String json = template.toJSON().toString();
    assertFalse(json.contains("S3_ACCESS_KEY"));
    assertFalse(json.contains("S3_SECRET_KEY"));
  }

  @Test void bootstrapRequiresOneExplicitSourceAddress() {
    for (Map<String, Object> input : List.of(Map.<String, Object>of("bootstrap", "true"),
        Map.<String, Object>of("bootstrap", "true", "bootstrapCidr", "0.0.0.0/0"),
        Map.<String, Object>of("bootstrap", "true", "bootstrapCidr", "999.0.0.1/32"),
        Map.<String, Object>of("bootstrap", "unexpected"))) {
      assertThrows(IllegalArgumentException.class, () -> stack(input));
    }
    Template template = Template.fromStack(stack(Map.of("bootstrap", "true", "bootstrapCidr", "192.0.2.10/32")));
    template.hasResourceProperties("AWS::ECS::TaskDefinition", Map.of("ContainerDefinitions", Match.arrayWith(List.of(Match.objectLike(Map.of(
        "Environment", Match.arrayWith(List.of(Map.of("Name", "REGISTRATION_ENABLED", "Value", "true")))))))));
    template.hasResourceProperties("AWS::CloudFront::Function", Map.of("FunctionCode", Match.stringLikeRegexp("192\\.0\\.2\\.10")));
  }

  @Test void registrationCanCloseBeforeAccessRestrictionIsRemoved() {
    Template template = Template.fromStack(stack(Map.of("bootstrap", "false", "bootstrapCidr", "192.0.2.10/32")));
    template.hasResourceProperties("AWS::ECS::TaskDefinition", Map.of("ContainerDefinitions", Match.arrayWith(List.of(Match.objectLike(Map.of(
        "Environment", Match.arrayWith(List.of(Map.of("Name", "REGISTRATION_ENABLED", "Value", "false")))))))));
    template.hasResourceProperties("AWS::CloudFront::Function", Map.of("FunctionCode", Match.stringLikeRegexp("192\\.0\\.2\\.10")));
    assertThrows(IllegalArgumentException.class, () -> stack(Map.of("bootstrap", "false", "bootstrapCidr", "0.0.0.0/0")));
  }
  @Test void productionIsIsolatedImmutableAndUnactivated() {
    SendRecStack stack = production(Map.of());
    Template template = Template.fromStack(stack);
    template.hasResourceProperties("AWS::ECS::Service", Map.of("DesiredCount", 0, "DeploymentConfiguration", Match.objectLike(Map.of(
        "MinimumHealthyPercent", 100, "MaximumPercent", 200, "DeploymentCircuitBreaker", Map.of("Enable", true, "Rollback", true)))));
    template.hasResourceProperties("AWS::RDS::DBInstance", Map.of("DBInstanceClass", "db.t4g.small", "MultiAZ", true, "BackupRetentionPeriod", 35,
        "StorageEncrypted", true, "PubliclyAccessible", false, "DeletionProtection", true));
    // Versioning on and nothing expires; only abandoned multipart uploads are cleaned up.
    template.hasResourceProperties("AWS::S3::Bucket", Map.of("VersioningConfiguration", Map.of("Status", "Enabled"),
        "LifecycleConfiguration", Map.of("Rules", List.of(Map.of("Id", "AbortIncompleteUploads", "Status", "Enabled",
            "AbortIncompleteMultipartUpload", Map.of("DaysAfterInitiation", 7))))));
    template.hasResourceProperties("AWS::ElasticLoadBalancingV2::TargetGroup", Map.of("HealthCheckPath", "/api/ready"));
    template.resourceCountIs("AWS::ECS::TaskDefinition", 2);
    String allTasks = template.findResources("AWS::ECS::TaskDefinition").toString();
    assertTrue(allTasks.contains("MPD_IDENTITY_ENABLED, Value=false"));
    assertTrue(allTasks.contains("MPD_EVENTS_ENABLED, Value=false"));
    assertFalse(allTasks.contains("MPD_SESSION_ENCRYPTION_KEY"));
    template.hasResourceProperties("AWS::IAM::Policy", Map.of("PolicyDocument", Match.objectLike(Map.of(
        "Statement", Match.arrayWith(List.of(Match.objectLike(Map.of("Action", "ecr:GetAuthorizationToken"))))))));
    template.resourceCountIs("AWS::CloudWatch::Alarm", 11);
    template.resourceCountIs("AWS::Events::Rule", 1);
    for (var task : template.findResources("AWS::ECS::TaskDefinition").values()) {
      String definition = task.toString();
      assertTrue(definition.contains("537421187871.dkr.ecr.ap-southeast-2."));
      assertTrue(definition.contains("/sendrec@sha256:" + "a".repeat(64)));
      assertFalse(definition.contains("REGISTRATION_ENABLED, Value=true"));
    }
    for (var alarm : template.findResources("AWS::CloudWatch::Alarm").values()) {
      String definition = alarm.toString();
      assertTrue(definition.contains("AlarmActions=[" + PRODUCTION_TOPIC + "]"));
      assertTrue(definition.contains("OKActions=[" + PRODUCTION_TOPIC + "]"));
      assertTrue(definition.contains("TreatMissingData=breaching") || definition.contains("TreatMissingData=notBreaching"));
    }
    template.hasResourceProperties("AWS::CloudWatch::Alarm", Map.of("MetricName", "FreeStorageSpace",
        "Threshold", 4.0 * 1024 * 1024 * 1024, "ComparisonOperator", "LessThanThreshold", "TreatMissingData", "breaching"));
    template.hasResourceProperties("AWS::CloudWatch::Alarm", Map.of("MetricName", "FreeableMemory",
        "Threshold", 128.0 * 1024 * 1024, "ComparisonOperator", "LessThanThreshold", "TreatMissingData", "breaching"));
    // Desired count comes from the independent probe, not this dormant template.
    template.hasResourceProperties("AWS::Lambda::Function", Map.of("Environment", Match.objectLike(Map.of(
        "Variables", Match.objectLike(Map.of("SERVICE_ARN", Match.anyValue(), "CLUSTER_ARN", Match.anyValue()))))));
    template.hasResourceProperties("AWS::IAM::Policy", Map.of("PolicyDocument", Match.objectLike(Map.of(
        "Statement", Match.arrayWith(List.of(Match.objectLike(Map.of("Action", "ecs:DescribeServices"))))))));
    int gated = 0;
    for (var alarm : template.findResources("AWS::CloudWatch::Alarm").entrySet()) {
      String definition = alarm.getValue().toString();
      if (alarm.getKey().startsWith("Database") || alarm.getKey().startsWith("BackupAgeSeconds")) {
        assertFalse(definition.contains("Expression="));
      } else {
        gated++;
        double missing = alarm.getKey().startsWith("Target5xx") ? 0.0
            : alarm.getKey().startsWith("OutboxOldestSeconds") ? 300.0 : 1.0;
        assertTrue(definition.contains("Expression=IF(desired > 0, FILL(signal, " + missing + "), 0)"));
        assertTrue(definition.contains("MetricName=ServiceDesiredCount"));
      }
    }
    assertEquals(7, gated);
    Stage.of(stack).synth();
  }

  @Test void controlledStagingHasSingleMigrationAndDisabledIntegration() {
    String image = "537421187871.dkr.ecr.ap-southeast-2.amazonaws.com/sendrec@sha256:" + "a".repeat(64);
    Template template = Template.fromStack(stack(Map.of("releaseImage", image)));
    template.resourceCountIs("AWS::ECS::TaskDefinition", 2);
    template.hasResourceProperties("AWS::RDS::DBInstance", Map.of("BackupRetentionPeriod", 7, "MultiAZ", false));
    template.hasResourceProperties("AWS::ECS::Service", Map.of("DesiredCount", 1));
    template.hasResourceProperties("AWS::ElasticLoadBalancingV2::TargetGroup", Map.of("HealthCheckPath", "/api/ready"));
    String tasks = template.findResources("AWS::ECS::TaskDefinition").toString();
    assertTrue(tasks.contains("MIGRATIONS_MODE, Value=only"));
    assertTrue(tasks.contains("MIGRATIONS_MODE, Value=skip"));
    assertTrue(tasks.contains("MPD_IDENTITY_ENABLED, Value=false"));
    assertThrows(IllegalArgumentException.class, () -> stack(Map.of("releaseImage", image.replace("@sha256:" + "a".repeat(64), ":latest"))));
    assertThrows(IllegalArgumentException.class, () -> stack(Map.of("activateMpdIntegration", "true")));
  }

  private Map<String, Object> activation(String origins) {
    Map<String, Object> context = new java.util.HashMap<>(Map.of(
        "activateMpdIntegration", "true",
        "mpdIntegrationSecretArn", "arn:aws:secretsmanager:ap-southeast-2:" + StagingConfig.ACCOUNT + ":secret:synthetic/mpd-AbCdEf",
        "MPD_COGNITO_ISSUER", "https://cognito-idp.ap-southeast-2.amazonaws.com/ap-southeast-2_synthetic",
        "MPD_COGNITO_CLIENT_ID", "synthetic-client",
        "MPD_ACCESS_URL", "https://api.example.test/api/v1/staff/video-access",
        "MPD_WORKSPACE_ID", "00000000-0000-4000-8000-000000000001",
        "MPD_TENANT_ID", "00000000-0000-4000-8000-000000000002",
        "MPD_ALLOWED_CLIENT_IDS", "synthetic-client",
        "MPD_EVENT_RECEIVER_URL", "https://api.example.test/webhook",
        "MPD_EVENT_KEY_ID", "synthetic-1"));
    context.put("MPD_ALLOWED_ORIGINS", origins);
    return context;
  }

  @Test void approvedMpdClientsCanUploadStraightToTheBucket() {
    Template dormant = Template.fromStack(stack(Map.of()));
    assertFalse(dormant.findResources("AWS::S3::Bucket").toString().contains("staff.example.test"));
    Template active = Template.fromStack(stack(activation("https://staff.example.test, https://localhost:5173")));
    active.hasResourceProperties("AWS::S3::Bucket", Map.of("CorsConfiguration", Map.of("CorsRules", List.of(Match.objectLike(Map.of(
        "AllowedOrigins", Match.arrayWith(List.of("https://staff.example.test", "https://localhost:5173")),
        "AllowedMethods", List.of("GET", "PUT", "HEAD")))))));
    assertThrows(IllegalArgumentException.class, () -> stack(activation("https://staff.example.test/path")));
    assertThrows(IllegalArgumentException.class, () -> stack(activation("*")));
    assertThrows(IllegalArgumentException.class, () -> stack(activation("http://localhost:5173")));
    assertThrows(IllegalArgumentException.class, () -> stack(activation("https://Staff.Example.Test")));
    assertThrows(IllegalArgumentException.class, () -> stack(activation("https://localhost.:5173")));
    for (String local : List.of("https://localhost:5173", "https://app.localhost", "https://127.0.0.1:5173", "https://10.0.0.5")) {
      App app = new App(AppProps.builder().context(activation(local)).build());
      Stack production = new Stack(app, "Production" + local.length(), StackProps.builder().env(Environment.builder()
          .account("111111111111").region(StagingConfig.REGION).build()).build());
      assertThrows(IllegalArgumentException.class, () -> MpdIntegrationConfig.browserOrigins(production), local);
      Stack staging = new Stack(app, "Staging" + local.length(), props);
      assertEquals(List.of(local), MpdIntegrationConfig.browserOrigins(staging));
    }
  }

  @Test void productionRejectsStagingDataMutableImagesAndMissingAlertDestination() {
    String image = "537421187871.dkr.ecr.ap-southeast-2.amazonaws.com/sendrec@sha256:" + "a".repeat(64);
    String topic = "arn:aws:sns:ap-southeast-2:537421187871:synthetic-operations";
    assertThrows(IllegalArgumentException.class, () -> new ProductionConfig("537421187871", StagingConfig.INSTANCE.vpcId(), "pl-123", "isolated-bucket", image, topic));
    assertThrows(IllegalArgumentException.class, () -> new ProductionConfig("537421187871", "vpc-123", "pl-123", StagingConfig.INSTANCE.recordingsBucketName(), image, topic));
    assertThrows(IllegalArgumentException.class, () -> new ProductionConfig("537421187871", "vpc-123", "pl-123", "isolated-bucket", image.replace("@sha256:" + "a".repeat(64), ":latest"), topic));
    assertThrows(IllegalArgumentException.class, () -> new ProductionConfig("537421187871", "vpc-123", "pl-123", "isolated-bucket", image, ""));
    assertThrows(IllegalArgumentException.class, () -> ProductionConfig.fromContext(new App()));
  }

  @Test void productionDatabaseSizeIsMicroOrSmallAndStagingStaysMicro() {
    Template.fromStack(stack(Map.of())).hasResourceProperties("AWS::RDS::DBInstance", Map.of("DBInstanceClass", "db.t4g.micro"));
    Template.fromStack(production(Map.of("productionDatabaseSize", "micro")))
        .hasResourceProperties("AWS::RDS::DBInstance", Map.of("DBInstanceClass", "db.t4g.micro"));
    Template.fromStack(production(Map.of("productionDatabaseSize", "micro")))
        .hasResourceProperties("AWS::CloudWatch::Alarm", Map.of("MetricName", "FreeableMemory", "Threshold", 64.0 * 1024 * 1024));
    assertThrows(IllegalArgumentException.class, () -> production(Map.of("productionDatabaseSize", "large")));
  }

  private SendRecStack production(Map<String, Object> context) {
    App app = new App(AppProps.builder().outdir("build/synthetic-production").context(context).build());
    IVpc vpc = syntheticVpc(app, "ProductionSyntheticNetwork");
    var config = new ProductionConfig("537421187871", "vpc-123abc", "pl-b8a742d1", "synthetic-sendrec-production",
        "537421187871.dkr.ecr.ap-southeast-2.amazonaws.com/sendrec@sha256:" + "a".repeat(64),
        PRODUCTION_TOPIC);
    return new SendRecStack(app, "Production", props, config, vpc);
  }

}
