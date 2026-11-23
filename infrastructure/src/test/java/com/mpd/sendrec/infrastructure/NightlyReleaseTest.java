package com.mpd.sendrec.infrastructure;

import static org.junit.jupiter.api.Assertions.*;

import com.dashjoin.jsonata.Jsonata;
import com.dashjoin.jsonata.Utils;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.function.Consumer;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import software.amazon.awscdk.assertions.Template;

/**
 * The nightly release runs the synthesized state machine against a fake pipeline, as the staff
 * app's {@code test_nightly_release.py} does, and checks its triggers, alerts and grants.
 */
class NightlyReleaseTest {
  private static final ObjectMapper JSON = new ObjectMapper();
  private static final String RELEASE = "Nightly release 17:20 Australia/Sydney";
  private static final String CHANGED =
      "the pipeline changed while the release was checking it. The next release tries again, or approve the gate by hand.";
  private static Template template;
  private static JsonNode definition;

  @BeforeAll
  static void synth() throws Exception {
    template = Template.fromStack(PipelineFixture.pipeline(PipelineFixture.app(PipelineFixture.REVISION, PipelineFixture.DIGEST,
        Map.of("alarmEmailsStaging", "a@example.com,b@example.com", "alarmEmailsProduction", "c@example.com, c@example.com")),
        PipelineFixture.environment()));
    JsonNode machine = JSON.valueToTree(template.findResources("AWS::StepFunctions::StateMachine").values().iterator().next());
    definition = JSON.readTree(text(machine.path("Properties").path("DefinitionString")));
  }

  /** A template string, with each reference written as {@code <logical id>}. */
  private static String text(JsonNode value) {
    if (value.isTextual()) return value.asText();
    if (value.has("Fn::Join")) {
      StringBuilder joined = new StringBuilder();
      value.path("Fn::Join").get(1).forEach(part -> joined.append(text(part)));
      return joined.toString();
    }
    if (value.has("Ref")) return value.path("Ref").asText().equals("AWS::Partition") ? "aws" : "<" + value.path("Ref").asText() + ">";
    return "<" + value.path("Fn::GetAtt").get(0).asText() + ">";
  }

  // The fake pipeline --------------------------------------------------------------------------

  static final class AwsError extends RuntimeException {
    AwsError(String name) {
      super(name);
    }
  }

  static final class Run {
    final String id;
    String status;
    final boolean rollback;

    Run(String id, String status, boolean rollback) {
      this.id = id;
      this.status = status;
      this.rollback = rollback;
    }

    Map<String, Object> summary() {
      Map<String, Object> entry = new LinkedHashMap<>();
      entry.put("PipelineExecutionId", id);
      entry.put("Status", status);
      entry.put("StartTime", "2026-11-23T05:00:00Z");
      // Ordinary runs carry no ExecutionType, as live; rollbacks carry the documented markers.
      entry.put("Trigger", Map.of("TriggerType", rollback ? "ManualRollback" : "WebhookV2"));
      if (rollback) {
        entry.put("ExecutionType", "ROLLBACK");
        entry.put("RollbackMetadata", Map.of("RollbackTargetPipelineExecutionId", "P"));
      }
      return entry;
    }
  }

  static Run run(String id) {
    return new Run(id, "InProgress", false);
  }

  static Run run(String id, String status) {
    return new Run(id, status, false);
  }

  /**
   * The gate's run, the run waiting to enter it, the latest run to pass staging and the runs in
   * and waiting for production, with every run newest first. P is an older run, out of the list's
   * first rows, that last deployed production.
   */
  static final class Fake {
    final List<Run> runs;
    String gate;
    String inbound;
    String staging;
    String production = "P";
    String productionInbound;
    String gateStatus = "InProgress";
    String hold = "off";
    String holdError = "Ssm.ParameterNotFoundException";
    final Map<String, String> disabled = new HashMap<>();
    final List<String> approvalErrors = new ArrayList<>();
    boolean inboundNeverEnters;
    /** Superseded rows after each run, as live. */
    int padding = 1;
    /** Changes made just before the nth pipeline read. */
    final Map<Integer, Consumer<Fake>> beforeRead = new HashMap<>();
    final List<List<String>> calls = new ArrayList<>();
    final List<String> started = new ArrayList<>();
    final Map<String, Integer> counts = new HashMap<>();
    int reads;
    boolean entering;

