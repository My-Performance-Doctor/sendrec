package com.mpd.sendrec.infrastructure;

/**
 * A public hostname for the distribution, such as results-staging.mpd.health, and its ACM
 * certificate. CloudFront takes certificates only from us-east-1. It lives in the committed
 * environment config, not a deploy context: a deploy that dropped it would take the alias off
 * the distribution and break every link already sent.
 */
record CustomDomain(String name, String certificateArn) {
  private static final String LABEL = "[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?";
  private static final String UUID = "[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}";

  CustomDomain {
    if (name == null || !name.matches(LABEL + "(?:\\." + LABEL + ")+"))
      throw new IllegalArgumentException("The custom domain must be a lowercase hostname");
    if (certificateArn == null || !certificateArn.matches("arn:aws:acm:us-east-1:[0-9]{12}:certificate/" + UUID))
      throw new IllegalArgumentException("The custom domain's certificate must be an ACM certificate in us-east-1");
  }

  String account() {
    return certificateArn.split(":")[4];
  }
}
