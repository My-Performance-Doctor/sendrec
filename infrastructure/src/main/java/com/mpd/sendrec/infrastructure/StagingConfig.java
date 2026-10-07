package com.mpd.sendrec.infrastructure;

public final class StagingConfig implements EnvironmentConfig {
  public static final String ACCOUNT = "537421187871";
  public static final String REGION = "ap-southeast-2";
  public static final StagingConfig INSTANCE = new StagingConfig();

  private StagingConfig() {}

  @Override
  public String environmentName() {
    return "staging";
  }

  @Override
  public String vpcId() {
    return "vpc-03b89ce2fed11fe35";
  }

  @Override
  public String cloudFrontPrefixListId() {
    return "pl-b8a742d1";
  }

  @Override
  public String recordingsBucketName() {
    return "mpd-video-recordings-staging";
  }

  @Override
  public int taskCpu() {
    return 2048;
  }

  @Override
  public int taskMemoryMiB() {
    return 4096;
  }
}