    Fake(Run... runs) {
      this.runs = new ArrayList<>(List.of(runs));
    }

    Run find(String id) {
      return runs.stream().filter(r -> r.id.equals(id)).findFirst().orElseThrow();
    }

    String status(String id) {
      return runs.stream().filter(r -> r.id.equals(id)).map(r -> r.status).findFirst().orElse("Succeeded");
    }

    @SuppressWarnings("unchecked")
    Object call(String resource, Map<String, Object> arguments) {
      String action = resource.replace("arn:aws:states:::", "").replace("aws-sdk:", "");
      counts.merge(action, 1, Integer::sum);
      switch (action) {
        case "ssm:getParameter" -> {
          assertEquals(Map.of("Name", NightlyRelease.HOLD_PARAMETER), arguments);
          if (hold == null) throw new AwsError(holdError);
          return Map.of("Parameter", Map.of("Name", NightlyRelease.HOLD_PARAMETER, "Value", hold));
        }
        case "sfn:startExecution" -> {
          assertEquals("{\"mode\":\"release\"}", arguments.get("Input"));
          String name = (String) arguments.get("Name");
          if (started.contains(name)) throw new AwsError("Sfn.ExecutionAlreadyExistsException");
          started.add(name);
          return Map.of("ExecutionArn", name, "StartDate", "now");
        }
        case "sns:publish" -> {
          calls.add(List.of("Published", (String) arguments.get("Message")));
          return Map.of("MessageId", "1");
        }
        case "codepipeline:getPipelineState" -> {
          return state();
        }
        case "codepipeline:listPipelineExecutions" -> {
          List<Run> history = new ArrayList<>(runs);
          if (production != null && runs.stream().noneMatch(r -> r.id.equals(production))) history.add(NightlyReleaseTest.run(production, "Succeeded"));
          List<Object> listed = new ArrayList<>();
          for (Run r : history) {
            listed.add(r.summary());
            for (int n = 1; n <= padding; n++) listed.add(NightlyReleaseTest.run(r.id + "." + n, "Superseded").summary());
          }
          int start = arguments.containsKey("NextToken") ? Integer.parseInt((String) arguments.get("NextToken")) : 0;
          int end = Math.min(start + ((Number) arguments.get("MaxResults")).intValue(), listed.size());
          Map<String, Object> page = new LinkedHashMap<>();
          page.put("PipelineExecutionSummaries", listed.subList(start, end));
          if (end < listed.size()) page.put("NextToken", String.valueOf(end));
          return page;
        }
        case "codepipeline:putApprovalResult" -> {
          return decide(arguments);
        }
        default -> throw new AssertionError("unexpected call " + action);
      }
    }

    Map<String, Object> stage(String name) {
      Map<String, Object> stage = new LinkedHashMap<>();
      stage.put("StageName", name);
      // The plural list is always there, and empty.
      stage.put("InboundExecutions", List.of());
      Map<String, Object> transition = new LinkedHashMap<>();
      transition.put("Enabled", !disabled.containsKey(name));
      if (disabled.containsKey(name)) transition.put("DisabledReason", disabled.get(name));
      stage.put("InboundTransitionState", transition);
      return stage;
    }

