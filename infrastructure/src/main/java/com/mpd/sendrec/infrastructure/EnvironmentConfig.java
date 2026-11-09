package com.mpd.sendrec.infrastructure;

/** Per-environment values the stack needs. One implementation per AWS account. */
public interface EnvironmentConfig {
  String environmentName();

  String vpcId();

  String cloudFrontPrefixListId();

  String recordingsBucketName();

  int taskCpu();

  int taskMemoryMiB();

  default boolean production() { return false; }

  default String immutableImage() { return ""; }

  default String alertTopicArn() { return ""; }

  /** The public hostname for links, or null to keep the CloudFront hostname. */
  default CustomDomain customDomain() { return null; }

}
