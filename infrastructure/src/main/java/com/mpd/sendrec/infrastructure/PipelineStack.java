package com.mpd.sendrec.infrastructure;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import software.amazon.awscdk.Duration;
import software.amazon.awscdk.Environment;
import software.amazon.awscdk.Stack;
import software.amazon.awscdk.StackProps;
import software.amazon.awscdk.Stage;
import software.amazon.awscdk.StageProps;
import software.amazon.awscdk.pipelines.AddStageOpts;
import software.amazon.awscdk.pipelines.CodeBuildOptions;
import software.amazon.awscdk.pipelines.CodeBuildStep;
import software.amazon.awscdk.pipelines.CodePipeline;
import software.amazon.awscdk.pipelines.CodePipelineSource;
import software.amazon.awscdk.pipelines.ConnectionSourceOptions;
import software.amazon.awscdk.pipelines.IFileSetProducer;
import software.amazon.awscdk.pipelines.ManualApprovalStep;
import software.amazon.awscdk.pipelines.WaveOptions;
import software.amazon.awscdk.services.codebuild.BuildEnvironment;
import software.amazon.awscdk.services.codebuild.BuildEnvironmentVariable;
import software.amazon.awscdk.services.codebuild.BuildEnvironmentVariableType;
import software.amazon.awscdk.services.codebuild.BuildSpec;
import software.amazon.awscdk.services.codebuild.ComputeType;
import software.amazon.awscdk.services.codebuild.LinuxBuildImage;
import software.amazon.awscdk.services.codepipeline.PipelineType;
import software.amazon.awscdk.services.iam.PolicyStatement;
import software.amazon.awscdk.services.iam.Role;
import software.amazon.awscdk.services.iam.ServicePrincipal;
import software.constructs.Construct;

/**
 * SendRec's deployment pipeline in the deployment account (design D1 to D6), shaped like the staff
 * app's: Source, Build, UpdatePipeline, Staging, ApproveProduction, Production. Build tests the
 * infrastructure, builds the image once and synthesizes both stages with its digest. Production
 * copies that digest from staging's repository and never rebuilds. Each stack deploy migrates
 * before its service changes ({@link MigrationRun}), and each stage then checks the version its
 * public URL serves. The nightly release approves the gate ({@link NightlyRelease}).
 */
final class PipelineStack extends Stack {
  static final String REPOSITORY = "My-Performance-Doctor/sendrec";
  static final String BRANCH = "main";
  /** Named roles, so staging's and production's repository policies can name them (README one-time steps). */
  static final String BUILD_ROLE = "sendrec-pipeline-build";
  static final String PROMOTE_ROLE = "sendrec-pipeline-promote";
  static final String STAGING_REGISTRY = StagingConfig.ACCOUNT + ".dkr.ecr." + PipelineConfig.REGION + ".amazonaws.com";
  /** Written into the cloud assembly by {@link SendRecApp}; later steps read the release from it. */
  static final String RELEASE_FILE = "sendrec-release.json";
  static final String ASSEMBLY_DIRECTORY = "assembly";
  /** The staff app's pins: the CDK CLI and Node.js 24, checked against the release's SHASUMS256.txt. */
  static final String CDK_CLI_VERSION = "2.1142.0";
  static final String NODE_VERSION = "24.22.1";
  static final String NODE_SHA256 = "eb6728e77c576a35162c122c45ccadf224d89ff00abc8dae46cc2897c6002d7c";
  /** go-containerregistry's crane, checked against the release's checksums.txt. */
  static final String CRANE_VERSION = "v0.20.2";
  static final String CRANE_SHA256 = "c14340087103ba9dadf61d45acd20675490fd0ccbd56ac7901fc1b502137f44b";
  /** mpd-api's floor: the first Ubuntu 22.04 ca-certificates without emSign ECC Root CA - G3 (LP: #2171507). */
  static final String UBUNTU_CA_CERTIFICATES = "20260816ubuntu0.22.04.1";
  /** SHA-256 fingerprints of the roots mpd-api removes from Corretto 21 before Gradle runs. */
  static final String EMSIGN_ECC_ROOT_G3_SHA256 =
      "86:A1:EC:BA:08:9C:4A:8D:3B:BE:27:34:C6:12:BA:34:1D:81:3E:04:3C:F9:E8:A8:62:CD:5C:57:A3:6B:BE:6B";
  static final String AAA_CERTIFICATE_SERVICES_SHA256 =
      "D7:A7:A0:FB:5D:7E:27:31:D7:71:E9:48:4E:BC:DE:F7:1D:5F:0C:3E:0A:29:48:78:2B:C8:3E:E0:EA:69:9E:F4";
  static final String SYSTEM_CA_BUNDLE = "/etc/ssl/certs/ca-certificates.crt";

