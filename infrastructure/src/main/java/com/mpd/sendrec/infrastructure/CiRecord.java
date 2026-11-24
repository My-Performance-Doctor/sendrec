package com.mpd.sendrec.infrastructure;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import software.amazon.awscdk.Duration;
import software.amazon.awscdk.RemovalPolicy;
import software.amazon.awscdk.services.dynamodb.Attribute;
import software.amazon.awscdk.services.dynamodb.AttributeType;
import software.amazon.awscdk.services.dynamodb.BillingMode;
import software.amazon.awscdk.services.dynamodb.Table;
import software.amazon.awscdk.services.iam.CfnOIDCProvider;
import software.amazon.awscdk.services.iam.FederatedPrincipal;
import software.amazon.awscdk.services.iam.IPrincipal;
import software.amazon.awscdk.services.iam.PolicyStatement;
import software.amazon.awscdk.services.iam.Role;
import software.constructs.Construct;

/**
 * Where GitHub Actions records ci.yml's result for each commit on main, so WaitForGitHubCI can
 * prove the checks passed without any GitHub token (design D11). ci.yml's last job signs in through
 * GitHub's OIDC provider as {@link #ROLE_NAME}, which trusts only workflows running on main of this
 * repository, named by GitHub's immutable ids, and may only put one item in {@link #TABLE_NAME}.
 * The subject names the branch, not the event or the workflow file: any workflow on main that asks
 * for {@code id-token: write} could assume it, so ci.yml's record-result job is the only one that does.
 *
 * <p>One item per commit, keyed by the full SHA: the nightly release ships main's newest
 * staging-verified run, often hours after its CI passed and after newer runs superseded others, so
 * it must find the result of that exact commit, not the latest result. Items expire after 90 days.
 */
final class CiRecord extends Construct {
  static final String TABLE_NAME = "sendrec-ci-results";
  static final String ROLE_NAME = "sendrec-github-ci-record";
  static final String GITHUB_OIDC_HOST = "token.actions.githubusercontent.com";
  static final String AUDIENCE = "sts.amazonaws.com";
  /**
   * GitHub's immutable subject for a workflow on main of My-Performance-Doctor/sendrec: the
   * organisation's and repository's ids do not change when either is renamed, so a new repository
   * that takes the name cannot assume the role. Pull requests, forks, other branches, tags and
   * environments all sign with other subjects. The ids are public on GitHub.
   */
  static final String MAIN_SUBJECT = "repo:My-Performance-Doctor@199071226/sendrec@1408816431:ref:refs/heads/main";

  private final Table table;

  CiRecord(Construct scope, String id, String existingProviderArn) {
    super(scope, id);
    table = Table.Builder.create(this, "Results")
        .tableName(TABLE_NAME)
        .partitionKey(Attribute.builder().name("commit").type(AttributeType.STRING).build())
        .billingMode(BillingMode.PAY_PER_REQUEST)
        .timeToLiveAttribute("expiresAt")
        // Results only gate a release; ci.yml writes them again on a rerun.
        .removalPolicy(RemovalPolicy.DESTROY)
        .build();
    // The native resource, not the CDK construct that creates it through a Lambda and file asset.
    String providerArn = existingProviderArn != null ? existingProviderArn
        : CfnOIDCProvider.Builder.create(this, "GitHubProvider")
            .url("https://" + GITHUB_OIDC_HOST)
            .clientIdList(List.of(AUDIENCE))
            .build().getAttrArn();
    Role role = Role.Builder.create(this, "RecordRole")
        .roleName(ROLE_NAME)
        .description("ci.yml on main of sendrec records its result for the deployment pipeline")
        .maxSessionDuration(Duration.hours(1))
        .assumedBy(new FederatedPrincipal(providerArn, Map.of("StringEquals", trust()), "sts:AssumeRoleWithWebIdentity"))
        .build();
    role.addToPolicy(PolicyStatement.Builder.create()
        .actions(List.of("dynamodb:PutItem"))
        .resources(List.of(table.getTableArn()))
        .build());
  }

  /** Exact matches only, in a fixed order: no wildcard can widen the subject. */
  private static Map<String, String> trust() {
    Map<String, String> trust = new LinkedHashMap<>();
    trust.put(GITHUB_OIDC_HOST + ":aud", AUDIENCE);
    trust.put(GITHUB_OIDC_HOST + ":sub", MAIN_SUBJECT);
    return trust;
  }

  /** Lets the wait step read one commit's result, and nothing else. */
  void grantLookup(IPrincipal grantee) {
    grantee.addToPrincipalPolicy(PolicyStatement.Builder.create()
        .actions(List.of("dynamodb:GetItem"))
        .resources(List.of(table.getTableArn()))
        .build());
  }

}