    Map<String, Object> state() {
      reads++;
      Consumer<Fake> change = beforeRead.remove(reads);
      if (change != null) change.accept(this);
      if (entering && !inboundNeverEnters) {
        // The waiting run enters one read after the gate's run is decided.
        entering = false;
        gate = inbound;
        inbound = null;
        gateStatus = "InProgress";
      } else if (!"InProgress".equals(gateStatus) && inbound != null) {
        entering = !disabled.containsKey(NightlyRelease.APPROVAL_STAGE);
      }
      List<Object> stages = new ArrayList<>();
      for (String name : List.of("Source", "Build", "UpdatePipeline")) {
        Map<String, Object> early = stage(name);
        early.put("LatestExecution", Map.of("PipelineExecutionId", runs.get(0).id, "Status", "Succeeded"));
        early.put("ActionStates", List.of(Map.of("ActionName", name,
            "LatestExecution", Map.of("ActionExecutionId", name + "-" + runs.get(0).id, "Status", "Succeeded"))));
        stages.add(early);
      }
      Map<String, Object> stagingStage = stage(NightlyRelease.STAGING_STAGE);
      if (staging != null) stagingStage.put("LatestExecution", Map.of("PipelineExecutionId", staging, "Status", "Succeeded"));
      stages.add(stagingStage);
      Map<String, Object> gateStage = stage(NightlyRelease.APPROVAL_STAGE);
      if (gate != null) {
        gateStage.put("LatestExecution", Map.of("PipelineExecutionId", gate, "Status", gateStatus));
        Map<String, Object> latest = new LinkedHashMap<>(Map.of("ActionExecutionId", "approval-" + gate, "Status", gateStatus));
        if ("InProgress".equals(gateStatus)) latest.put("Token", "token-" + gate);
        gateStage.put("ActionStates", List.of(Map.of("ActionName", NightlyRelease.APPROVAL_ACTION, "LatestExecution", latest)));
      }
      if (inbound != null) gateStage.put("InboundExecution", Map.of("PipelineExecutionId", inbound, "Status", find(inbound).status));
      stages.add(gateStage);
      Map<String, Object> productionStage = stage(NightlyRelease.PRODUCTION_STAGE);
      if (production != null) productionStage.put("LatestExecution", Map.of("PipelineExecutionId", production, "Status", status(production)));
      if (productionInbound != null) {
        productionStage.put("InboundExecution", Map.of("PipelineExecutionId", productionInbound, "Status", "InProgress"));
      }
      stages.add(productionStage);
      return Map.of("StageStates", stages);
    }

    @SuppressWarnings("unchecked")
    Map<String, Object> decide(Map<String, Object> arguments) {
      assertEquals(NightlyRelease.APPROVAL_STAGE, arguments.get("StageName"));
      assertEquals(NightlyRelease.APPROVAL_ACTION, arguments.get("ActionName"));
      assertEquals(NightlyRelease.PIPELINE_NAME, arguments.get("PipelineName"));
      if (!approvalErrors.isEmpty()) {
        // Someone else decided first, so the state machine's token is stale.
        gateStatus = "Succeeded";
        throw new AwsError(approvalErrors.remove(0));
      }
      assertEquals("token-" + gate, arguments.get("Token"));
      Map<String, Object> result = (Map<String, Object>) arguments.get("Result");
      calls.add(List.of((String) result.get("Status"), (String) result.get("Summary"), gate));
      if ("Rejected".equals(result.get("Status"))) {
        gateStatus = "Failed";
        find(gate).status = "Failed";
      } else {
        gateStatus = "Succeeded";
      }
      return Map.of();
    }
  }

  // The interpreter --------------------------------------------------------------------------

  private static Object toJava(JsonNode node) {
    return JSON.convertValue(node, Object.class);
  }

  /** A field as Step Functions evaluates it: {@code {% ... %}} strings are JSONata. */
  @SuppressWarnings("unchecked")
  private static Object evaluate(Object value, Map<String, Object> variables, Map<String, Object> states) {
    if (value instanceof Map<?, ?> map) {
      Map<String, Object> evaluated = new LinkedHashMap<>();
      map.forEach((key, item) -> evaluated.put((String) key, evaluate(item, variables, states)));
      return evaluated;
    }
    if (!(value instanceof String text) || !text.startsWith("{%")) return value;
    Jsonata expression = Jsonata.jsonata(text.substring(2, text.length() - 2));
    Jsonata.Frame frame = expression.createFrame();
    Map<String, Object> all = new HashMap<>(variables);
    all.put("states", states);
    all.forEach((name, bound) -> frame.bind(name, bound == null ? Jsonata.NULL_VALUE : Utils.convertNulls(bound)));
    return expression.evaluate(null, frame);
  }

