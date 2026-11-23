package com.mpd.sendrec.infrastructure;

import java.util.Arrays;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import software.amazon.awscdk.Duration;
import software.amazon.awscdk.Stack;
import software.amazon.awscdk.TimeZone;
import software.amazon.awscdk.services.events.EventField;
import software.amazon.awscdk.services.events.EventPattern;
import software.amazon.awscdk.services.events.Rule;
import software.amazon.awscdk.services.events.RuleTargetInput;
import software.amazon.awscdk.services.events.targets.SfnStateMachine;
import software.amazon.awscdk.services.events.targets.SfnStateMachineProps;
import software.amazon.awscdk.services.events.targets.SnsTopic;
import software.amazon.awscdk.services.events.targets.SnsTopicProps;
import software.amazon.awscdk.services.iam.Effect;
import software.amazon.awscdk.services.iam.PolicyStatement;
import software.amazon.awscdk.services.iam.ServicePrincipal;
import software.amazon.awscdk.services.scheduler.ContextAttribute;
import software.amazon.awscdk.services.scheduler.CronOptionsWithTimezone;
import software.amazon.awscdk.services.scheduler.Schedule;
import software.amazon.awscdk.services.scheduler.ScheduleExpression;
import software.amazon.awscdk.services.scheduler.ScheduleTargetInput;
import software.amazon.awscdk.services.scheduler.targets.ScheduleTargetBaseProps;
import software.amazon.awscdk.services.scheduler.targets.StepFunctionsStartExecution;
import software.amazon.awscdk.services.sns.Topic;
import software.amazon.awscdk.services.sns.subscriptions.EmailSubscription;
import software.amazon.awscdk.services.stepfunctions.CatchProps;
import software.amazon.awscdk.services.stepfunctions.Choice;
import software.amazon.awscdk.services.stepfunctions.Condition;
import software.amazon.awscdk.services.stepfunctions.DefinitionBody;
import software.amazon.awscdk.services.stepfunctions.Fail;
import software.amazon.awscdk.services.stepfunctions.FailJsonataProps;
import software.amazon.awscdk.services.stepfunctions.IChainable;
import software.amazon.awscdk.services.stepfunctions.Pass;
import software.amazon.awscdk.services.stepfunctions.PassJsonataProps;
import software.amazon.awscdk.services.stepfunctions.QueryLanguage;
import software.amazon.awscdk.services.stepfunctions.RetryProps;
import software.amazon.awscdk.services.stepfunctions.StateMachine;
import software.amazon.awscdk.services.stepfunctions.Succeed;
import software.amazon.awscdk.services.stepfunctions.TaskInput;
import software.amazon.awscdk.services.stepfunctions.TaskStateBase;
import software.amazon.awscdk.services.stepfunctions.Wait;
import software.amazon.awscdk.services.stepfunctions.WaitJsonataProps;
import software.amazon.awscdk.services.stepfunctions.WaitTime;
import software.amazon.awscdk.services.stepfunctions.tasks.CallAwsService;
import software.amazon.awscdk.services.stepfunctions.tasks.CallAwsServiceJsonataProps;
import software.amazon.awscdk.services.stepfunctions.tasks.SnsPublish;
import software.amazon.awscdk.services.stepfunctions.tasks.SnsPublishJsonataProps;
import software.constructs.Construct;

/**
 * The nightly production release and the pipeline's failure alerts: a port of the staff app's
 * {@code staff_infrastructure/nightly_release.py} on main (mpd-os-staff #604 and #618), which is
 * MPD API's release (mpd-api #609, #620, change {@code add-nightly-production-release}) made
 * stricter about when it approves. Only names and the release time differ (design D7).
 *
 * <p>{@code SendRecRelease} runs in three modes. {@code resolve} starts whenever {@code Staging}
 * succeeds and only rejects a superseded run at the gate. {@code schedule} starts at
 * {@link #RELEASE_TIME} and starts one {@code release} execution named after the firing. {@code
 * release} resolves and then, unless a hold is on, approves the gate's run.
 */
