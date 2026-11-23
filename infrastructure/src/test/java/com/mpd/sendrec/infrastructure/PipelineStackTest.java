package com.mpd.sendrec.infrastructure;

import static com.mpd.sendrec.infrastructure.PipelineFixture.*;
import static org.junit.jupiter.api.Assertions.*;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.function.Consumer;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import software.amazon.awscdk.App;
import software.amazon.awscdk.assertions.Match;
import software.amazon.awscdk.assertions.Template;

/** The pipeline's shape, inputs, roles and steps (design D1 to D6, D10). */
class PipelineStackTest {
  private static final ObjectMapper JSON = new ObjectMapper();
  private static PipelineStack pipeline;
  private static Template template;
  /** Each CodeBuild project's install and build commands, in order, keyed by its step. */
  private static Map<String, List<String>> projects;
  private static Map<String, JsonNode> projectProperties;

  @BeforeAll
  static void synth() throws Exception {
    pipeline = PipelineFixture.pipeline();
    template = Template.fromStack(pipeline);
    projects = new LinkedHashMap<>();
    projectProperties = new LinkedHashMap<>();
    for (Map.Entry<String, Map<String, Object>> project : template.findResources("AWS::CodeBuild::Project").entrySet()) {
      JsonNode properties = JSON.valueToTree(project.getValue().get("Properties"));
      JsonNode phases = JSON.readTree(properties.path("Source").path("BuildSpec").asText()).path("phases");
      List<String> commands = new ArrayList<>();
      for (String phase : List.of("install", "pre_build", "build")) phases.path(phase).path("commands").forEach(c -> commands.add(c.asText()));
      projects.put(project.getKey(), commands);
      projectProperties.put(project.getKey(), properties);
    }
  }

  private static String project(String step) {
    return projects.keySet().stream().filter(key -> key.contains(step)).findFirst()
        .orElseThrow(() -> new AssertionError("no project for " + step + " in " + projects.keySet()));
  }

  private static int indexOf(List<String> commands, String part) {
    for (int i = 0; i < commands.size(); i++) if (commands.get(i).contains(part)) return i;
    throw new AssertionError("no command with " + part + " in " + commands);
  }

  private static List<String> actions(JsonNode stage) {
    List<String> names = new ArrayList<>();
    stage.path("Actions").forEach(action -> names.add(action.path("RunOrder").asInt() + ":" + action.path("Name").asText()));
    return names;
  }

  @Test void theStagesRunInTheStaffAppsOrder() {
    JsonNode stages = JSON.valueToTree(template.findResources("AWS::CodePipeline::Pipeline").values().iterator().next())
        .path("Properties").path("Stages");
    List<String> names = new ArrayList<>();
    stages.forEach(stage -> names.add(stage.path("Name").asText()));
    assertEquals(List.of("Source", "Build", "UpdatePipeline", "Staging", "ApproveProduction", "Production"), names);
    template.hasResourceProperties("AWS::CodePipeline::Pipeline", Map.of("Name", "SendRec", "PipelineType", "V2"));
    assertEquals(List.of("1:ApproveProduction"), actions(stages.get(4)));
    // Staging deploys, then checks its version. Production copies the image, deploys, then checks.
    assertEquals(List.of("1:SendRecStaging.Prepare", "2:SendRecStaging.Deploy", "3:VerifyStaging"), actions(stages.get(3)));
    assertEquals(List.of("1:PromoteImage", "2:SendRecProduction.Prepare", "3:SendRecProduction.Deploy", "4:VerifyProduction"),
        actions(stages.get(5)));
    JsonNode source = stages.get(0).path("Actions").get(0).path("Configuration");
    assertEquals("My-Performance-Doctor/sendrec", source.path("FullRepositoryId").asText());
    assertEquals("main", source.path("BranchName").asText());
    assertEquals(CONNECTION, source.path("ConnectionArn").asText());
  }

