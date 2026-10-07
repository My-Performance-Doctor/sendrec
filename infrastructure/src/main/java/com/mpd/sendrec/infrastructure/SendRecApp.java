package com.mpd.sendrec.infrastructure;

import software.amazon.awscdk.App;
import software.amazon.awscdk.Environment;
import software.amazon.awscdk.StackProps;

public final class SendRecApp {
  private SendRecApp() {}

  public static void main(final String[] args) {
    App app = new App();

    new SendRecStack(
        app,
        "SendRecStaging",
        StackProps.builder()
            .env(
                Environment.builder()
                    .account(StagingConfig.ACCOUNT)
                    .region(StagingConfig.REGION)
                    .build())
            .stackName("SendRecStaging")
            .description("SendRec video recording and hosting, staging")
            .build(),
        StagingConfig.INSTANCE);

    app.synth();
  }
}