final class NightlyRelease extends Construct {
  static final String PIPELINE_NAME = "SendRec";
  static final String STAGING_STAGE = "Staging";
  static final String APPROVAL_STAGE = "ApproveProduction";
  static final String APPROVAL_ACTION = "ApproveProduction";
  static final String PRODUCTION_STAGE = "Production";
  /** Stages whose failure emails the staging list, and those that email production's. */
  static final List<String> PRE_GATE_STAGES = List.of("Source", "Build", "UpdatePipeline", STAGING_STAGE);
  static final List<String> PRODUCTION_STAGES = List.of(PRODUCTION_STAGE);
  /** After the staff app's 17:10 (design D8). */
  static final int RELEASE_HOUR = 17;
  static final int RELEASE_MINUTE = 20;
  static final String RELEASE_TIME_ZONE = "Australia/Sydney";
  static final String RELEASE_TIME = RELEASE_HOUR + ":" + String.format("%02d", RELEASE_MINUTE) + " " + RELEASE_TIME_ZONE;
  /** Only "off" lets the release approve. CDK never creates or changes it, so a deploy cannot lift a hold. */
  static final String HOLD_PARAMETER = "/sendrec/release/hold";
  static final String RELEASE_MACHINE = PIPELINE_NAME + "Release";
  /** Runs read per page, newest first, and the most pages read for one pipeline read. */
  static final int RUNS_READ = 50;
  static final int PAGES_READ = 6;
  /** A list entry that shows an ordinary run: triggered, and with no rollback marker. */
  static final String ORDINARY = "$exists(Trigger.TriggerType)"
      + " and $not(Trigger.TriggerType in [\"ManualRollback\", \"AutomatedRollback\"])"
      + " and ($not($exists(ExecutionType)) or ExecutionType = \"STANDARD\")"
      + " and $not($exists(RollbackMetadata))";
  /** The statuses CodePipeline documents for a run. */
  static final String STATUSES =
      "[\"Cancelled\", \"InProgress\", \"Stopped\", \"Stopping\", \"Succeeded\", \"Superseded\", \"Failed\"]";
  /** The statuses of a run that has ended. */
  static final String FINISHED = "[\"Cancelled\", \"Stopped\", \"Failed\", \"Superseded\", \"Succeeded\"]";
  /** Only an approval still waiting has a usable token (cortex-ai #132). */
  static final String GATE_TOKEN = "$gate.ActionStates[ActionName = \"" + APPROVAL_ACTION
      + "\" and LatestExecution.Status = \"InProgress\"].LatestExecution.Token";

  private final String region;
  private final String account;
  private final String pipelineArn;
  private final String releaseMachineArn;
  private final String console;

  NightlyRelease(Construct scope, String id, List<String> stagingEmails, List<String> productionEmails) {
    super(scope, id);
    Stack stack = Stack.of(this);
    region = stack.getRegion();
    account = stack.getAccount();
    pipelineArn = "arn:aws:codepipeline:" + region + ":" + account + ":" + PIPELINE_NAME;
    // Built from the name, since the state machine's own reference would be circular.
    releaseMachineArn = "arn:aws:states:" + region + ":" + account + ":stateMachine:" + RELEASE_MACHINE;
    console = "https://" + region + ".console.aws.amazon.com";

    Topic stagingTopic = topic("StagingTopic", "staging", stagingEmails);
    Topic productionTopic = topic("ProductionTopic", "production", productionEmails);
    alertOnFailedStages("PreGateFailureRule", PRE_GATE_STAGES, stagingTopic);
    alertOnFailedStages("ProductionFailureRule", PRODUCTION_STAGES, productionTopic);
    StateMachine release = stateMachine(productionTopic);
    Rule.Builder.create(this, "ReleaseJobFailureRule")
        .eventPattern(EventPattern.builder()
            .source(List.of("aws.states"))
            .detailType(List.of("Step Functions Execution Status Change"))
            .detail(ordered("status", List.of("FAILED", "TIMED_OUT", "ABORTED"),
                "stateMachineArn", List.of(release.getStateMachineArn())))
            .build())
        .targets(List.of(new SnsTopic(productionTopic, SnsTopicProps.builder()
            .message(RuleTargetInput.fromText(PIPELINE_NAME + " nightly release job "
                + EventField.fromPath("$.detail.status") + ": " + console + "/states/home?region=" + region
                + "#/v2/executions/details/" + EventField.fromPath("$.detail.executionArn")))
            .build())))
        .build();
    Rule.Builder.create(this, "ResolveGateRule")
        .eventPattern(stagePattern(List.of(STAGING_STAGE), "SUCCEEDED"))
        .targets(List.of(new SfnStateMachine(release, SfnStateMachineProps.builder()
            .input(RuleTargetInput.fromObject(Map.of("mode", "resolve"))).build())))
        .build();
    Schedule.Builder.create(this, "Schedule")
        .schedule(ScheduleExpression.cron(CronOptionsWithTimezone.builder()
            .minute(String.valueOf(RELEASE_MINUTE)).hour(String.valueOf(RELEASE_HOUR))
            .timeZone(TimeZone.of(RELEASE_TIME_ZONE)).build()))
        // The firing starts the release under a name made from its scheduled time, so a retry or
        // a second delivery cannot start a second release.
        .target(new StepFunctionsStartExecution(release, ScheduleTargetBaseProps.builder()
            .input(ScheduleTargetInput.fromObject(ordered(
                "mode", "schedule", "scheduledTime", ContextAttribute.getScheduledTime())))
            // Never retry a failed start into a release in the middle of the night.
            .maxEventAge(Duration.minutes(15))
            .build()))
        .build();
  }

