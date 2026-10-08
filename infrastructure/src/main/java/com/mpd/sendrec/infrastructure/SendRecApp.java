package com.mpd.sendrec.infrastructure;

import software.amazon.awscdk.App;
import software.amazon.awscdk.Environment;
import software.amazon.awscdk.StackProps;

public final class SendRecApp {
  private SendRecApp() {}

  public static void main(final String[] args) {
    App app = new App();

    Object target = app.getNode().tryGetContext("targetEnvironment");
    if (target != null && !target.equals("staging") && !target.equals("production")) {
      throw new IllegalArgumentException("targetEnvironment must be staging or production");
    }
    if ("production".equals(target)) {
      ProductionConfig production = ProductionConfig.fromContext(app);
      new SendRecStack(app, "SendRecProduction", StackProps.builder()
          .env(Environment.builder().account(production.account()).region(ProductionConfig.REGION).build())
          .stackName("SendRecProduction").description("SendRec production preparation; zero tasks until approved release")
          .build(), production);
    } else {
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

    }
    app.synth();
  }
}