  @Test void bothStagesUpdateTheLiveStacksWithTheBuiltDigest() {
    Template staging = Template.fromStack(stageStack(pipeline, "Staging", "SendRecStaging"));
    Template production = Template.fromStack(stageStack(pipeline, "Production", "SendRecProduction"));
    assertEquals("SendRecStaging", stageStack(pipeline, "Staging", "SendRecStaging").getStackName());
    assertEquals("SendRecProduction", stageStack(pipeline, "Production", "SendRecProduction").getStackName());
    assertEquals(PRODUCTION, stageStack(pipeline, "Production", "SendRecProduction").getAccount());
    for (var entry : Map.of(StagingConfig.ACCOUNT, staging, PRODUCTION, production).entrySet()) {
      Template stack = entry.getValue();
      stack.resourceCountIs("AWS::ECS::TaskDefinition", 2);
      for (var task : stack.findResources("AWS::ECS::TaskDefinition").values()) {
        String definition = task.toString();
        assertTrue(definition.contains(entry.getKey() + ".dkr.ecr.ap-southeast-2."), definition);
        assertTrue(definition.contains("/sendrec@" + DIGEST), definition);
      }
      stack.resourceCountIs("Custom::MigrationRun", 1);
      String environment = stack.findResources("AWS::ECS::TaskDefinition").toString();
      assertTrue(environment.contains("MIGRATIONS_MODE, Value=skip"));
      assertTrue(environment.contains("MPD_IDENTITY_ENABLED, Value=true"));
    }
    production.hasResourceProperties("AWS::ECS::Service", Map.of("DesiredCount", 2));
    staging.hasResourceProperties("AWS::ECS::Service", Map.of("DesiredCount", Match.absent()));
    // Each stage gets only its own context: staging's watch share is off, production's on.
    assertTrue(staging.findResources("AWS::ECS::TaskDefinition").toString().contains("MPD_WATCH_SHARE_ENABLED, Value=false"));
    assertTrue(production.findResources("AWS::ECS::TaskDefinition").toString().contains("MPD_WATCH_SHARE_ENABLED, Value=true"));
    // A stage stack deploy publishes no file assets, so the pipeline needs no Assets stage.
    assertFalse(template.toJSON().toString().contains("\"Name\":\"Assets\""));
  }

  @Test void aNewCommitChangesNothingInThePipelinesOwnDefinition() {
    String other = Template.fromStack(PipelineFixture.pipeline(
        PipelineFixture.app("fedcba9876543210fedcba9876543210fedcba98", "sha256:" + "d".repeat(64), Map.of()),
        PipelineFixture.environment())).toJSON().toString();
    String json = template.toJSON().toString();
    assertEquals(json, other, "a merge must not make the pipeline update itself");
    assertFalse(json.contains(REVISION));
    assertFalse(json.contains(DIGEST.substring(7)));
  }

  @Test void theReleaseFileNamesOneDigestForBothEnvironments() throws Exception {
    App app = PipelineFixture.app(REVISION, DIGEST, Map.of());
    PipelineConfig config = PipelineConfig.load(app, PipelineFixture.environment()::get);
    new PipelineStack(app, "SendRecPipeline", software.amazon.awscdk.StackProps.builder()
        .env(software.amazon.awscdk.Environment.builder().account(DEPLOYMENT).region("ap-southeast-2").build()).build(), config);
    var assembly = app.synth();
    SendRecApp.writeRelease(assembly, config);
    JsonNode release = JSON.readTree(Files.readString(Path.of(assembly.getDirectory(), "sendrec-release.json")));
    assertEquals(REVISION, release.path("revision").asText());
    assertEquals("01234567", release.path("version").asText());
    assertEquals(DIGEST, release.path("digest").asText());
    assertEquals("537421187871.dkr.ecr.ap-southeast-2.amazonaws.com/sendrec@" + DIGEST, release.path("stagingImage").asText());
    assertEquals(PRODUCTION + ".dkr.ecr.ap-southeast-2.amazonaws.com/sendrec@" + DIGEST, release.path("productionImage").asText());
  }

  private static void refuses(String message, Consumer<Map<String, Object>> staging, Consumer<Map<String, Object>> production) {
    Map<String, Object> stagingContext = new HashMap<>(PipelineFixture.stagingContext());
    Map<String, Object> productionContext = new HashMap<>(PipelineFixture.productionContext());
    staging.accept(stagingContext);
    production.accept(productionContext);
    assertThrows(IllegalArgumentException.class, () -> PipelineFixture.pipeline(
        PipelineFixture.app(REVISION, DIGEST, Map.of()), PipelineFixture.environment(stagingContext, productionContext)), message);
  }

  @Test void theConfigurationCannotSetTheImageOrBootstrapAndMustStateTheTaskCount() {
    for (String key : List.of("releaseImage", "productionImage", "targetEnvironment", "bootstrap", "bootstrapCidr")) {
      refuses("staging " + key, c -> c.put(key, "x"), c -> {});
      refuses("production " + key, c -> {}, c -> c.put(key, "x"));
    }
    refuses("no task count", c -> {}, c -> c.remove("productionTaskCount"));
    refuses("task count 0", c -> {}, c -> c.put("productionTaskCount", 0));
    refuses("no production account", c -> {}, c -> c.remove("productionAccount"));
    refuses("production in the deployment account", c -> {}, c -> c.put("productionAccount", DEPLOYMENT));
    refuses("production in staging's account", c -> {}, c -> c.put("productionAccount", StagingConfig.ACCOUNT));
    refuses("a switch left out", c -> c.remove("mpdWatchShare"), c -> {});
    refuses("a nested value", c -> c.put("MPD_EVENT_KEY_ID", List.of("a")), c -> {});
  }