  @SuppressWarnings("unchecked")
  private static void assign(JsonNode state, Map<String, Object> variables, Map<String, Object> states) {
    // Every expression reads the variables as they were before the state.
    if (state.has("Assign")) variables.putAll((Map<String, Object>) evaluate(toJava(state.path("Assign")), variables, states));
  }

  /** Runs the definition until it ends; returns {@code Succeeded} or the error. */
  @SuppressWarnings("unchecked")
  static String execute(Fake pipeline, String mode, Map<String, Object> given) {
    Map<String, Object> variables = new HashMap<>();
    String name = definition.path("StartAt").asText();
    for (int step = 0; step < 400; step++) {
      JsonNode state = definition.path("States").path(name);
      String kind = state.path("Type").asText();
      switch (kind) {
        case "Succeed" -> {
          return "Succeeded";
        }
        case "Fail" -> {
          return state.path("Error").asText();
        }
        case "Choice" -> {
          name = state.path("Default").asText(null);
          for (JsonNode choice : state.path("Choices")) {
            Object matched = evaluate(choice.path("Condition").asText(), variables, Map.of());
            assertTrue(matched instanceof Boolean, choice.path("Condition").asText() + " gave " + matched);
            if ((Boolean) matched) {
              name = choice.path("Next").asText();
              break;
            }
          }
          continue;
        }
        case "Pass" -> {
          Map<String, Object> input = new HashMap<>(given);
          input.put("mode", mode);
          assign(state, variables, Map.of("input", input));
        }
        case "Task" -> {
          Map<String, Object> arguments = (Map<String, Object>) evaluate(toJava(state.path("Arguments")), variables, Map.of());
          int retries = 0;
          for (JsonNode retry : state.path("Retry")) retries += retry.path("MaxAttempts").asInt();
          Object result = null;
          AwsError failure = null;
          for (int attempt = 0; attempt <= retries; attempt++) {
            try {
              result = pipeline.call(state.path("Resource").asText(), arguments);
              failure = null;
              break;
            } catch (AwsError error) {
              failure = error;
            }
          }
          if (failure != null) {
            assertEquals(1, state.path("Catch").size(), name + " has no catch for " + failure.getMessage());
            JsonNode handler = state.path("Catch").get(0);
            assertEquals("[\"States.ALL\"]", handler.path("ErrorEquals").toString());
            assign(handler, variables, Map.of("errorOutput", Map.of("Error", failure.getMessage(), "Cause", "fake")));
            name = handler.path("Next").asText();
            continue;
          }
          assign(state, variables, Map.of("result", result));
        }
        case "Wait" -> {}
        default -> throw new AssertionError("unexpected state type " + kind);
      }
      if (state.path("End").asBoolean(false)) return "Succeeded";
      name = state.path("Next").asText();
    }
    throw new AssertionError("The state machine did not end");
  }

  static String execute(Fake pipeline, String mode) {
    return execute(pipeline, mode, Map.of());
  }

  private static List<String> skipped(String reason) {
    return List.of("Published", "The 17:20 Australia/Sydney release of SendRec was skipped because " + reason);
  }

  /** Run A at the gate, the newest run, with an older one already shipped. */
  private static Fake waiting() {
    Fake fake = new Fake(run("A"), run("Z", "Succeeded"));
    fake.gate = "A";
    fake.staging = "A";
    return fake;
  }

  // Scenarios --------------------------------------------------------------------------------

  @Test void anUnheldReleaseApprovesTheRunAtTheGate() {
    Fake fake = waiting();
    assertEquals("Succeeded", execute(fake, "release"));
    assertEquals(List.of(List.of("Approved", RELEASE, "A")), fake.calls);
    // A hold read, then the pipeline, then the hold and the pipeline again before approving.
    assertEquals(2, fake.counts.get("ssm:getParameter"));
    assertEquals(2, fake.counts.get("codepipeline:getPipelineState"));
  }

