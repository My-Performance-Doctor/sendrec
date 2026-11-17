package com.mpd.sendrec.infrastructure;

import software.constructs.Construct;

/**
 * Production is opt-in synthesis. Its service runs the tasks named by {@link #taskCount}. Patient links
 * use {@link #HOSTNAME}; its certificate, like the account it names, is a required deploy input, so a
 * deploy that leaves it out fails rather than taking the alias off the distribution.
 */
public record ProductionConfig(String account, String vpcId, String cloudFrontPrefixListId,
    String recordingsBucketName, String immutableImage, String alertTopicArn, String domainCertificateArn)
    implements EnvironmentConfig {
  public static final String REGION = "ap-southeast-2";
  public static final String HOSTNAME = "results.mpd.health";

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
    if (!new CustomDomain(HOSTNAME, domainCertificateArn).account().equals(account))
      throw new IllegalArgumentException("The production hostname's certificate must be in the production account");
  }

  public static ProductionConfig fromContext(Construct scope) {
    return new ProductionConfig(required(scope, "productionAccount"), required(scope, "productionVpcId"),
        required(scope, "productionCloudFrontPrefixListId"), required(scope, "productionBucket"),
        required(scope, "productionImage"), required(scope, "productionAlertTopicArn"),
        required(scope, "productionDomainCertificateArn"));
  }

  /**
   * `productionTaskCount`: the service's task count, required on every production deploy. A template
   * without it would reset a running service to the template's count. It is 1 to 4: the production
   * stack has run since 13 November 2026, so a count of 0 could only stop a running service.
   */
  public static int taskCount(Construct scope) {
    String value = required(scope, "productionTaskCount").trim();
    Object raw = scope.getNode().tryGetContext("productionTaskCount");
    int count;
    if (raw instanceof Number number) {
      // cdk.json or -c parsed as JSON gives a number, such as 2 or 2.0.
      double d = number.doubleValue();
      if (d != Math.rint(d) || Double.isInfinite(d)) throw new IllegalArgumentException("productionTaskCount must be a whole number");
      count = (int) d;
      if (count != d) throw new IllegalArgumentException("productionTaskCount must be 1 to 4");
    } else {
      try {
        count = Integer.parseInt(value);
      } catch (NumberFormatException e) {
        throw new IllegalArgumentException("productionTaskCount must be a whole number");
      }
    }
    if (count < 1 || count > 4) throw new IllegalArgumentException("productionTaskCount must be 1 to 4");
    return count;
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
  @Override public CustomDomain customDomain() { return new CustomDomain(HOSTNAME, domainCertificateArn); }
}