  PipelineStack(Construct scope, String id, StackProps props, PipelineConfig config) {
    super(scope, id, props);
    if (!config.deploymentAccount().equals(getAccount()))
      throw new IllegalArgumentException("The pipeline stack must be in the configured deployment account");
    String productionAccount = config.productionContext().get("productionAccount").toString();
    CodePipelineSource source = CodePipelineSource.connection(REPOSITORY, BRANCH,
        ConnectionSourceOptions.builder().connectionArn(config.connectionArn()).build());

    Role buildRole = Role.Builder.create(this, "BuildRole").roleName(BUILD_ROLE)
        .assumedBy(new ServicePrincipal("codebuild.amazonaws.com")).build();
    buildRole.addToPolicy(PolicyStatement.Builder.create().actions(List.of("ecr:GetAuthorizationToken"))
        .resources(List.of("*")).build());
    buildRole.addToPolicy(PolicyStatement.Builder.create()
        .actions(List.of("ecr:DescribeImages", "ecr:BatchCheckLayerAvailability", "ecr:InitiateLayerUpload",
            "ecr:UploadLayerPart", "ecr:CompleteLayerUpload", "ecr:PutImage", "ecr:BatchGetImage",
            "ecr:GetDownloadUrlForLayer"))
        .resources(List.of(repositoryArn(StagingConfig.ACCOUNT, PipelineConfig.IMAGE_REPOSITORY))).build());
    buildRole.addToPolicy(PolicyStatement.Builder.create()
        .actions(List.of("ecr:BatchCheckLayerAvailability", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"))
        .resources(List.of(repositoryArn(StagingConfig.ACCOUNT, "sendrec-base"))).build());
    // No lookup role: each stage's context names its network, because a CDK lookup role can read
    // every bucket in its account, and this step runs third-party build code.
    buildRole.addToPolicy(PolicyStatement.Builder.create().actions(List.of("ssm:GetParameters"))
        .resources(parameterArns()).build());

    CodeBuildStep synth = CodeBuildStep.Builder.create("Synth")
        .input(source)
        .role(buildRole)
        .buildEnvironment(BuildEnvironment.builder().privileged(true).computeType(ComputeType.LARGE)
            .environmentVariables(parameterVariables()).build())
        .env(ordered("STAGING_REGISTRY", STAGING_REGISTRY, "AWS_CA_BUNDLE", SYSTEM_CA_BUNDLE))
        .partialBuildSpec(BuildSpec.fromObject(installPhase(Map.of("java", "corretto21"), synthInstallCommands())))
        .commands(List.of(
            "python3 -B -m unittest discover -s infrastructure/tests",
            "cd infrastructure && node tests/test_bootstrap_access.cjs && ./gradlew -q test && cd ..",
            "REVISION=\"$CODEBUILD_RESOLVED_SOURCE_VERSION\"",
            "export REVISION",
            "RELEASE_DIGEST=\"$(sh infrastructure/pipeline/build-image.sh)\"",
            "cd infrastructure && cdk synth -q -c targetEnvironment=pipeline -c releaseRevision=\"$REVISION\""
                + " -c releaseDigest=\"$RELEASE_DIGEST\" -c alarmEmailsStaging=\"$ALARM_EMAILS_STAGING\""
                + " -c alarmEmailsProduction=\"$ALARM_EMAILS_PRODUCTION\""))
        .primaryOutputDirectory("infrastructure/cdk.out")
        .build();

    CodePipeline pipeline = CodePipeline.Builder.create(this, "Pipeline")
        .pipelineName(NightlyRelease.PIPELINE_NAME)
        .pipelineType(PipelineType.V2)
        .crossAccountKeys(true)
        .selfMutation(true)
        .cliVersion(CDK_CLI_VERSION)
        .codeBuildDefaults(CodeBuildOptions.builder()
            .buildEnvironment(BuildEnvironment.builder().buildImage(LinuxBuildImage.STANDARD_7_0).build())
            .partialBuildSpec(BuildSpec.fromObject(installPhase(Map.of("nodejs", "24"), nodeInstallCommands())))
            .build())
        .synth(synth)
        .build();
    IFileSetProducer assembly = synth;

    Stage staging = new Stage(this, NightlyRelease.STAGING_STAGE, StageProps.builder()
        .env(Environment.builder().account(StagingConfig.ACCOUNT).region(PipelineConfig.REGION).build()).build());
    config.stagingContext().forEach((key, value) -> staging.getNode().setContext(key, value));
    staging.getNode().setContext("releaseImage", config.stagingImage());
    SendRecStack stagingStack = new SendRecStack(staging, "SendRecStaging",
        SendRecApp.stagingProps(StagingConfig.ACCOUNT), StagingConfig.INSTANCE);
    // ci.yml records its result here through GitHub OIDC; the wait step only reads it (design D11).
    CiRecord ciRecord = new CiRecord(this, "CiRecord", config.githubOidcProviderArn());
    CodeBuildStep waitForCi = CodeBuildStep.Builder.create("WaitForGitHubCI")
        .input(source)
        .additionalInputs(Map.of(ASSEMBLY_DIRECTORY, assembly))
        .timeout(Duration.minutes(70))
        .env(ordered("RELEASE_FILE", ASSEMBLY_DIRECTORY + "/" + RELEASE_FILE, "CI_RESULTS_TABLE", CiRecord.TABLE_NAME,
            "AWS_CA_BUNDLE", SYSTEM_CA_BUNDLE))
        .partialBuildSpec(BuildSpec.fromObject(installPhase(Map.of(), caCertificatesCommands())))
        .commands(List.of("python3 -B infrastructure/pipeline/wait-for-ci.py"))
        .build();
    pipeline.addStage(staging, AddStageOpts.builder()
        .pre(List.of(waitForCi))
        .post(List.of(verify("VerifyStaging", source, assembly, stagingStack))).build());

    // The gate is a stage of its own, as in MPD API and the staff app, so a newer run can wait at
    // it while an approved run deploys, and each side has its own transition.
    pipeline.addWave(NightlyRelease.APPROVAL_STAGE, WaveOptions.builder()
        .pre(List.of(ManualApprovalStep.Builder.create(NightlyRelease.APPROVAL_ACTION)
            .comment("The nightly release approves this at " + NightlyRelease.RELEASE_TIME
                + " unless a release hold is on.")
            .build()))
        .build());

    Stage production = new Stage(this, NightlyRelease.PRODUCTION_STAGE, StageProps.builder()
        .env(Environment.builder().account(productionAccount).region(PipelineConfig.REGION).build()).build());
    config.productionContext().forEach((key, value) -> production.getNode().setContext(key, value));
    production.getNode().setContext("productionImage", config.productionImage());
    ProductionConfig productionConfig = ProductionConfig.fromContext(production);
    SendRecStack productionStack = new SendRecStack(production, "SendRecProduction",
        SendRecApp.productionProps(productionAccount), productionConfig);

    Role promoteRole = Role.Builder.create(this, "PromoteRole").roleName(PROMOTE_ROLE)
        .assumedBy(new ServicePrincipal("codebuild.amazonaws.com")).build();
    promoteRole.addToPolicy(PolicyStatement.Builder.create().actions(List.of("ecr:GetAuthorizationToken"))
        .resources(List.of("*")).build());
    promoteRole.addToPolicy(PolicyStatement.Builder.create()
        .actions(List.of("ecr:BatchCheckLayerAvailability", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"))
        .resources(List.of(repositoryArn(StagingConfig.ACCOUNT, PipelineConfig.IMAGE_REPOSITORY))).build());
    promoteRole.addToPolicy(PolicyStatement.Builder.create()
        .actions(List.of("ecr:BatchCheckLayerAvailability", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer",
            "ecr:InitiateLayerUpload", "ecr:UploadLayerPart", "ecr:CompleteLayerUpload", "ecr:PutImage"))
        .resources(List.of(repositoryArn(productionAccount, PipelineConfig.IMAGE_REPOSITORY))).build());
    CodeBuildStep promote = CodeBuildStep.Builder.create("PromoteImage")
        .input(source)
        .additionalInputs(Map.of(ASSEMBLY_DIRECTORY, assembly))
        .role(promoteRole)
        .env(ordered("RELEASE_FILE", ASSEMBLY_DIRECTORY + "/" + RELEASE_FILE, "AWS_CA_BUNDLE", SYSTEM_CA_BUNDLE,
            "CRANE", "/tmp/crane/crane"))
        .partialBuildSpec(BuildSpec.fromObject(installPhase(Map.of(), promoteInstallCommands())))
        .commands(List.of("sh infrastructure/pipeline/promote-image.sh"))
        .build();
    pipeline.addStage(production, AddStageOpts.builder()
        .pre(List.of(promote))
        .post(List.of(verify("VerifyProduction", source, assembly, productionStack))).build());

    // Without context, as in a local synth, the alert topics email no one.
    new NightlyRelease(this, "NightlyRelease",
        NightlyRelease.parseAlarmEmails("staging", (String) getNode().tryGetContext("alarmEmailsStaging")),
        NightlyRelease.parseAlarmEmails("production", (String) getNode().tryGetContext("alarmEmailsProduction")));

    // The step's role exists only once the pipeline is built.
    pipeline.buildPipeline();
    ciRecord.grantLookup(waitForCi.getGrantPrincipal());
  }

  /** Reads the stack's public /api/health until it reports the release's version (design D6). */
  private static CodeBuildStep verify(String id, CodePipelineSource source, IFileSetProducer assembly, SendRecStack stack) {
    return CodeBuildStep.Builder.create(id)
        .input(source)
        .additionalInputs(Map.of(ASSEMBLY_DIRECTORY, assembly))
        .envFromCfnOutputs(Map.of("BASE_URL", stack.baseUrlOutput()))
        .env(Map.of("RELEASE_FILE", ASSEMBLY_DIRECTORY + "/" + RELEASE_FILE))
        .timeout(Duration.minutes(15))
        .commands(List.of("sh infrastructure/pipeline/verify-version.sh"))
        .build();
  }

  /**
   * Environment variables in the order given. Map.of iterates in a different order on each JVM, and
   * the order reaches the Build project's config hash in the pipeline's definition.
   */
  private static Map<String, String> ordered(String... pairs) {
    Map<String, String> map = new LinkedHashMap<>();
    for (int i = 0; i < pairs.length; i += 2) map.put(pairs[i], pairs[i + 1]);
    return map;
  }

  private String repositoryArn(String account, String name) {
    return "arn:aws:ecr:" + PipelineConfig.REGION + ":" + account + ":repository/" + name;
  }

  /** The configuration parameters and MPD API's alarm lists, which the staff app also reads. */
  static Map<String, String> parameters() {
    Map<String, String> parameters = new LinkedHashMap<>(PipelineConfig.PARAMETERS);
    parameters.put("ALARM_EMAILS_STAGING", "/mpd-api/alarm-emails/staging");
    parameters.put("ALARM_EMAILS_PRODUCTION", "/mpd-api/alarm-emails/production");
    return parameters;
  }

  private List<String> parameterArns() {
    return parameters().values().stream()
        .map(name -> "arn:aws:ssm:" + PipelineConfig.REGION + ":" + getAccount() + ":parameter" + name).toList();
  }

  private static Map<String, BuildEnvironmentVariable> parameterVariables() {
    Map<String, BuildEnvironmentVariable> variables = new LinkedHashMap<>();
    parameters().forEach((variable, name) -> variables.put(variable, BuildEnvironmentVariable.builder()
        .type(BuildEnvironmentVariableType.PARAMETER_STORE).value(name).build()));
    return variables;
  }

  /** A partial buildspec holding only install-phase runtimes and commands. */
  static Map<String, Object> installPhase(Map<String, String> runtimeVersions, List<String> commands) {
    Map<String, Object> install = new LinkedHashMap<>();
    if (!runtimeVersions.isEmpty()) install.put("runtime-versions", runtimeVersions);
    install.put("commands", commands);
    return Map.of("phases", Map.of("install", install));
  }

  /**
   * Replaces the image's Node.js with {@link #NODE_VERSION}. The archive's hash is checked before
   * anything unpacks or runs it, and the old npm and corepack are removed first.
   */
  static List<String> nodeInstallCommands() {
    String archive = "node-v" + NODE_VERSION + "-linux-x64.tar.gz";
    return List.of(
        "curl -fsSL --retry 3 -o /tmp/" + archive + " https://nodejs.org/dist/v" + NODE_VERSION + "/" + archive,
        "echo \"" + NODE_SHA256 + "  /tmp/" + archive + "\" | sha256sum -c -",
        "rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack",
        "tar -xzf /tmp/" + archive + " -C /usr/local --strip-components=1 --no-same-owner",
        "test \"$(node --version)\" = v" + NODE_VERSION);
  }

  /** Docker and the AWS CLI log in through the image's CA store, so it is upgraded first (mpd-api). */
  static List<String> caCertificatesCommands() {
    return List.of(
        "apt-get update -o Dir::Etc::sourceparts=-",
        "DEBIAN_FRONTEND=noninteractive apt-get install -y --only-upgrade ca-certificates",
        "dpkg --compare-versions \"$(dpkg-query -W -f='${Version}' ca-certificates)\" ge " + UBUNTU_CA_CERTIFICATES);
  }

  /**
   * Removes emSign ECC Root CA - G3 and AAA Certificate Services from the JDK Gradle runs on, by
   * fingerprint, and fails unless both are gone (mpd-api's {@code javaTrustCommands}).
   */
  static List<String> javaTrustCommands() {
    String list = "\"$JAVA_HOME/bin/keytool\" -list -cacerts -storepass changeit";
    String delete = "\"$JAVA_HOME/bin/keytool\" -delete -cacerts -storepass changeit -alias \"$alias\"";
    return List.of(
        list + " > /tmp/cacerts.txt",
        "awk -v g3=" + EMSIGN_ECC_ROOT_G3_SHA256 + " -v aaa=" + AAA_CERTIFICATE_SERVICES_SHA256
            + " '/, trustedCertEntry,/ { alias = substr($0, 1, index($0, \", \") - 1) }"
            + " $NF == g3 || $NF == aaa { print alias }' /tmp/cacerts.txt"
            + " | while IFS= read -r alias; do " + delete + " || exit 1; echo \"Removed $alias\"; done",
        list + " > /tmp/cacerts.txt && { grep -q -e " + EMSIGN_ECC_ROOT_G3_SHA256 + " -e "
            + AAA_CERTIFICATE_SERVICES_SHA256 + " /tmp/cacerts.txt; test $? -eq 1; }");
  }

  private static List<String> synthInstallCommands() {
    List<String> commands = new ArrayList<>(caCertificatesCommands());
    commands.addAll(javaTrustCommands());
    commands.add("npm install -g aws-cdk@" + CDK_CLI_VERSION);
    return commands;
  }

  /** The pinned crane release, checked before it is unpacked. */
  static List<String> promoteInstallCommands() {
    String archive = "go-containerregistry_Linux_x86_64.tar.gz";
    List<String> commands = new ArrayList<>(caCertificatesCommands());
    commands.addAll(List.of(
        "curl -fsSL --retry 3 -o /tmp/" + archive
            + " https://github.com/google/go-containerregistry/releases/download/" + CRANE_VERSION + "/" + archive,
        "echo \"" + CRANE_SHA256 + "  /tmp/" + archive + "\" | sha256sum -c -",
        "mkdir -p /tmp/crane && tar -xzf /tmp/" + archive + " -C /tmp/crane --no-same-owner crane",
        "test \"$(/tmp/crane/crane version)\" = " + CRANE_VERSION.substring(1)));
    return commands;
  }
}