  @Test void resolveNeverApprovesAndNeverReadsTheHold() {
    Fake fake = waiting();
    assertEquals("Succeeded", execute(fake, "resolve"));
    assertEquals(List.of(), fake.calls);
    assertNull(fake.counts.get("ssm:getParameter"));
  }

  @Test void oneScheduledFiringStartsOneRelease() {
    Fake fake = waiting();
    Map<String, Object> firing = Map.of("scheduledTime", "2026-11-23T06:20:00Z");
    assertEquals("Succeeded", execute(fake, "schedule", firing));
    assertEquals("Succeeded", execute(fake, "schedule", firing));
    assertEquals(List.of("release-2026-11-23T062000Z"), fake.started);
    assertEquals(List.of(), fake.calls, "a scheduled firing only starts the release");
  }

  @Test void onlyAnExactOffLetsAReleaseApprove() {
    for (String hold : List.of("on", "Off", " off", "off ", "on: first release")) {
      Fake fake = waiting();
      fake.hold = hold;
      assertEquals("Succeeded", execute(fake, "release"), hold);
      // The value is quoted as JSON, so a stray space or capital shows.
      assertEquals(List.of(skipped("/sendrec/release/hold is " + JSON.valueToTree(hold) + ". Only off releases.")),
          fake.calls, hold);
    }
    Fake missing = waiting();
    missing.hold = null;
    assertEquals("Succeeded", execute(missing, "release"));
    assertEquals(List.of(skipped("hold parameter /sendrec/release/hold is not set. Only off releases.")), missing.calls);
  }

  @Test void anUnreadableHoldFailsTheJobAndApprovesNothing() {
    Fake fake = waiting();
    fake.hold = null;
    fake.holdError = "Ssm.AccessDeniedException";
    assertEquals("UnexpectedError", execute(fake, "release"));
    assertEquals(List.of(), fake.calls);
  }

  @Test void aReleaseWithAnEmptyGateDoesNothing() {
    Fake fake = new Fake(run("Z", "Succeeded"));
    fake.staging = "Z";
    assertEquals("Succeeded", execute(fake, "release"));
    assertEquals(List.of(), fake.calls);
  }

  @Test void aNewerRunSupersedesTheOneAtTheGateAndShipsInItsPlace() {
    for (String mode : List.of("resolve", "release")) {
      Fake fake = new Fake(run("B"), run("A"), run("Z", "Succeeded"));
      fake.gate = "A";
      fake.inbound = "B";
      fake.staging = "B";
      assertEquals("Succeeded", execute(fake, mode), mode);
      List<List<String>> expected = new ArrayList<>(List.of(List.of("Rejected", "Superseded by B", "A")));
      if (mode.equals("release")) expected.add(List.of("Approved", RELEASE, "B"));
      assertEquals(expected, fake.calls, mode);
    }
  }

  @Test void aGateRunPastTheFirstPageIsFoundByPaging() {
    Fake fake = new Fake(run("B"), run("A"), run("Z", "Succeeded"));
    fake.gate = "A";
    fake.inbound = "B";
    fake.staging = "B";
    fake.padding = 60;
    assertEquals("Succeeded", execute(fake, "resolve"));
    assertEquals(List.of(List.of("Rejected", "Superseded by B", "A")), fake.calls);
    assertTrue(fake.counts.get("codepipeline:listPipelineExecutions") >= 2);
  }

  @Test void aNewerRollbackNeverSupersedesAndHoldsTheRelease() {
    Fake fake = new Fake(new Run("R", "Succeeded", true), run("A"), run("Z", "Succeeded"));
    fake.gate = "A";
    fake.staging = "A";
    assertEquals("Succeeded", execute(fake, "release"));
    assertEquals(List.of(skipped("run R, newer than the gate's run, is a rollback or of unknown kind.")), fake.calls);
  }

