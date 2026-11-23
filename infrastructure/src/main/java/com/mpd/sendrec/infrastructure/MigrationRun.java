package com.mpd.sendrec.infrastructure;

import java.io.IOException;
import java.io.UncheckedIOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.Map;
import software.amazon.awscdk.CustomResource;
import software.amazon.awscdk.Duration;
import software.amazon.awscdk.Stack;
import software.amazon.awscdk.services.ec2.ISecurityGroup;
import software.amazon.awscdk.services.ecs.ICluster;
import software.amazon.awscdk.services.ecs.TaskDefinition;
import software.amazon.awscdk.services.iam.PolicyStatement;
import software.amazon.awscdk.services.lambda.Code;
import software.amazon.awscdk.services.lambda.Function;
import software.amazon.awscdk.services.lambda.Runtime;
import software.amazon.awscdk.services.logs.ILogGroup;
import software.constructs.Construct;

/**
 * Runs the release's migration task definition once per stack deploy, before the service changes
 * (`migration-runner.py`). Its properties name the definition's revision, so each new image runs
 * it once. The service depends on it, so a migration that does not exit 0 fails the deploy before
 * the service moves. The Lambda's code is inline, so a stack deploy publishes no file assets.
 */
final class MigrationRun {
  static final String CONTAINER = "migration";

  private MigrationRun() {}

  static CustomResource add(Construct scope, ICluster cluster, TaskDefinition migration, List<String> subnetIds,
      ISecurityGroup securityGroup, ILogGroup logs) {
    Stack stack = Stack.of(scope);
    Function runner = Function.Builder.create(scope, "MigrationRunner")
        .runtime(Runtime.PYTHON_3_13).handler("index.handler").code(Code.fromInline(code()))
        .timeout(Duration.minutes(15)).logGroup(logs)
        // CloudFormation invokes it asynchronously; a Lambda retry would start a second migration.
        .retryAttempts(0)
        .description("Runs the release's migration task before the service changes").build();
    runner.addToRolePolicy(PolicyStatement.Builder.create().actions(List.of("ecs:RunTask"))
        .resources(List.of(migration.getTaskDefinitionArn()))
        .conditions(Map.of("ArnEquals", Map.of("ecs:cluster", cluster.getClusterArn()))).build());
    runner.addToRolePolicy(PolicyStatement.Builder.create().actions(List.of("ecs:DescribeTasks"))
        .resources(List.of("arn:" + stack.getPartition() + ":ecs:" + stack.getRegion() + ":" + stack.getAccount()
            + ":task/" + cluster.getClusterName() + "/*"))
        .conditions(Map.of("ArnEquals", Map.of("ecs:cluster", cluster.getClusterArn()))).build());
    runner.addToRolePolicy(PolicyStatement.Builder.create().actions(List.of("iam:PassRole"))
        .resources(List.of(migration.getTaskRole().getRoleArn(), migration.obtainExecutionRole().getRoleArn())).build());
    runner.addToRolePolicy(PolicyStatement.Builder.create().actions(List.of("cloudformation:DescribeStacks"))
        .resources(List.of(stack.getStackId())).build());
    // A fixed order, so an unchanged resource never shows as changed.
    Map<String, Object> properties = new java.util.LinkedHashMap<>();
    properties.put("Cluster", cluster.getClusterArn());
    properties.put("TaskDefinition", migration.getTaskDefinitionArn());
    properties.put("Subnets", subnetIds);
    properties.put("SecurityGroups", List.of(securityGroup.getSecurityGroupId()));
    properties.put("Container", CONTAINER);
    CustomResource run = CustomResource.Builder.create(scope, "MigrationRun")
        .serviceToken(runner.getFunctionArn())
        // A runner that never answers fails the deploy after 16 minutes, not CloudFormation's hour.
        .serviceTimeout(Duration.minutes(16))
        .resourceType("Custom::MigrationRun")
        .properties(properties)
        .build();
    // The task's roles and their policies exist before it starts, so it can pull and read secrets.
    run.getNode().addDependency(migration.obtainExecutionRole(), migration.getTaskRole());
    return run;
  }

  private static String code() {
    try {
      return Files.readString(Path.of("migration-runner.py"));
    } catch (IOException error) {
      throw new UncheckedIOException(error);
    }
  }
}
