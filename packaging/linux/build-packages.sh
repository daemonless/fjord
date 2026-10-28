#!/bin/sh
# Build fjord's .deb and .rpm for amd64 and arm64 from fjordd-linux-<arch>
# in the current directory (the release job's binaries), run from the repo
# root. Needs nfpm on PATH.
#   packaging/linux/build-packages.sh 0.4.0
set -eu
version=${1:?usage: build-packages.sh <version>}
# The packages run /usr/sbin/fjordd; the release's fjordd.service is
# install-linux.sh's, on /usr/local.
sed 's|^ExecStart=/usr/local/sbin/fjordd$|ExecStart=/usr/sbin/fjordd|' packaging/fjordd.service > fjordd.pkg.service
grep -qx 'ExecStart=/usr/sbin/fjordd' fjordd.pkg.service
for arch in amd64 arm64; do
  cp "fjordd-linux-$arch" fjordd # nfpm does not expand variables in paths
  for kind in deb rpm; do
    VERSION=$version ARCH=$arch nfpm package -f packaging/nfpm.yaml -p "$kind" -t "fjord-$version-linux-$arch.$kind"
  done
done
rm -f fjordd fjordd.pkg.service
ls -l fjord-"$version"-linux-*
