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
    String prefix = "sendrec-" + config.environmentName();

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
                        .version(PostgresEngineVersion.VER_17)
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
            .multiAz(false)
            .backupRetention(Duration.days(7))
            .removalPolicy(RemovalPolicy.RETAIN)
            .deletionProtection(false)
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

    taskDefinition.addContainer(
        "sendrec",
        ContainerDefinitionOptions.builder()
            .image(
                ContainerImage.fromAsset(
                    "..",
                    AssetImageProps.builder()
                        .file("Dockerfile")
                        .platform(Platform.LINUX_AMD64)
                        .build()))
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
            .desiredCount(1)
            .minHealthyPercent(0)
            .maxHealthyPercent(100)
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
                    .path("/api/health")
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
}