  /** Alarm recipients from a comma-separated value, duplicates dropped. Missing, blank or `none` means none. */
  static List<String> parseAlarmEmails(String environment, String raw) {
    if (raw == null || raw.isBlank() || raw.strip().equalsIgnoreCase("none")) return List.of();
    List<String> emails = Arrays.stream(raw.split(",")).map(String::strip).filter(e -> !e.isEmpty()).distinct().toList();
    if (emails.stream().anyMatch(e -> !e.contains("@")))
      throw new IllegalArgumentException("Alarm email for " + environment + " is not an address");
    return emails;
  }

  private static String q(String expression) {
    return "{% " + expression + " %}";
  }

  /** Retries any error, 2 s apart and doubling, before the state's catch runs. */
  private static void retry(TaskStateBase state, int attempts) {
    state.addRetry(RetryProps.builder().errors(List.of("States.ALL")).maxAttempts(attempts)
        .interval(Duration.seconds(2)).backoffRate(2.0).build());
  }

  /**
   * An SNS topic emailing each address. EventBridge may publish to it only from rules in this
   * account, since the rule target's grant has no condition. The deny needs both keys to be
   * foreign, so a key EventBridge leaves out never blocks our own rules.
   */
  private Topic topic(String id, String environment, List<String> emails) {
    Topic topic = Topic.Builder.create(this, id).topicName("sendrec-pipeline-" + environment).build();
    emails.forEach(email -> topic.addSubscription(new EmailSubscription(email)));
    topic.addToResourcePolicy(PolicyStatement.Builder.create()
        .effect(Effect.DENY)
        .principals(List.of(new ServicePrincipal("events.amazonaws.com")))
        .actions(List.of("sns:Publish"))
        .resources(List.of(topic.getTopicArn()))
        .conditions(ordered(
            "ArnNotLike", Map.of("aws:SourceArn", "arn:aws:events:" + region + ":" + account + ":rule/*"),
            "StringNotEquals", Map.of("aws:SourceAccount", account)))
        .build());
    return topic;
  }

  private static EventPattern stagePattern(List<String> stages, String state) {
    return EventPattern.builder()
        .source(List.of("aws.codepipeline"))
        .detailType(List.of("CodePipeline Stage Execution State Change"))
        .detail(ordered("pipeline", List.of(PIPELINE_NAME), "stage", stages, "state", List.of(state)))
        .build();
  }

  private void alertOnFailedStages(String id, List<String> stages, Topic topic) {
    String execution = EventField.fromPath("$.detail.execution-id");
    Rule.Builder.create(this, id)
        .eventPattern(stagePattern(stages, "FAILED"))
        .targets(List.of(new SnsTopic(topic, SnsTopicProps.builder()
            .message(RuleTargetInput.fromText(EventField.fromPath("$.detail.pipeline") + " stage "
                + EventField.fromPath("$.detail.stage") + " failed in execution " + execution + ": " + console
                + "/codesuite/codepipeline/pipelines/" + PIPELINE_NAME + "/executions/" + execution
                + "/timeline?region=" + region))
            .build())))
        .build();
  }

