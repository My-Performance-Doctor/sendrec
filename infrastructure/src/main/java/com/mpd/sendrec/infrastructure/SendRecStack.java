package com.mpd.sendrec.infrastructure;

import java.util.HashMap;
import java.util.List;
import java.util.Map;
import software.amazon.awscdk.CfnOutput;
import software.amazon.awscdk.Duration;
import software.amazon.awscdk.RemovalPolicy;
import software.amazon.awscdk.Stack;
import software.amazon.awscdk.StackProps;
import software.amazon.awscdk.services.ec2.IVpc;
import software.amazon.awscdk.services.ec2.InstanceClass;
import software.amazon.awscdk.services.ec2.InstanceSize;
import software.amazon.awscdk.services.ec2.InstanceType;
import software.amazon.awscdk.services.ec2.Peer;
import software.amazon.awscdk.services.ec2.Port;
import software.amazon.awscdk.services.ec2.SecurityGroup;
import software.amazon.awscdk.services.ec2.SubnetSelection;
import software.amazon.awscdk.services.ec2.SubnetType;
import software.amazon.awscdk.services.ec2.Vpc;
import software.amazon.awscdk.services.ec2.VpcLookupOptions;
import software.amazon.awscdk.services.cloudfront.*;
import software.amazon.awscdk.services.cloudfront.origins.VpcOrigin;
import software.amazon.awscdk.services.cloudfront.origins.VpcOriginWithEndpointProps;
import software.amazon.awscdk.services.ecr.assets.Platform;
import software.amazon.awscdk.services.ecs.AssetImageProps;
import software.amazon.awscdk.services.ecs.AwsLogDriverProps;
import software.amazon.awscdk.services.ecs.Cluster;
import software.amazon.awscdk.services.ecs.ContainerDefinitionOptions;
import software.amazon.awscdk.services.ecs.ContainerImage;
import software.amazon.awscdk.services.ecs.FargateService;
import software.amazon.awscdk.services.ecs.FargateTaskDefinition;
import software.amazon.awscdk.services.ecs.LogDrivers;
import software.amazon.awscdk.services.ecs.PortMapping;
import software.amazon.awscdk.services.ecs.Secret;
import software.amazon.awscdk.services.elasticloadbalancingv2.AddApplicationTargetsProps;
import software.amazon.awscdk.services.elasticloadbalancingv2.ApplicationListener;
import software.amazon.awscdk.services.elasticloadbalancingv2.ApplicationLoadBalancer;
import software.amazon.awscdk.services.elasticloadbalancingv2.ApplicationProtocol;
import software.amazon.awscdk.services.elasticloadbalancingv2.ApplicationTargetGroup;
import software.amazon.awscdk.services.elasticloadbalancingv2.HealthCheck;
import software.amazon.awscdk.services.elasticloadbalancingv2.ListenerAction;
import software.amazon.awscdk.services.elasticloadbalancingv2.ListenerCertificate;
import software.amazon.awscdk.services.elasticloadbalancingv2.RedirectOptions;
import software.amazon.awscdk.services.elasticloadbalancingv2.SslPolicy;
import software.amazon.awscdk.services.elasticloadbalancingv2.TargetType;
import software.amazon.awscdk.services.logs.LogGroup;
import software.amazon.awscdk.services.logs.RetentionDays;
import software.amazon.awscdk.services.rds.Credentials;
import software.amazon.awscdk.services.rds.CredentialsBaseOptions;
import software.amazon.awscdk.services.rds.DatabaseInstance;
import software.amazon.awscdk.services.rds.DatabaseInstanceEngine;
import software.amazon.awscdk.services.rds.PostgresEngineVersion;
import software.amazon.awscdk.services.rds.PostgresInstanceEngineProps;
import software.amazon.awscdk.services.rds.StorageType;
import software.amazon.awscdk.services.s3.BlockPublicAccess;
import software.amazon.awscdk.services.s3.Bucket;
import software.amazon.awscdk.services.s3.BucketEncryption;
import software.amazon.awscdk.services.s3.CorsRule;
import software.amazon.awscdk.services.s3.HttpMethods;
import software.amazon.awscdk.services.secretsmanager.SecretStringGenerator;
import software.constructs.Construct;

