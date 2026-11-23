package com.mpd.sendrec.infrastructure;

import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import java.io.UncheckedIOException;
import java.nio.file.Files;
import java.nio.file.Path;
import software.amazon.awscdk.App;
import software.amazon.awscdk.Environment;
import software.amazon.awscdk.StackProps;
import software.amazon.awscdk.cxapi.CloudAssembly;

/**
 * `targetEnvironment` picks what to synthesize: staging (the default) or production for a hand
 * release, or pipeline for `SendRecPipeline`, which deploys both stacks itself.
 */
public final class SendRecApp {
  private SendRecApp() {}

  public static void main(final String[] args) {
    App app = new App();

    Object target = app.getNode().tryGetContext("targetEnvironment");
    if (target != null && !target.equals("staging") && !target.equals("production") && !target.equals("pipeline")) {
      throw new IllegalArgumentException("targetEnvironment must be staging, production or pipeline");
    }
    if ("pipeline".equals(target)) {
      PipelineConfig config = PipelineConfig.load(app, System::getenv);
      new PipelineStack(app, "SendRecPipeline", StackProps.builder()
          .env(Environment.builder().account(config.deploymentAccount()).region(PipelineConfig.REGION).build())
          .stackName("SendRecPipeline")
          .description("SendRec's deployment pipeline and nightly production release")
          .build(), config);
      writeRelease(app.synth(), config);
      return;
    }
    if ("production".equals(target)) {
      ProductionConfig production = ProductionConfig.fromContext(app);
      new SendRecStack(app, "SendRecProduction", productionProps(production.account()), production);
    } else {
      new SendRecStack(app, "SendRecStaging", stagingProps(StagingConfig.ACCOUNT), StagingConfig.INSTANCE);
    }
    app.synth();
  }

  /** The same stack name and description wherever the stack is synthesized, so the pipeline updates the live stack. */
  static StackProps stagingProps(String account) {
    return StackProps.builder()
        .env(Environment.builder().account(account).region(StagingConfig.REGION).build())
        .stackName("SendRecStaging")
        .description("SendRec video recording and hosting, staging")
        .build();
  }

  static StackProps productionProps(String account) {
    return StackProps.builder()
        .env(Environment.builder().account(account).region(ProductionConfig.REGION).build())
        .stackName("SendRecProduction")
        .description("SendRec production; runs the tasks named by productionTaskCount")
        .build();
  }

  /** Later steps read the release from the cloud assembly, so it never enters the pipeline's definition. */
  static void writeRelease(CloudAssembly assembly, PipelineConfig config) {
    try {
      Files.writeString(Path.of(assembly.getDirectory(), PipelineStack.RELEASE_FILE),
          new ObjectMapper().writerWithDefaultPrettyPrinter().writeValueAsString(config.release()));
    } catch (IOException error) {
      throw new UncheckedIOException(error);
    }
  }
}
