#!/bin/sh
# Fail if the image's CA store trusts a root that must stay out of it: the
# three eMudhra roots Alpine's ca-certificates 20261020 removed after the 2026
# eMudhra intermediate compromise, and AAA Certificate Services. Run inside the
# image being checked; Dockerfile, Dockerfile.base and Dockerfile.standalone do.
#
# Reads every certificate in every file under /etc/ssl/certs, as Go does, not
# just the bundle. A PEM body is base64 of the certificate's DER, so its SHA-256
# is the certificate's fingerprint and no openssl is needed.
# shellcheck disable=SC3040 # Alpine's BusyBox ash supports pipefail.
set -eu -o pipefail

blocked='86a1ecba089c4a8d3bbe2734c612ba341d813e043cf9e8a862cd5c57a36bbe6b emSign ECC Root CA - G3
125609aa301da0a249b97a8239cb6a34216f44dcac9f3954b14292f2e8c8608f emSign Root CA - C1
bc4d809b15189d78db3e1d8cf4f9726a795da1643ca5f1358e1ddb0edc0d7eb3 emSign ECC Root CA - C3
d7a7a0fb5d7e2731d771e9484ebcdef71d5f0c3e0a2948782bc83ee0ea699ef4 AAA Certificate Services'

work=$(mktemp -d)
cd "$work"
awk '/-----BEGIN CERTIFICATE-----/ { n++; body = 1; next }
     /-----END CERTIFICATE-----/ { body = 0; close("cert" n); next }
     body { print > ("cert" n) }' /etc/ssl/certs/*
for cert in cert*; do base64 -d "$cert" | sha256sum | cut -d ' ' -f 1; done | sort -u > roots

# Each root appears several times (bundle, ca-cert-*.pem, hash link), so count
# unique fingerprints. Alpine 3.21 ships 118; far fewer means a broken store.
count=$(wc -l < roots)
if [ "$count" -lt 100 ]; then
  echo "CA store has only $count distinct roots" >&2
  exit 1
fi
if echo "$blocked" | grep -F -f roots; then
  echo "CA store trusts the blocked roots above" >&2
  exit 1
fi
cd /
rm -rf "$work"
echo "CA store: $count distinct roots, none blocked"