  /**
   * The staff app's release state machine with SendRec's names and cortex-ai #132's fixes: a
 * finished run still shown waiting at the gate is not waited for, the approval snapshot covers
 * only Staging, the gate and Production, the token is read only from a waiting approval, and a
 * changed confirmation reads again within the cap instead of skipping. Its comments carry over: see
   * {@code NightlyRelease._state_machine} in mpd-os-staff for the full reasoning. In short: a run
   * supersedes the gate's only when its list entry shows an ordinary run, it started after the
   * gate's run, and it either shipped or is still in progress as a candidate. A release approves
   * only with the hold exactly {@code off}, both transitions enabled, nothing in or waiting for
   * {@code Production}, and an unchanged pipeline between the read that chose the run and the read
   * that confirms it. Decisions are never retried. At most 10 reads start a decision.
   */
  private StateMachine stateMachine(Topic productionTopic) {
    String gateRun = "$gate.LatestExecution.PipelineExecutionId";
    // A waiting run can be reported in either field; both are read. The plural list is often
    // present and empty, so it is counted, never tested to exist.
    String gateInbound = "[$gate.InboundExecution, $gate.InboundExecutions]";
    String stagingRun = "$staging.LatestExecution.PipelineExecutionId";
    String gateInProgress = "$gate.LatestExecution.Status = \"InProgress\"";
    // A transition counts as enabled only when the response says so.
    String enabled = "$gate.InboundTransitionState.Enabled = true";
    Fail unexpected = Fail.jsonata(this, "UnexpectedError", FailJsonataProps.builder()
        .error("UnexpectedError")
        .cause("A release step failed with an error other than a missing hold or a stale token").build());
    Succeed done = Succeed.jsonata(this, "Done");
    Wait firstWait = Wait.jsonata(this, "WaitForGate",
        WaitJsonataProps.builder().time(WaitTime.duration(Duration.seconds(15))).build());
    Wait waitAgain = Wait.jsonata(this, "WaitBeforeRereading",
        WaitJsonataProps.builder().time(WaitTime.duration(Duration.seconds(15))).build());

    // The gate and the stages around it, from one GetPipelineState.
    Map<String, Object> pipelineState = new LinkedHashMap<>();
    pipelineState.put("gate", q("$states.result.StageStates[StageName = \"" + APPROVAL_STAGE + "\"]"));
    pipelineState.put("staging", q("$states.result.StageStates[StageName = \"" + STAGING_STAGE + "\"]"));
    pipelineState.put("arrow", q("($off := $states.result.StageStates["
        + "(StageName = \"" + APPROVAL_STAGE + "\""
        + " or StageName = \"" + PRODUCTION_STAGE + "\")"
        + " and $not(InboundTransitionState.Enabled = true)];"
        + " $exists($off) ? $off[0].{\"stage\": StageName,"
        + " \"disabled\": InboundTransitionState.Enabled = false,"
        + " \"reason\": InboundTransitionState.DisabledReason} : null)"));
    // A run in or waiting for production, which an approval could follow. A waiting run that has
    // already finished, such as one cancelled when the pipeline changed, is not deploying; one with
    // no status still counts.
    pipelineState.put("deploying", q("($p := $states.result.StageStates"
        + "[StageName = \"" + PRODUCTION_STAGE + "\"];"
        + " $in := [$p.InboundExecution, $p.InboundExecutions];"
        + " $waiting := $in[$not(Status in " + FINISHED + ")];"
        + " $exists($waiting) ? $waiting[0].PipelineExecutionId"
        + " : $p.LatestExecution.Status in [\"InProgress\", \"Stopping\"]"
        + " ? $p.LatestExecution.PipelineExecutionId : null)"));
    pipelineState.put("production", q("($l := $states.result.StageStates"
        + "[StageName = \"" + PRODUCTION_STAGE + "\"].LatestExecution;"
        + " $exists($l) ? $l : null)"));
    // Every run the pipeline shows: each stage's latest and waiting runs.
    pipelineState.put("shown", q("[$states.result.StageStates.LatestExecution.PipelineExecutionId,"
        + " $states.result.StageStates.InboundExecution.PipelineExecutionId,"
        + " $states.result.StageStates.InboundExecutions.PipelineExecutionId]"));
    // The latest runs of Staging and Production must both be known.
    pipelineState.put("complete", q("$count($states.result.StageStates["
        + "(StageName = \"" + STAGING_STAGE + "\" or StageName = \"" + PRODUCTION_STAGE + "\")"
        + " and $exists(LatestExecution.PipelineExecutionId)"
        + " and $exists(LatestExecution.Status)]) = 2"));
    // Staging's, the gate's and Production's latest and inbound runs, transitions and approval
    // tokens. A release approves only if this is the same when it selects a run and when it
    // confirms it. A merge moving through Source or Build does not count (cortex-ai #132).
    pipelineState.put("pipeline", q("$string($states.result.StageStates[StageName in [\""
        + STAGING_STAGE + "\", \"" + APPROVAL_STAGE + "\", \"" + PRODUCTION_STAGE + "\"]].{"
        + "\"stage\": StageName,"
        + " \"enabled\": InboundTransitionState.Enabled,"
        + " \"latest\": LatestExecution.{\"run\": PipelineExecutionId,"
        + " \"status\": Status},"
        + " \"inbound\": InboundExecution.{\"run\": PipelineExecutionId,"
        + " \"status\": Status},"
        + " \"inbounds\": InboundExecutions.{\"run\": PipelineExecutionId,"
        + " \"status\": Status},"
        + " \"tokens\": ActionStates.LatestExecution.Token})"));
    Map<String, Object> firstRead = new LinkedHashMap<>(pipelineState);
    firstRead.put("iteration", q("$iteration + 1"));
    CallAwsService readState = readPipelineState("ReadPipelineState", firstRead);
    retry(readState, 3);
    // The runs newer than the gate's run are found by searching the joined ids, and each filter
    // reads only workflow variables: Step Functions did not evaluate positional bindings (mpd-api #620).
    Pass place = Pass.jsonata(this, "PlaceRuns", PassJsonataProps.builder().assign(ordered(
        // The ids before the gate's run in the newest-first list, or none when the gate's run is
        // not in it. Ids never contain a space.
        "newer", q("($ids := \" \" & $join($runs.PipelineExecutionId, \" \") & \" \";"
            + " $at := \" \" & " + gateRun + " & \" \";"
            + " $contains($ids, $at)"
            + " ? [$split($substringBefore($ids, $at), \" \")[$ != \"\"]] : [])"),
        // The runs that could supersede it: those waiting to enter the gate and the latest staging
        // success. A filter on an array constructor matches nothing, so the waiting runs are bound first.
        "candidates", q("($inbound := " + gateInbound + ";"
            + " [$inbound[Status = \"InProgress\""
            + " and PipelineExecutionId != " + gateRun + "].PipelineExecutionId,"
            + " ($staging.LatestExecution.Status = \"Succeeded\""
            + " and " + stagingRun + " != " + gateRun + ") ? " + stagingRun + "])"))).build());

    CallAwsService listRuns = listRuns("ListPipelineRuns", Map.of(), page("[]", "1"));
    CallAwsService listMore = listRuns("ListMoreRuns", Map.of("NextToken", q("$next")), page("$runs", "$pages + 1"));
    retry(listRuns, 3);
    retry(listMore, 3);
    // The list is paged until every run the pipeline shows is found, or up to PAGES_READ pages. A
    // run still not found leaves its order unknown.
    Pass weigh = Pass.jsonata(this, "WeighRuns", PassJsonataProps.builder().assign(ordered(
        // The gate's run is named first when it is one of them.
        "unlisted", q("($missing := $shown[$not($ in $runs.PipelineExecutionId)];"
            + " " + gateRun + " in $missing ? " + gateRun
            + " : $exists($missing) ? $missing[0] : null)"),
        // Production last ran a run newer than the gate's, in any state.
        "ahead", q("$production.PipelineExecutionId in $newer"),
        // Only an ordinary run supersedes the gate's run: one that shipped, or the newest
        // candidate still in progress.
        "supersededBy", q("($ordinary := $runs[PipelineExecutionId in $newer"
            + " and " + ORDINARY + "];"
            + " $shipped := $ordinary[Status = \"Succeeded\"]"
            + "[0].PipelineExecutionId;"
            + " $match := $ordinary[Status = \"InProgress\""
            + " and PipelineExecutionId in $candidates][0].PipelineExecutionId;"
            + " $exists($shipped) ? $shipped : $exists($match) ? $match : null)"),
        // A newer rollback, or a run the list does not show as ordinary, that shipped or waits as a
        // candidate holds a release. So does a newer candidate the list gives no usable status.
        "unproven", q("($u := $runs[PipelineExecutionId in $newer"
            + " and (($not(" + ORDINARY + ")"
            + " and (Status = \"Succeeded\" or (Status = \"InProgress\""
            + " and PipelineExecutionId in $candidates)))"
            + " or (PipelineExecutionId in $candidates"
            + " and $not(Status in " + STATUSES + ")))][0].PipelineExecutionId;"
            + " $exists($u) ? $u : null)"))).build());
    Choice paged = Choice.jsonata(this, "HasEveryShownRun")
        .when(Condition.jsonata(q("$count($shown[$not($ in $runs.PipelineExecutionId)]) > 0"
            + " and $type($next) = \"string\" and $pages < " + PAGES_READ)), listMore)
        .otherwise(place);
    place.next(weigh);
    listRuns.next(paged);
    listMore.next(paged);
    CallAwsService reject = approvalResult("RejectSupersededRun", "Rejected", q("\"Superseded by \" & $supersededBy"));
    CallAwsService approve = approvalResult("ApproveRun", "Approved", "Nightly release " + RELEASE_TIME);
    // The cap counts the reads that start a decision. A rejection's confirming read is not counted;
    // an approval's must fit under the cap.
    Choice again = Choice.jsonata(this, "UnderIterationCap")
        .when(Condition.jsonata(q("$iteration < 10")), waitAgain)
        .otherwise(Fail.jsonata(this, "ResolveLoopCapped", FailJsonataProps.builder()
            .error("ResolveLoopCapped").cause("The gate still needed a decision after 10 reads").build()));
    waitAgain.next(readState);
    // Neither decision is ever sent again, since a retry would act on an older read. After a failed
    // rejection the release reads everything again and decides afresh, within the read cap. After a
    // failed approval it ends: a stale token may mean its own approval applied and the response was lost.
    reject.addCatch(again, CatchProps.builder().errors(List.of("States.ALL")).build());
    approve.addCatch(Choice.jsonata(this, "IsApprovalStale")
            .when(Condition.jsonata(q("$contains($error, \"ApprovalAlreadyCompleted\")"
                + " or $contains($error, \"InvalidApprovalToken\")")),
                Fail.jsonata(this, "ApprovalUncertain", FailJsonataProps.builder()
                    .error("ApprovalUncertain")
                    .cause("The approval token was already used, perhaps by this release's own approval. Check the gate's run")
                    .build()))
            .otherwise(unexpected),
        CatchProps.builder().errors(List.of("States.ALL")).assign(Map.of("error", q("$states.errorOutput.Error"))).build());
    reject.next(again);
    approve.next(done);
    SnsPublish skip = SnsPublish.jsonata(this, "PublishHoldSkip", SnsPublishJsonataProps.builder()
        .topic(productionTopic)
        .subject(PIPELINE_NAME + " nightly release skipped: the release is held")
        .message(TaskInput.fromText(q(
            "\"The " + RELEASE_TIME + " release of " + PIPELINE_NAME + " was skipped"
                + " because \" & ($type($arrow) = \"object\""
                + " ? \"the transition into \" & $arrow.stage & ($arrow.disabled"
                + " ? \" is disabled: \" & $arrow.reason & \".\" : \" could not be read.\")"
                + " : $hold = null ? \"hold parameter " + HOLD_PARAMETER
                + " is not set. Only off releases.\""
                // The value is quoted as JSON, so a stray space or capital shows.
                + " : $hold != \"off\""
                + " ? \"" + HOLD_PARAMETER + " is \" & ($json := $string([$hold]);"
                + " $substring($json, 1, $length($json) - 2))"
                + " & \". Only off releases.\""
                + " : $type($unlisted) = \"string\""
                + " ? ($unlisted = " + gateRun + " ? \"the gate's run \" : \"run \")"
                + " & $unlisted & \" was not found among the " + (RUNS_READ * PAGES_READ)
                + " newest runs. Approve the right run by hand in the console.\""
                + " : $not($complete) ? ($type($production) = \"null\""
                + " ? \"" + PRODUCTION_STAGE + " has no recorded run yet. Approve the first"
                + " production deploy by hand; nightly releases follow.\""
                + " : \"the pipeline state was incomplete.\")"
                + " : $type($deploying) = \"string\" ? \"run \" & $deploying"
                + " & \" has not finished deploying to " + PRODUCTION_STAGE + ".\""
                // The gate's run stays held until Production runs again.
                + " : $ahead ? ($run := $production.PipelineExecutionId;"
                + " \"run \" & $run & \", newer than the gate's run, last ran in"
                + " " + PRODUCTION_STAGE + ". Check run \" & $run & \", and if its change"
                + " should ship, start a fresh run with Release change.\")"
                + " : $type($unproven) = \"string\" ? \"run \" & $unproven"
                + " & \", newer than the gate's run, \""
                + " & ($runs[PipelineExecutionId = $unproven].Status in " + STATUSES
                + " ? \"is a rollback or of unknown kind.\""
                + " : \"has no readable status in the run list. Check that run in"
                + " the console.\")"
                + " : \"the pipeline changed while the release was checking it. The"
                + " next release tries again, or approve the gate by hand.\")")))
        .build());
    retry(skip, 3);
    skip.next(done);
    // Before approving, a release keeps its pipeline read, reads the hold and then the pipeline once
    // more, with no wait or retry, and approves only if the two pipeline reads are the same.
    Pass select = Pass.jsonata(this, "SelectRun", PassJsonataProps.builder().assign(Map.of("selected", q("$pipeline"))).build());
    CallAwsService confirmState = readPipelineState("ConfirmPipelineState", pipelineState);
    confirmState.addCatch(unexpected, CatchProps.builder().errors(List.of("States.ALL")).build());
    // A change reads everything again within the cap, rather than skipping the night (cortex-ai #132).
    confirmState.next(Choice.jsonata(this, "IsPipelineUnchanged")
        .when(Condition.jsonata(q("$pipeline = $selected")), approve)
        .otherwise(again));
    // Before rejecting, the state machine notes the gate's run, its token and the run superseding
    // it, then reads the pipeline once more, with no wait or retry. It rejects only if the gate's
    // run and token are the same and the superseding run has shipped or still waits for the gate.
    // Staging's record of a run outlives the run, so it is no proof that the run is still alive.
    Pass selectRejection = Pass.jsonata(this, "SelectRejection", PassJsonataProps.builder().assign(Map.of(
        "rejecting", q("{\"run\": " + gateRun + ", \"token\": " + GATE_TOKEN + ", \"by\": $supersededBy,"
            + " \"shipped\": $runs[PipelineExecutionId = $supersededBy].Status"
            + " = \"Succeeded\"}"))).build());
    CallAwsService confirmRejection = readPipelineState("ConfirmRejectionState", pipelineState);
    confirmRejection.addCatch(unexpected, CatchProps.builder().errors(List.of("States.ALL")).build());
    selectRejection.next(confirmRejection);
    confirmRejection.next(Choice.jsonata(this, "IsRejectionConfirmed")
        .when(Condition.jsonata(q(gateRun + " = $rejecting.run"
            + " and " + GATE_TOKEN + " = $rejecting.token"
            + " and ($rejecting.shipped or ($in := " + gateInbound + ";"
            + " $rejecting.by in $in[Status = \"InProgress\"]"
            + ".PipelineExecutionId))")), reject)
        // A release reads again within the cap; a resolve leaves it to the next one.
        .when(Condition.jsonata(q("$release")), again)
        .otherwise(done));
    // With the gate idle, decide before the run list is read (cortex-ai #132): wait only for an
    // unfinished run to enter through an enabled transition. A finished run still shown waiting,
    // such as one cancelled when the pipeline changed, is not waited for.
    readState.next(Choice.jsonata(this, "ReadGate")
        // The gate's run can show before its approval has a token.
        .when(Condition.jsonata(q(gateInProgress + " and $not($exists(" + GATE_TOKEN + "))")), again)
        .when(Condition.jsonata(q("$not(" + gateInProgress + ") and ($in := " + gateInbound + ";"
            + " $count($in[$not(Status in " + FINISHED + ")]) > 0) and " + enabled)), again)
        .when(Condition.jsonata(q("$not(" + gateInProgress + ") and $release and $type($arrow) = \"object\"")), skip)
        .when(Condition.jsonata(q("$not(" + gateInProgress + ")")), done)
        .otherwise(listRuns));
    weigh.next(Choice.jsonata(this, "DecideGate")
        .when(Condition.jsonata(q(gateInProgress + " and $type($supersededBy) = \"string\""
            + " and $type($unlisted) = \"null\"")), selectRejection)
        .when(Condition.jsonata(q("$release and " + gateInProgress + " and $complete"
                + " and $type($unlisted) = \"null\""
                + " and $type($arrow) = \"null\" and $type($deploying) = \"null\""
                + " and $not($ahead) and $type($unproven) = \"null\"")),
            // The confirmation read counts under the 10-read cap. Without room for it, the release
            // fails at the cap.
            Choice.jsonata(this, "HasReadsToConfirm")
                .when(Condition.jsonata(q("$iteration < 10")), select)
                .otherwise(again))
        .when(Condition.jsonata(q("$release")), skip)
        .otherwise(done));
    Choice holdMissing = Choice.jsonata(this, "IsHoldMissing")
        .when(Condition.jsonata(q("$contains($error, \"ParameterNotFound\")")), skip)
        .otherwise(unexpected);
    // Only the exact value off releases.
    Condition holdOff = Condition.jsonata(q("$hold = \"off\""));
    CallAwsService readHold = readHold("ReadReleaseHold", holdMissing);
    readHold.next(Choice.jsonata(this, "IsHoldOff").when(holdOff, readState).otherwise(skip));
    CallAwsService confirmHold = readHold("ConfirmReleaseHold", holdMissing);
    confirmHold.next(Choice.jsonata(this, "IsHoldStillOff").when(holdOff, confirmState).otherwise(skip));
    select.next(confirmHold);
    Pass start = Pass.jsonata(this, "Start", PassJsonataProps.builder().assign(ordered(
        "release", q("$states.input.mode = \"release\""),
        "scheduled", q("$states.input.mode = \"schedule\""),
        "firing", q("$exists($states.input.scheduledTime) ? $states.input.scheduledTime : null"),
        "iteration", 0,
        "arrow", q("null"))).build());
    // One release per scheduled firing: Step Functions refuses a second execution with the same
    // name for 90 days. Names cannot contain colons.
    CallAwsService startRelease = CallAwsService.jsonata(this, "StartNamedRelease", CallAwsServiceJsonataProps.builder()
        .service("sfn").action("startExecution").iamAction("states:StartExecution")
        .iamResources(List.of(releaseMachineArn))
        .parameters(ordered(
            "StateMachineArn", releaseMachineArn,
            "Name", q("\"release-\" & $replace($firing, \":\", \"\")"),
            "Input", "{\"mode\":\"release\"}"))
        .build());
    retry(startRelease, 2);
    startRelease.addCatch(Choice.jsonata(this, "IsAlreadyStarted")
            .when(Condition.jsonata(q("$contains($error, \"ExecutionAlreadyExists\")")), done)
            .otherwise(unexpected),
        CatchProps.builder().errors(List.of("States.ALL")).assign(Map.of("error", q("$states.errorOutput.Error"))).build());
    startRelease.next(done);
    // The other modes wait first, so a run that just passed staging has entered the gate before
    // the hold and the gate are read.
    start.next(Choice.jsonata(this, "IsScheduled")
        .when(Condition.jsonata(q("$scheduled")), startRelease)
        .otherwise(firstWait));
    firstWait.next(Choice.jsonata(this, "IsReleaseMode")
        .when(Condition.jsonata(q("$release")), readHold)
        .otherwise(readState));
    return StateMachine.Builder.create(this, "StateMachine")
        .stateMachineName(RELEASE_MACHINE)
        .queryLanguage(QueryLanguage.JSONATA)
        .timeout(Duration.minutes(15))
        .definitionBody(DefinitionBody.fromChainable(start))
        .build();
  }