  @Test void aRunWaitingForProductionHoldsTheRelease() {
    Fake fake = new Fake(run("A"), run("Q"), run("Z", "Succeeded"));
    fake.gate = "A";
    fake.staging = "A";
    fake.productionInbound = "Q";
    assertEquals("Succeeded", execute(fake, "release"));
    assertEquals(List.of(skipped("run Q has not finished deploying to Production.")), fake.calls);
  }

  @Test void productionWithoutARecordedRunHoldsUntilTheFirstApprovalByHand() {
    Fake fake = waiting();
    fake.production = null;
    assertEquals("Succeeded", execute(fake, "release"));
    assertEquals(List.of(skipped("Production has no recorded run yet. Approve the first production deploy by hand;"
        + " nightly releases follow.")), fake.calls);
  }

  @Test void aDisabledTransitionHoldsTheReleaseAndNamesItsReason() {
    for (String stage : List.of("ApproveProduction", "Production")) {
      Fake fake = waiting();
      fake.disabled.put(stage, "maintenance window");
      assertEquals("Succeeded", execute(fake, "release"), stage);
      assertEquals(List.of(skipped("the transition into " + stage + " is disabled: maintenance window.")), fake.calls, stage);
    }
  }

  @Test void anyChangeInThePipelineBeforeApprovalHoldsTheRelease() {
    Fake fake = waiting();
    // A newer run passes staging between the read that chose A and the read that confirms it.
    fake.beforeRead.put(2, f -> {
      f.runs.add(0, run("C", "InProgress"));
      f.staging = "C";
    });
    assertEquals("Succeeded", execute(fake, "release"));
    assertEquals(List.of(skipped(CHANGED)), fake.calls);
  }

  @Test void anApprovalSomeoneMadeFirstEndsTheReleaseAndOtherErrorsFailIt() {
    Fake stale = waiting();
    stale.approvalErrors.add("CodePipeline.ApprovalAlreadyCompletedException");
    assertEquals("ApprovalUncertain", execute(stale, "release"));
    Fake denied = waiting();
    denied.approvalErrors.add("CodePipeline.AccessDeniedException");
    assertEquals("UnexpectedError", execute(denied, "release"));
    assertEquals(1, denied.counts.get("codepipeline:putApprovalResult"), "an approval is never retried");
  }

  @Test void theWaitForARunToEnterTheGateIsCapped() {
    Fake fake = new Fake(run("B"), run("A", "Failed"), run("Z", "Succeeded"));
    fake.gate = "A";
    fake.gateStatus = "Failed";
    fake.inbound = "B";
    fake.staging = "B";
    fake.inboundNeverEnters = true;
    assertEquals("ResolveLoopCapped", execute(fake, "resolve"));
    assertEquals(10, fake.counts.get("codepipeline:getPipelineState"));
  }

  // Wiring -----------------------------------------------------------------------------------

  @Test void decisionsAreNeverRetriedAndEveryReadIs() {
    definition.path("States").properties().forEach(entry -> {
      String resource = entry.getValue().path("Resource").asText();
      if (resource.contains("putApprovalResult")) assertTrue(entry.getValue().path("Retry").isMissingNode(), entry.getKey());
      else if (resource.contains("getParameter") || resource.contains("listPipelineExecutions") || resource.contains("sns:publish")
          || resource.contains("startExecution") || entry.getKey().equals("ReadPipelineState")) {
        assertTrue(entry.getValue().path("Retry").size() > 0, entry.getKey());
      }
    });
    assertEquals("JSONata", definition.path("QueryLanguage").asText());
    assertEquals(900, definition.path("TimeoutSeconds").asInt());
  }

  @Test void theScheduleStartsAtMostOneReleaseAt1720Sydney() {
    JsonNode schedule = JSON.valueToTree(template.findResources("AWS::Scheduler::Schedule").values().iterator().next()).path("Properties");
    assertEquals("cron(20 17 * * ? *)", schedule.path("ScheduleExpression").asText());
    assertEquals("Australia/Sydney", schedule.path("ScheduleExpressionTimezone").asText());
    assertEquals(JSON.createObjectNode().put("mode", "schedule").put("scheduledTime", "<aws.scheduler.scheduled-time>"),
        readTree(schedule.path("Target").path("Input").asText()));
    assertEquals(900, schedule.path("Target").path("RetryPolicy").path("MaximumEventAgeInSeconds").asInt());
  }