  @Test void theReleaseAndSettingsMustBeWellFormed() {
    for (String revision : List.of("0123456", REVISION.toUpperCase(), "main")) {
      assertThrows(IllegalArgumentException.class,
          () -> PipelineFixture.pipeline(PipelineFixture.app(revision, DIGEST, Map.of()), PipelineFixture.environment()), revision);
    }
    for (String digest : List.of("latest", "sha256:abc", DIGEST.toUpperCase())) {
      assertThrows(IllegalArgumentException.class,
          () -> PipelineFixture.pipeline(PipelineFixture.app(REVISION, digest, Map.of()), PipelineFixture.environment()), digest);
    }
    for (String variable : List.of(PipelineConfig.SETTINGS_VARIABLE, PipelineConfig.STAGING_VARIABLE, PipelineConfig.PRODUCTION_VARIABLE)) {
      for (String raw : java.util.Arrays.asList(null, "", "not json", "[]")) {
        Map<String, String> environment = new HashMap<>(PipelineFixture.environment());
        environment.put(variable, raw);
        IllegalArgumentException error = assertThrows(IllegalArgumentException.class,
            () -> PipelineFixture.pipeline(PipelineFixture.app(REVISION, DIGEST, Map.of()), environment), variable + " " + raw);
        // A parse error never quotes the parameter's value.
        if ("not json".equals(raw)) assertFalse(error.getMessage().contains("not json"));
      }
    }
    Map<String, String> wrongConnection = new HashMap<>(PipelineFixture.environment());
    wrongConnection.put(PipelineConfig.SETTINGS_VARIABLE, "{\"deploymentAccount\":\"" + DEPLOYMENT + "\",\"connectionArn\":\""
        + CONNECTION.replace(DEPLOYMENT, PRODUCTION) + "\"}");
    assertThrows(IllegalArgumentException.class,
        () -> PipelineFixture.pipeline(PipelineFixture.app(REVISION, DIGEST, Map.of()), wrongConnection));
  }

  private static List<String> statements(String role) {
    List<String> found = new ArrayList<>();
    for (var policy : template.findResources("AWS::IAM::Policy").values()) {
      JsonNode properties = JSON.valueToTree(policy.get("Properties"));
      if (!properties.path("Roles").toString().contains("\"" + role)) continue;
      properties.path("PolicyDocument").path("Statement").forEach(s -> found.add(s.toString()));
    }
    return found;
  }

  private static String roleId(String name) {
    return template.findResources("AWS::IAM::Role", Match.objectLike(Map.of("Properties", Match.objectLike(Map.of("RoleName", name)))))
        .keySet().iterator().next();
  }

  @Test void onlyTheBuildRolePushesToStagingAndOnlyThePromoteRolePushesToProduction() {
    String build = String.join("\n", statements(roleId("sendrec-pipeline-build")));
    String promote = String.join("\n", statements(roleId("sendrec-pipeline-promote")));
    String stagingRepository = "arn:aws:ecr:ap-southeast-2:537421187871:repository/sendrec\"";
    String productionRepository = "arn:aws:ecr:ap-southeast-2:" + PRODUCTION + ":repository/sendrec\"";
    assertTrue(build.contains("ecr:PutImage") && build.contains(stagingRepository), build);
    assertTrue(build.contains("arn:aws:ecr:ap-southeast-2:537421187871:repository/sendrec-base"), build);
    assertTrue(build.contains("cdk-hnb659fds-lookup-role-537421187871-ap-southeast-2"), build);
    assertTrue(build.contains("cdk-hnb659fds-lookup-role-" + PRODUCTION + "-ap-southeast-2"), build);
    assertFalse(build.contains(productionRepository), build);
    for (String parameter : PipelineStack.parameters().values()) assertTrue(build.contains("parameter" + parameter), parameter);
    assertTrue(promote.contains(productionRepository) && promote.contains("ecr:PutImage"), promote);
    for (String statement : statements(roleId("sendrec-pipeline-promote"))) {
      if (statement.contains(stagingRepository)) assertFalse(statement.contains("ecr:PutImage"), statement);
    }
    // No other policy in the stack can write to either image repository.
    for (var policy : template.findResources("AWS::IAM::Policy").values()) {
      String text = JSON.valueToTree(policy).toString();
      if (text.contains("sendrec-pipeline-build") || text.contains("sendrec-pipeline-promote")) continue;
      if (text.contains("\"Roles\":[{\"Ref\":\"" + roleId("sendrec-pipeline-build"))
          || text.contains("\"Roles\":[{\"Ref\":\"" + roleId("sendrec-pipeline-promote"))) continue;
      assertFalse(text.contains("ecr:PutImage"), text);
    }
  }