  /**
   * Adds a page's runs, keeping only the fields the decisions use so $runs stays small, and notes
   * where the next page starts. A run that starts while the list is paged pushes every row back
   * one, so a row can appear again at the top of the next page; dropping rows already read keeps
   * each run once, where it was first seen.
   */
  private static Map<String, Object> page(String runs, String pages) {
    return ordered(
        "runs", q("($page := [$states.result.PipelineExecutionSummaries.{"
            + "\"PipelineExecutionId\": PipelineExecutionId, \"Status\": Status,"
            + " \"Trigger\": Trigger.{\"TriggerType\": TriggerType},"
            + " \"ExecutionType\": ExecutionType,"
            + " \"RollbackMetadata\": RollbackMetadata}];"
            + " $append(" + runs + ", [$page[$not(PipelineExecutionId"
            + " in " + runs + ".PipelineExecutionId)]]))"),
        "next", q("$exists($states.result.NextToken) ? $states.result.NextToken : null"),
        "pages", q(pages));
  }

  private CallAwsService readPipelineState(String id, Map<String, Object> assign) {
    return CallAwsService.jsonata(this, id, CallAwsServiceJsonataProps.builder()
        .service("codepipeline").action("getPipelineState").iamAction("codepipeline:GetPipelineState")
        .iamResources(List.of(pipelineArn))
        .parameters(Map.of("Name", PIPELINE_NAME))
        .assign(assign)
        .build());
  }