  private static JsonNode readTree(String json) {
    try {
      return JSON.readTree(json);
    } catch (com.fasterxml.jackson.core.JsonProcessingException error) {
      throw new AssertionError(error);
    }
  }

  @Test void stageFailuresAndFailedReleaseJobsEmailTheirLists() {
    Map<String, Map<String, Object>> rules = template.findResources("AWS::Events::Rule");
    List<String> patterns = new ArrayList<>();
    rules.values().forEach(rule -> {
      JsonNode detail = JSON.valueToTree(rule).path("Properties").path("EventPattern").path("detail");
      patterns.add(detail.has("stage") ? detail.path("stage") + " " + detail.path("state") : detail.path("status").toString());
    });
    patterns.sort(null);
    assertEquals(List.of("[\"FAILED\",\"TIMED_OUT\",\"ABORTED\"]", "[\"Production\"] [\"FAILED\"]",
        "[\"Source\",\"Build\",\"UpdatePipeline\",\"Staging\"] [\"FAILED\"]", "[\"Staging\"] [\"SUCCEEDED\"]"), patterns);
    template.hasResourceProperties("AWS::SNS::Topic", Map.of("TopicName", "sendrec-pipeline-staging"));
    template.hasResourceProperties("AWS::SNS::Topic", Map.of("TopicName", "sendrec-pipeline-production"));
    List<String> subscriptions = new ArrayList<>();
    template.findResources("AWS::SNS::Subscription").values().forEach(s -> subscriptions.add(
        String.valueOf(((Map<?, ?>) s.get("Properties")).get("Endpoint"))));
    subscriptions.sort(null);
    assertEquals(List.of("a@example.com", "b@example.com", "c@example.com"), subscriptions);
    String policies = JSON.valueToTree(template.findResources("AWS::SNS::TopicPolicy")).toString();
    assertTrue(policies.contains("\"aws:SourceArn\":\"arn:aws:events:ap-southeast-2:999988887777:rule/*\""), policies);
    assertTrue(policies.contains("\"aws:SourceAccount\":\"999988887777\""), policies);
  }

  @Test void alarmListsParseToDistinctAddresses() {
    assertEquals(List.of(), NightlyRelease.parseAlarmEmails("staging", null));
    assertEquals(List.of(), NightlyRelease.parseAlarmEmails("staging", " NONE "));
    assertEquals(List.of("a@x.test", "b@x.test"), NightlyRelease.parseAlarmEmails("staging", "a@x.test, b@x.test,,a@x.test"));
    assertThrows(IllegalArgumentException.class, () -> NightlyRelease.parseAlarmEmails("production", "a@x.test,nobody"));
  }

  @Test void theReleaseRoleHasOnlyItsGrants() {
    String machineRole = JSON.valueToTree(template.findResources("AWS::StepFunctions::StateMachine").values().iterator().next())
        .path("Properties").path("RoleArn").path("Fn::GetAtt").get(0).asText();
    List<String> actions = new ArrayList<>();
    for (var policy : template.findResources("AWS::IAM::Policy").values()) {
      JsonNode properties = JSON.valueToTree(policy.get("Properties"));
      if (!properties.path("Roles").toString().contains(machineRole)) continue;
      properties.path("PolicyDocument").path("Statement").forEach(s -> {
        JsonNode action = s.path("Action");
        if (action.isArray()) action.forEach(a -> actions.add(a.asText())); else actions.add(action.asText());
      });
    }
    actions.sort(null);
    assertEquals(List.of("codepipeline:GetPipelineState", "codepipeline:ListPipelineExecutions", "codepipeline:PutApprovalResult",
        "sns:Publish", "ssm:GetParameter", "states:StartExecution"), actions.stream().distinct().toList());
  }
}