/**
 * SendRec on Fargate: one task behind an ALB, one RDS Postgres instance, one S3 bucket.
 *
 * <p>Transcription runs inside the task with whisper.cpp, so the task is sized for ffmpeg plus a
 * small Whisper model. The S3 client in the fork falls back to the task role when no static keys
 * are set, so no IAM user is created.
 */
public class SendRecStack extends Stack {
  private static final int CONTAINER_PORT = 8080;
  private static final String WHISPER_MODEL_URL =
      "https://huggingface.co/ggerganov/whisper.cpp/resolve/5359861c739e955e79d9a303bcbc70fb988958b1/ggml-small.bin";
  private static final String WHISPER_MODEL_SHA256 =
      "1be3a9b2063867b937e64e2ec7483364a79917e157fa98c5d94b5c1fffea987b";

  public SendRecStack(
      final Construct scope,
      final String id,
      final StackProps props,
      final EnvironmentConfig config) {
    this(scope, id, props, config, null);
  }

  SendRecStack(final Construct scope, final String id, final StackProps props,
      final EnvironmentConfig config, final IVpc suppliedVpc) {
    super(scope, id, props);

    DeploymentOptions options = new DeploymentOptions(this);
    if (config.production() && (options.bootstrap || !options.bootstrapIp.isEmpty())) {
      throw new IllegalArgumentException("Production registration/bootstrap is prohibited");
    }
    String prefix = "sendrec-" + config.environmentName();
    Object override = getNode().tryGetContext("releaseImage");
    String image = config.production() ? config.immutableImage() : override == null ? "" : override.toString();
    boolean controlledRelease = !image.isEmpty();
    if (controlledRelease && !image.matches(getAccount() + "\\.dkr\\.ecr\\.ap-southeast-2\\.amazonaws\\.com/[a-z0-9/_-]+@sha256:[a-f0-9]{64}")) {
      throw new IllegalArgumentException("Release image requires a same-account Sydney ECR digest");
    }

    IVpc vpc =
        suppliedVpc != null ? suppliedVpc : Vpc.fromLookup(
            this,
            "Vpc",
            VpcLookupOptions.builder().vpcId(config.vpcId()).region(this.getRegion()).build());

    // Storage -----------------------------------------------------------------------------
    Bucket recordings =
        Bucket.Builder.create(this, "Recordings")
            .bucketName(config.recordingsBucketName())
            .blockPublicAccess(BlockPublicAccess.BLOCK_ALL)
            .encryption(BucketEncryption.S3_MANAGED)
            .versioned(config.production())
            .enforceSsl(true)
            .removalPolicy(RemovalPolicy.RETAIN)
            .build();

    // Network -----------------------------------------------------------------------------
    SecurityGroup albSecurityGroup =
        SecurityGroup.Builder.create(this, "AlbSecurityGroup")
            .vpc(vpc)
            .securityGroupName(prefix + "-alb-sg")
            .description("SendRec load balancer")
            .allowAllOutbound(true)
            .build();
    albSecurityGroup.addIngressRule(Peer.prefixList(config.cloudFrontPrefixListId()),
        Port.tcp(80), "CloudFront private origin traffic only");

    SecurityGroup serviceSecurityGroup =
        SecurityGroup.Builder.create(this, "ServiceSecurityGroup")
            .vpc(vpc)
            .securityGroupName(prefix + "-service-sg")
            .description("SendRec Fargate tasks")
            .allowAllOutbound(true)
            .build();
    serviceSecurityGroup.addIngressRule(
        albSecurityGroup, Port.tcp(CONTAINER_PORT), "From the load balancer");

    ApplicationLoadBalancer alb =
        ApplicationLoadBalancer.Builder.create(this, "Alb")
            .vpc(vpc)
            .loadBalancerName(prefix + "-private-alb")
            .internetFacing(false)
            .securityGroup(albSecurityGroup)
            .vpcSubnets(SubnetSelection.builder().subnetType(SubnetType.PRIVATE_ISOLATED).build())
            .idleTimeout(Duration.seconds(600))
            .build();
    // CloudFront appends the verified viewer IP. SendRec trusts the last entry,
    // so do not append the CloudFront ENI address at the ALB as another hop.
    alb.setAttribute("routing.http.xff_header_processing.mode", "preserve");

    software.amazon.awscdk.services.cloudfront.Function access =
        software.amazon.awscdk.services.cloudfront.Function.Builder.create(this, "ViewerAccess")
            .functionName(prefix + "-access")
            .runtime(FunctionRuntime.JS_2_0)
            .code(FunctionCode.fromInline(options.accessCode()))
            .build();
    Distribution distribution = Distribution.Builder.create(this, "Distribution")
        .defaultBehavior(BehaviorOptions.builder()
            .origin(VpcOrigin.withApplicationLoadBalancer(alb,
                VpcOriginWithEndpointProps.builder().protocolPolicy(OriginProtocolPolicy.HTTP_ONLY).build()))
            .viewerProtocolPolicy(ViewerProtocolPolicy.REDIRECT_TO_HTTPS)
            .allowedMethods(AllowedMethods.ALLOW_ALL)
            .cachePolicy(CachePolicy.CACHING_DISABLED)
            .originRequestPolicy(OriginRequestPolicy.ALL_VIEWER_EXCEPT_HOST_HEADER)
            .functionAssociations(List.of(FunctionAssociation.builder().function(access)
                .eventType(FunctionEventType.VIEWER_REQUEST).build()))
            .build())
        .minimumProtocolVersion(SecurityPolicyProtocol.TLS_V1_2_2021)
        .enableIpv6(false)
        .build();
    String baseUrl = "https://" + distribution.getDistributionDomainName();

    // The browser PUTs recordings straight to S3 with a presigned URL.
    recordings.addCorsRule(
        CorsRule.builder()
            .allowedOrigins(List.of(baseUrl))
            .allowedMethods(List.of(HttpMethods.GET, HttpMethods.PUT, HttpMethods.HEAD))
            .allowedHeaders(List.of("*"))
            .exposedHeaders(List.of("ETag"))
            .maxAge(3600)
            .build());

    // Database ----------------------------------------------------------------------------
    DatabaseInstance database =
        DatabaseInstance.Builder.create(this, "Database")
            .instanceIdentifier(prefix + "-encrypted-db")
            .databaseName("sendrec")
            .engine(
                DatabaseInstanceEngine.postgres(
                    PostgresInstanceEngineProps.builder()
                        // Upstream publishes media with PostgreSQL 18 RETURNING old/new syntax.
                        .version(PostgresEngineVersion.of("18.6", "18"))
                        .build()))
            .credentials(
                Credentials.fromGeneratedSecret(
                    "sendrec",
                    CredentialsBaseOptions.builder()
                        .secretName(prefix + "/rds-credentials")
                        .excludeCharacters(" /@\"'\\#%")
                        .build()))
            .vpc(vpc)
            .vpcSubnets(SubnetSelection.builder().subnetType(SubnetType.PRIVATE_ISOLATED).build())
            .instanceType(InstanceType.of(InstanceClass.T4G, InstanceSize.MICRO))
            .storageType(StorageType.GP3)
            .allocatedStorage(20)
            .storageEncrypted(true)
            .publiclyAccessible(false)
            .multiAz(config.production())
            .backupRetention(Duration.days(config.production() ? 35 : 7))
            .allowMajorVersionUpgrade(true)
            .removalPolicy(RemovalPolicy.RETAIN)
            .deletionProtection(config.production())
            .build();
    database.getSecret().applyRemovalPolicy(RemovalPolicy.RETAIN);
    database.getConnections().allowFrom(serviceSecurityGroup, Port.tcp(5432), "SendRec tasks");

    software.amazon.awscdk.services.secretsmanager.Secret jwtSecret =
        software.amazon.awscdk.services.secretsmanager.Secret.Builder.create(this, "JwtSecret")
            .secretName(prefix + "/jwt-secret")
            .description("SendRec session signing key")
            .generateSecretString(
                SecretStringGenerator.builder().passwordLength(64).excludePunctuation(true).build())
            .removalPolicy(RemovalPolicy.RETAIN)
            .build();

    if (!config.production()) {
    // A controlled evaluation receiver validates HMAC and never forwards messages.
    software.amazon.awscdk.services.secretsmanager.Secret receiverSecret =
        software.amazon.awscdk.services.secretsmanager.Secret.Builder.create(this, "SyntheticReceiverSecret")
            .secretName(prefix + "/synthetic-webhook-verifier")
            .generateSecretString(SecretStringGenerator.builder().passwordLength(64).excludePunctuation(true).build())
            .removalPolicy(RemovalPolicy.RETAIN).build();
    String receiverCode;
    try {
      receiverCode = java.nio.file.Files.readString(java.nio.file.Path.of("synthetic-receiver.py"));
    } catch (java.io.IOException error) {
      throw new java.io.UncheckedIOException(error);
    }
    software.amazon.awscdk.services.lambda.Function receiver =
        software.amazon.awscdk.services.lambda.Function.Builder.create(this, "SyntheticReceiver")
            .functionName(prefix + "-synthetic-receiver")
            .runtime(software.amazon.awscdk.services.lambda.Runtime.PYTHON_3_13)
            .handler("index.handler")
            .code(software.amazon.awscdk.services.lambda.Code.fromInline(receiverCode))
            .timeout(Duration.seconds(20))
            .environment(Map.of("SECRET_ARN", receiverSecret.getSecretArn(), "TARGET_URL", baseUrl))
            .build();
    receiverSecret.grantRead(receiver);
    software.amazon.awscdk.services.lambda.FunctionUrl receiverUrl = receiver.addFunctionUrl(
        software.amazon.awscdk.services.lambda.FunctionUrlOptions.builder()
            .authType(software.amazon.awscdk.services.lambda.FunctionUrlAuthType.NONE).build());
    CfnOutput.Builder.create(this, "SyntheticReceiverUrl").value(receiverUrl.getUrl()).build();
    CfnOutput.Builder.create(this, "SyntheticReceiverSecretName").value(receiverSecret.getSecretName()).build();

    }

    // Compute -----------------------------------------------------------------------------
    Cluster cluster =
        Cluster.Builder.create(this, "Cluster")
            .clusterName(prefix + "-cluster")
            .vpc(vpc)
            .containerInsightsV2(software.amazon.awscdk.services.ecs.ContainerInsights.DISABLED)
            .build();

    FargateTaskDefinition taskDefinition =
        FargateTaskDefinition.Builder.create(this, "TaskDefinition")
            .family(prefix)
            .cpu(config.taskCpu())
            .memoryLimitMiB(config.taskMemoryMiB())
            .build();
    recordings.grantReadWrite(taskDefinition.getTaskRole());
    taskDefinition.getTaskRole().addToPrincipalPolicy(
        software.amazon.awscdk.services.iam.PolicyStatement.Builder.create()
            .actions(List.of("s3:GetBucketCORS"))
            .resources(List.of(recordings.getBucketArn())).build());

    LogGroup logGroup =
        LogGroup.Builder.create(this, "LogGroup")
            .logGroupName("/ecs/" + prefix)
            .retention(RetentionDays.ONE_MONTH)
            .removalPolicy(RemovalPolicy.DESTROY)
            .build();

    Map<String, String> environment = new HashMap<>();
    environment.put("PORT", String.valueOf(CONTAINER_PORT));
    environment.put("BASE_URL", baseUrl);
    environment.put("TRUSTED_PROXY", "true");
    environment.put("S3_ENDPOINT", "https://s3." + this.getRegion() + ".amazonaws.com");
    environment.put("S3_PUBLIC_ENDPOINT", "https://s3." + this.getRegion() + ".amazonaws.com");
    environment.put("S3_BUCKET", recordings.getBucketName());
    environment.put("S3_REGION", this.getRegion());
    environment.put("MAX_UPLOAD_BYTES", String.valueOf(2L * 1024 * 1024 * 1024));
    environment.put("DB_SSLMODE", "require");
    environment.put("DB_NAME", "sendrec");
    environment.put("REGISTRATION_ENABLED", Boolean.toString(options.bootstrap));
    environment.put("API_DOCS_ENABLED", "true");
    environment.put("TRANSCRIPTION_ENABLED", "true");
    environment.put("TRANSCRIPTION_PROVIDER", "local");
    environment.put("WHISPER_MODEL_PATH", "/tmp/models/ggml-small.bin");
    environment.put("WHISPER_MODEL_URL", WHISPER_MODEL_URL);
    environment.put("WHISPER_MODEL_SHA256", WHISPER_MODEL_SHA256);
    environment.put("MAX_CONCURRENT_ENCODES", "1");
    environment.put("NOISE_REDUCTION_FILTER", "arnndn=m=/app/models/std.rnnn");
    environment.put("EMAIL_FROM_ADDRESS", "noreply@myperformancedoctor.com");
    environment.put("EMAIL_FROM_NAME", "My Performance Doctor");
    environment.put("BRANDING_DEFAULT_NAME", "My Performance Doctor");

    Map<String, Secret> secrets = new HashMap<>();
    secrets.put("DB_HOST", Secret.fromSecretsManager(database.getSecret(), "host"));
    secrets.put("DB_PORT", Secret.fromSecretsManager(database.getSecret(), "port"));
    secrets.put("DB_USER", Secret.fromSecretsManager(database.getSecret(), "username"));
    secrets.put("DB_PASSWORD", Secret.fromSecretsManager(database.getSecret(), "password"));
    secrets.put("JWT_SECRET", Secret.fromSecretsManager(jwtSecret));
    MpdIntegrationConfig.apply(this, environment, secrets);

    if (controlledRelease) {
      environment.put("MIGRATIONS_MODE", "skip");
      environment.put("MPD_MANAGED_MODE", "true");
      environment.put("AI_ENABLED", "false");
      environment.put("OPTIONAL_WORKERS_ENABLED", "false");
      environment.put("LEGACY_WEBHOOKS_ENABLED", "false");
      environment.put("REGISTRATION_ENABLED", "false");
      environment.put("API_DOCS_ENABLED", "false");
    }
    ContainerImage releaseImage;
    if (controlledRelease) {
      String repositoryName = image.substring(image.indexOf('/') + 1, image.indexOf('@'));
      var repository = software.amazon.awscdk.services.ecr.Repository.fromRepositoryArn(this, "ReleaseRepository",
          "arn:aws:ecr:" + getRegion() + ":" + getAccount() + ":repository/" + repositoryName);
      releaseImage = ContainerImage.fromEcrRepository(repository, image.substring(image.indexOf('@') + 1));
    } else {
      releaseImage = ContainerImage.fromAsset("..", AssetImageProps.builder().file("Dockerfile")
          .platform(Platform.LINUX_AMD64).build());
    }
    taskDefinition.addContainer(
        "sendrec",
        ContainerDefinitionOptions.builder()
            .image(releaseImage)
            .environment(environment)
            .secrets(secrets)
            .portMappings(List.of(PortMapping.builder().containerPort(CONTAINER_PORT).build()))
            .logging(
                LogDrivers.awsLogs(
                    AwsLogDriverProps.builder().logGroup(logGroup).streamPrefix("sendrec").build()))
            .build());

    FargateService service =
        FargateService.Builder.create(this, "Service")
            .cluster(cluster)
            .serviceName(prefix + "-service")
            .taskDefinition(taskDefinition)
            .desiredCount(config.production() ? 0 : 1)
            .minHealthyPercent(config.production() ? 100 : 0)
            .maxHealthyPercent(config.production() ? 200 : 100)
            .circuitBreaker(software.amazon.awscdk.services.ecs.DeploymentCircuitBreaker.builder().rollback(true).build())
            .securityGroups(List.of(serviceSecurityGroup))
            .vpcSubnets(
                SubnetSelection.builder().subnetType(SubnetType.PRIVATE_WITH_EGRESS).build())
            .healthCheckGracePeriod(Duration.seconds(600))
            .build();

    ApplicationTargetGroup targetGroup =
        ApplicationTargetGroup.Builder.create(this, "PrivateTargetGroup")
            .vpc(vpc)
            .port(CONTAINER_PORT)
            .protocol(ApplicationProtocol.HTTP)
            .targetType(TargetType.IP)
            .deregistrationDelay(Duration.seconds(30))
            .healthCheck(
                HealthCheck.builder()
                    .path(controlledRelease ? "/api/ready" : "/api/health")
                    .healthyHttpCodes("200")
                    .interval(Duration.seconds(30))
                    .timeout(Duration.seconds(5))
                    .healthyThresholdCount(2)
                    .unhealthyThresholdCount(5)
                    .build())
            .build();
    targetGroup.addTarget(service);

      ApplicationListener.Builder.create(this, "PrivateListener")
          .loadBalancer(alb)
          .port(80)
          .open(false)
          .protocol(ApplicationProtocol.HTTP)
          .defaultAction(ListenerAction.forward(List.of(targetGroup)))
          .build();

    if (controlledRelease) {
      FargateTaskDefinition migration = FargateTaskDefinition.Builder.create(this, "MigrationTaskDefinition")
          .family(prefix + "-migration").cpu(256).memoryLimitMiB(512).build();
      Map<String, String> migrationEnvironment = new HashMap<>();
      migrationEnvironment.put("DB_SSLMODE", "require");
      migrationEnvironment.put("DB_NAME", "sendrec");
      migrationEnvironment.put("MIGRATIONS_MODE", "only");
      Map<String, Secret> migrationSecrets = new HashMap<>();
      for (String name : List.of("DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD")) migrationSecrets.put(name, secrets.get(name));
      migration.addContainer("migration", ContainerDefinitionOptions.builder().image(releaseImage)
          .environment(migrationEnvironment).secrets(migrationSecrets)
          .logging(LogDrivers.awsLogs(AwsLogDriverProps.builder().logGroup(logGroup).streamPrefix("migration").build())).build());
      CfnOutput.Builder.create(this, "MigrationTaskDefinitionArn").value(migration.getTaskDefinitionArn()).build();
      if (config.production()) addProductionAlarms(config, database, service, targetGroup, logGroup);
    }
    CfnOutput.Builder.create(this, "ClusterArn").value(cluster.getClusterArn()).build();
    CfnOutput.Builder.create(this, "ServiceArn").value(service.getServiceArn()).build();
    CfnOutput.Builder.create(this, "ApplicationTaskDefinitionArn").value(taskDefinition.getTaskDefinitionArn()).build();
    CfnOutput.Builder.create(this, "ServiceSecurityGroupId").value(serviceSecurityGroup.getSecurityGroupId()).build();
    CfnOutput.Builder.create(this, "PrivateSubnetIds").value(String.join(",", vpc.selectSubnets(
        SubnetSelection.builder().subnetType(SubnetType.PRIVATE_WITH_EGRESS).build()).getSubnetIds())).build();
    CfnOutput.Builder.create(this, "BaseUrl").value(baseUrl).build();
    CfnOutput.Builder.create(this, "AlbDnsName").value(alb.getLoadBalancerDnsName()).build();
    CfnOutput.Builder.create(this, "DistributionId").value(distribution.getDistributionId()).build();
    CfnOutput.Builder.create(this, "ClusterName").value(cluster.getClusterName()).build();
    CfnOutput.Builder.create(this, "ServiceName").value(service.getServiceName()).build();
    CfnOutput.Builder.create(this, "RecordingsBucket").value(recordings.getBucketName()).build();
    CfnOutput.Builder.create(this, "DatabaseSecret")
        .value(database.getSecret().getSecretName())
        .build();
    // RDS returns an attached-secret wrapper. Retaining that wrapper alone does
    // not retain the generated credential resource, so retain every owned secret.
    for (software.constructs.IConstruct child : getNode().findAll()) {
      if (child instanceof software.amazon.awscdk.services.secretsmanager.CfnSecret secret) {
        secret.applyRemovalPolicy(RemovalPolicy.RETAIN);
      }
    }
  }
  private void addProductionAlarms(EnvironmentConfig config, DatabaseInstance database,
      FargateService service, ApplicationTargetGroup targets, LogGroup logs) {
    String probeCode;
    try { probeCode = java.nio.file.Files.readString(java.nio.file.Path.of("backup-probe.py")); }
    catch (java.io.IOException error) { throw new java.io.UncheckedIOException(error); }
    var backupProbe = software.amazon.awscdk.services.lambda.Function.Builder.create(this, "BackupProbe")
        .runtime(software.amazon.awscdk.services.lambda.Runtime.PYTHON_3_13).handler("index.handler")
        .code(software.amazon.awscdk.services.lambda.Code.fromInline(probeCode)).timeout(Duration.seconds(30))
        .logGroup(logs).environment(Map.of("DATABASE_IDENTIFIER", database.getInstanceIdentifier(),
            "CLUSTER_ARN", service.getCluster().getClusterArn(), "SERVICE_ARN", service.getServiceArn())).build();
    backupProbe.addToRolePolicy(software.amazon.awscdk.services.iam.PolicyStatement.Builder.create()
        .actions(List.of("rds:DescribeDBInstances")).resources(List.of(database.getInstanceArn())).build());
    backupProbe.addToRolePolicy(software.amazon.awscdk.services.iam.PolicyStatement.Builder.create()
        .actions(List.of("ecs:DescribeServices")).resources(List.of(service.getServiceArn())).build());
    software.amazon.awscdk.services.events.Rule.Builder.create(this, "BackupProbeSchedule")
        .schedule(software.amazon.awscdk.services.events.Schedule.rate(Duration.minutes(5)))
        .targets(List.of(new software.amazon.awscdk.services.events.targets.LambdaFunction(backupProbe))).build();
    var destination = software.amazon.awscdk.services.sns.Topic.fromTopicArn(this, "OperationalDestination", config.alertTopicArn());
    Map<String, software.amazon.awscdk.services.cloudwatch.IMetric> metrics = new HashMap<>();
    metrics.put("Target5xx", targets.metricHttpCodeTarget(software.amazon.awscdk.services.elasticloadbalancingv2.HttpCodeTarget.TARGET_5XX_COUNT));
    metrics.put("UnhealthyTargets", targets.metricUnhealthyHostCount());
    metrics.put("DatabaseCPU", database.metricCPUUtilization());
    for (String name : List.of("UploadFailures", "ProcessingFailures", "OldProcessingJobs", "OutboxOldestSeconds", "OutboxDeadLetters", "BackupAgeSeconds")) {
      // Application/backup probes emit numeric counters only, never row IDs or signed URLs.
      software.amazon.awscdk.services.logs.MetricFilter.Builder.create(this, name + "Filter")
          .logGroup(logs).filterPattern(software.amazon.awscdk.services.logs.FilterPattern.exists("$." + name))
          .metricNamespace("MPD/SendRecProduction").metricName(name).metricValue("$." + name).build();
      metrics.put(name, software.amazon.awscdk.services.cloudwatch.Metric.Builder.create()
          .namespace("MPD/SendRecProduction").metricName(name).statistic("Maximum").period(Duration.minutes(5)).build());
    }
    // This probe runs independently of application tasks, so missing application
    // data can be distinguished from an intentionally dormant, zero-task service.
    software.amazon.awscdk.services.logs.MetricFilter.Builder.create(this, "ServiceDesiredCountFilter")
        .logGroup(logs).filterPattern(software.amazon.awscdk.services.logs.FilterPattern.exists("$.ServiceDesiredCount"))
        .metricNamespace("MPD/SendRecProduction").metricName("ServiceDesiredCount")
        .metricValue("$.ServiceDesiredCount").build();
    var desiredCount = software.amazon.awscdk.services.cloudwatch.Metric.Builder.create()
        .namespace("MPD/SendRecProduction").metricName("ServiceDesiredCount")
        .statistic("Maximum").period(Duration.minutes(5)).build();
    for (var item : metrics.entrySet()) {
      double threshold = switch (item.getKey()) {
        case "DatabaseCPU" -> 85;
        case "OutboxOldestSeconds" -> 300;
        case "BackupAgeSeconds" -> 900;
        default -> 1;
      };
      boolean applicationMetric = !item.getKey().equals("DatabaseCPU") && !item.getKey().equals("BackupAgeSeconds");
      software.amazon.awscdk.services.cloudwatch.IMetric signal = item.getValue();
      if (applicationMetric) {
        // Missing active-task probes must breach. Idle 5xx has no data by design.
        double missingValue = item.getKey().equals("Target5xx") ? 0 : threshold;
        signal = software.amazon.awscdk.services.cloudwatch.MathExpression.Builder.create()
            .expression("IF(desired > 0, FILL(signal, " + missingValue + "), 0)")
            .usingMetrics(Map.of("desired", desiredCount, "signal", item.getValue()))
            .period(Duration.minutes(5)).build();
      }
      var alarm = software.amazon.awscdk.services.cloudwatch.Alarm.Builder.create(this, item.getKey() + "Alarm")
          .metric(signal).threshold(threshold).evaluationPeriods(2)
          .comparisonOperator(software.amazon.awscdk.services.cloudwatch.ComparisonOperator.GREATER_THAN_OR_EQUAL_TO_THRESHOLD)
          // Missing probe metrics are failures, including a stopped backup probe.
          .treatMissingData(item.getKey().equals("Target5xx")
              ? software.amazon.awscdk.services.cloudwatch.TreatMissingData.NOT_BREACHING
              : software.amazon.awscdk.services.cloudwatch.TreatMissingData.BREACHING).build();
      alarm.addAlarmAction(new software.amazon.awscdk.services.cloudwatch.actions.SnsAction(destination));
    }
  }

}
