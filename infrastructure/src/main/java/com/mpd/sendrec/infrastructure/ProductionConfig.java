package com.mpd.sendrec.infrastructure;

import software.constructs.Construct;

/** Production is opt-in synthesis. Creating its service starts zero application tasks. */
public record ProductionConfig(String account, String vpcId, String cloudFrontPrefixListId,
    String recordingsBucketName, String immutableImage, String alertTopicArn) implements EnvironmentConfig {
  public static final String REGION = "ap-southeast-2";

  public ProductionConfig {
    if (account == null || !account.matches("[0-9]{12}")) throw new IllegalArgumentException("Explicit production account required");
    if (vpcId == null || !vpcId.matches("vpc-[a-f0-9]+") || vpcId.equals(StagingConfig.INSTANCE.vpcId()))
      throw new IllegalArgumentException("Production requires a separate VPC");
    if (cloudFrontPrefixListId == null || !cloudFrontPrefixListId.matches("pl-[a-f0-9]+"))
      throw new IllegalArgumentException("CloudFront prefix list required");
    if (recordingsBucketName == null || !recordingsBucketName.matches("[a-z0-9][a-z0-9-]{1,61}[a-z0-9]")
        || recordingsBucketName.equals(StagingConfig.INSTANCE.recordingsBucketName()))
      throw new IllegalArgumentException("Separate production bucket required");
    if (immutableImage == null || !immutableImage.matches(account + "\\.dkr\\.ecr\\.ap-southeast-2\\.amazonaws\\.com/[a-z0-9/_-]+@sha256:[a-f0-9]{64}"))
      throw new IllegalArgumentException("Production image must be a same-account Sydney ECR digest");
    if (alertTopicArn == null || !alertTopicArn.matches("arn:aws:sns:ap-southeast-2:" + account + ":[A-Za-z0-9_-]+"))
      throw new IllegalArgumentException("An explicit same-account operational SNS destination is required");
  }

  public static ProductionConfig fromContext(Construct scope) {
    return new ProductionConfig(required(scope, "productionAccount"), required(scope, "productionVpcId"),
        required(scope, "productionCloudFrontPrefixListId"), required(scope, "productionBucket"),
        required(scope, "productionImage"), required(scope, "productionAlertTopicArn"));
  }

  private static String required(Construct scope, String key) {
    Object value = scope.getNode().tryGetContext(key);
    if (value == null || value.toString().isBlank()) throw new IllegalArgumentException("Missing " + key);
    return value.toString();
  }

  public String environmentName() { return "production"; }
  public int taskCpu() { return 2048; }
  public int taskMemoryMiB() { return 4096; }
  public boolean production() { return true; }
}
