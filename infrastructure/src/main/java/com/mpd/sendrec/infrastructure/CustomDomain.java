package com.mpd.sendrec.infrastructure;

import software.amazon.awscdk.Stack;

/**
 * An optional public hostname for the distribution, such as results-staging.mpd.health.
 * Pass both contexts on every deploy or neither: leaving them out returns BASE_URL to the
 * CloudFront hostname. CloudFront takes certificates only from us-east-1.
 */
record CustomDomain(String name, String certificateArn) {
  private static final String HOST = "[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?";

  /** Returns null when neither context is set. */
  static CustomDomain fromContext(Stack scope) {
    String name = text(scope, "domainName");
    String arn = text(scope, "domainCertificateArn");
    if (name.isEmpty() && arn.isEmpty()) return null;
    if (name.isEmpty() || arn.isEmpty())
      throw new IllegalArgumentException("domainName and domainCertificateArn must be supplied together");
    if (!name.matches(HOST + "(?:\\." + HOST + ")+"))
      throw new IllegalArgumentException("domainName must be a lowercase hostname");
    if (!arn.matches("arn:aws:acm:us-east-1:" + scope.getAccount() + ":certificate/[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}"))
      throw new IllegalArgumentException("domainCertificateArn must be an ACM certificate in us-east-1 in this account");
    return new CustomDomain(name, arn);
  }

  private static String text(Stack scope, String key) {
    Object value = scope.getNode().tryGetContext(key);
    return value == null ? "" : value.toString().trim();
  }
}