  private CallAwsService readHold(String id, IChainable onError) {
    CallAwsService hold = CallAwsService.jsonata(this, id, CallAwsServiceJsonataProps.builder()
        .service("ssm").action("getParameter").iamAction("ssm:GetParameter")
        .iamResources(List.of("arn:aws:ssm:" + region + ":" + account + ":parameter" + HOLD_PARAMETER))
        .parameters(Map.of("Name", HOLD_PARAMETER))
        .assign(Map.of("hold", q("$exists($states.result.Parameter.Value) ? $states.result.Parameter.Value : null")))
        .build());
    retry(hold, 2);
    hold.addCatch(onError, CatchProps.builder().errors(List.of("States.ALL"))
        .assign(ordered("error", q("$states.errorOutput.Error"), "hold", q("null"))).build());
    return hold;
  }

  private CallAwsService listRuns(String id, Map<String, Object> parameters, Map<String, Object> assign) {
    Map<String, Object> all = new LinkedHashMap<>();
    all.put("PipelineName", PIPELINE_NAME);
    all.put("MaxResults", RUNS_READ);
    all.putAll(parameters);
    return CallAwsService.jsonata(this, id, CallAwsServiceJsonataProps.builder()
        .service("codepipeline").action("listPipelineExecutions").iamAction("codepipeline:ListPipelineExecutions")
        .iamResources(List.of(pipelineArn))
        .parameters(all)
        .assign(assign)
        .build());
  }

  private CallAwsService approvalResult(String id, String status, String summary) {
    return CallAwsService.jsonata(this, id, CallAwsServiceJsonataProps.builder()
        .service("codepipeline").action("putApprovalResult").iamAction("codepipeline:PutApprovalResult")
        .iamResources(List.of(pipelineArn + "/" + APPROVAL_STAGE + "/" + APPROVAL_ACTION))
        .parameters(ordered(
            "PipelineName", PIPELINE_NAME,
            "StageName", APPROVAL_STAGE,
            "ActionName", APPROVAL_ACTION,
            "Result", ordered("Status", status, "Summary", summary),
            "Token", q(GATE_TOKEN)))
        .build());
  }

  /** A map that keeps its keys in the order given: key, value, key, value. */
  private static Map<String, Object> ordered(Object... pairs) {
    Map<String, Object> map = new LinkedHashMap<>();
    for (int i = 0; i < pairs.length; i += 2) map.put((String) pairs[i], pairs[i + 1]);
    return map;
  }
}
