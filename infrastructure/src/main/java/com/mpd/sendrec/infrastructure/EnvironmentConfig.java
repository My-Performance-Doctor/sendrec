package com.mpd.sendrec.infrastructure;

/** Per-environment values the stack needs. One implementation per AWS account. */
public interface EnvironmentConfig {
  String environmentName();

  String vpcId();

  String cloudFrontPrefixListId();

  String recordingsBucketName();

  int taskCpu();

  int taskMemoryMiB();
}
