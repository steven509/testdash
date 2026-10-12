#!/usr/bin/env bash
# Copies the shop's jars from the Gradle cache, as the demo builds offline.
set -euo pipefail
cd "$(dirname "$0")"
cache="${GRADLE_USER_HOME:-$HOME/.gradle}/caches/modules-2/files-2.1"
copy() {
  local dir=$1 coordinates=$2 jar
  IFS=: read -r group artifact version <<<"$coordinates"
  jar=$(find "$cache/$group/$artifact/$version" -name "$artifact-$version.jar" | head -1)
  [[ -n $jar ]] || { echo "missing $coordinates in the Gradle cache" >&2; exit 1; }
  cp "$jar" "libs/$dir/"
}
rm -rf libs && mkdir -p libs/main libs/test
for coordinates in org.mariadb.jdbc:mariadb-java-client:3.0.10 org.apache.kafka:kafka-clients:3.3.2 \
  com.github.luben:zstd-jni:1.5.2-1 org.lz4:lz4-java:1.8.0 org.xerial.snappy:snappy-java:1.1.8.4 \
  org.slf4j:slf4j-api:2.0.18 org.mongodb:mongodb-driver-sync:4.6.1 org.mongodb:mongodb-driver-core:4.6.1 \
  org.mongodb:bson:4.6.1 org.mongodb:bson-record-codec:4.6.1 com.fasterxml.jackson.core:jackson-databind:2.21.4 \
  com.fasterxml.jackson.core:jackson-core:2.21.4 com.fasterxml.jackson.core:jackson-annotations:2.21; do
  copy main "$coordinates"
done
for coordinates in org.junit.jupiter:junit-jupiter-api:5.12.2 org.junit.jupiter:junit-jupiter-engine:5.12.2 \
  org.junit.jupiter:junit-jupiter-params:5.12.2 org.junit.platform:junit-platform-commons:1.12.2 \
  org.junit.platform:junit-platform-engine:1.12.2 org.junit.platform:junit-platform-launcher:1.12.2 \
  org.opentest4j:opentest4j:1.3.0 org.apiguardian:apiguardian-api:1.1.2; do
  copy test "$coordinates"
done
