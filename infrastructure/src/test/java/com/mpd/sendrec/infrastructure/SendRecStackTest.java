package com.mpd.sendrec.infrastructure;

import static org.junit.jupiter.api.Assertions.*;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import software.amazon.awscdk.*;
import software.amazon.awscdk.assertions.*;
import software.amazon.awscdk.services.ec2.*;

class SendRecStackTest {
  private final StackProps props = StackProps.builder().env(Environment.builder()
      .account(StagingConfig.ACCOUNT).region(StagingConfig.REGION).build()).build();

  private SendRecStack stack(Map<String, Object> context) {
    App app = new App(AppProps.builder().context(context).build());
    Stack network = new Stack(app, "SyntheticNetwork", props);
    IVpc vpc = Vpc.Builder.create(network, "Vpc").maxAzs(2).subnetConfiguration(List.of(
        SubnetConfiguration.builder().name("public").subnetType(SubnetType.PUBLIC).build(),
        SubnetConfiguration.builder().name("service").subnetType(SubnetType.PRIVATE_WITH_EGRESS).build(),
        SubnetConfiguration.builder().name("data").subnetType(SubnetType.PRIVATE_ISOLATED).build())).build();
    return new SendRecStack(app, "SendRecStaging", props, StagingConfig.INSTANCE, vpc);
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
}