  @Test void buildTestsThenBuildsTheImageThenSynthesizesWithItsDigest() {
    String synth = project("Synth");
    List<String> commands = projects.get(synth);
    int node = indexOf(commands, "sha256sum -c -");
    int nodeCheck = indexOf(commands, "test \"$(node --version)\" = v" + PipelineStack.NODE_VERSION);
    int caCertificates = indexOf(commands, "--only-upgrade ca-certificates");
    int javaTrust = indexOf(commands, "test $? -eq 1");
    int cli = indexOf(commands, "npm install -g aws-cdk@" + PipelineStack.CDK_CLI_VERSION);
    int tests = indexOf(commands, "./gradlew -q test");
    int image = indexOf(commands, "sh infrastructure/pipeline/build-image.sh");
    int cdkSynth = indexOf(commands, "cdk synth");
    assertTrue(node < nodeCheck && nodeCheck < cli && caCertificates < image && javaTrust < tests, commands.toString());
    assertTrue(cli < tests && tests < image && image < cdkSynth, commands.toString());
    String synthCommand = commands.get(cdkSynth);
    for (String part : List.of("-c targetEnvironment=pipeline", "-c releaseRevision=\"$REVISION\"",
        "-c releaseDigest=\"$RELEASE_DIGEST\"", "-c alarmEmailsStaging=\"$ALARM_EMAILS_STAGING\"",
        "-c alarmEmailsProduction=\"$ALARM_EMAILS_PRODUCTION\"")) {
      assertTrue(synthCommand.contains(part), part);
    }
    // The image is built and pushed by Docker, so the project is privileged; the configuration
    // arrives from Parameter Store, never on a command line.
    JsonNode environment = projectProperties.get(synth).path("Environment");
    assertTrue(environment.path("PrivilegedMode").asBoolean());
    Map<String, String> variables = new HashMap<>();
    environment.path("EnvironmentVariables").forEach(v -> variables.put(v.path("Name").asText(), v.path("Type").asText() + ":" + v.path("Value").asText()));
    PipelineStack.parameters().forEach((variable, parameter) -> assertEquals("PARAMETER_STORE:" + parameter, variables.get(variable), variable));
    assertEquals("PLAINTEXT:" + PipelineStack.STAGING_REGISTRY, variables.get("STAGING_REGISTRY"));
    assertEquals(DEPLOYMENT, pipeline.getAccount());
  }

  @Test void productionCopiesTheImageWithACheckedCraneAndVerifiesTheVersion() {
    List<String> promote = projects.get(project("PromoteImage"));
    int download = indexOf(promote, "go-containerregistry/releases/download/" + PipelineStack.CRANE_VERSION);
    int check = indexOf(promote, PipelineStack.CRANE_SHA256 + "  /tmp/go-containerregistry_Linux_x86_64.tar.gz\" | sha256sum -c -");
    int unpack = indexOf(promote, "tar -xzf /tmp/go-containerregistry_Linux_x86_64.tar.gz");
    int run = indexOf(promote, "sh infrastructure/pipeline/promote-image.sh");
    assertTrue(download < check && check < unpack && unpack < run, promote.toString());
    assertTrue(promote.contains("test \"$(/tmp/crane/crane version)\" = 0.20.2"), promote.toString());
    for (String step : List.of("VerifyStaging", "VerifyProduction")) {
      List<String> commands = projects.get(project(step));
      assertEquals("sh infrastructure/pipeline/verify-version.sh", commands.get(commands.size() - 1));
    }
    // Each verify step reads its own stack's BaseUrl output.
    String json = template.toJSON().toString();
    for (String stage : List.of("StagingSendRecStaging", "ProductionSendRecProduction")) {
      assertTrue(java.util.regex.Pattern.compile("\"name\":\"BASE_URL\",\"type\":\"PLAINTEXT\",\"value\":\"#\\{SendRecPipeline"
          + stage + "[0-9A-F]{8}\\.BaseUrl}\"").matcher(json).find(), stage);
    }
  }

  @Test void thePipelineCreatesNoHoldAndNamesItsRelease() {
    template.resourceCountIs("AWS::SSM::Parameter", 0);
    template.hasResourceProperties("AWS::StepFunctions::StateMachine", Map.of("StateMachineName", "SendRecRelease"));
    template.hasResourceProperties("AWS::Scheduler::Schedule", Map.of(
        "ScheduleExpression", "cron(20 17 * * ? *)", "ScheduleExpressionTimezone", "Australia/Sydney"));
  }
}
