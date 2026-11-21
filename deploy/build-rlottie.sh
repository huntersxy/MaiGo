#!/bin/sh
set -eu

revision=36ddb42d78d1b13c1b1d7e1699aef8a9f339ab6f
checksum=ac7673afc60ca3c35a3d144c01ee611a2b01b052155ce225ad27b1f5d21dae9fa6cf02b5761a6ee8faade639b0aba2862938e2b26b8e3b43451974e7b0adef41
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT

archive="$build_dir/source.tar.gz"
if [ "$#" -gt 0 ]; then
  cp "$1" "$archive"
else
  curl --fail --silent --show-error --location --retry 3 --max-time 90 -o "$archive" \
    "https://codeload.github.com/Samsung/rlottie/tar.gz/$revision"
fi
printf '%s  %s\n' "$checksum" "$archive" | sha512sum -c -
tar -xzf "$archive" -C "$build_dir"
source_dir="$build_dir/rlottie-$revision"
meson setup "$build_dir/build" "$source_dir" --prefix /usr/local --libdir lib --default-library static \
  -Dexample=false -Dmodule=false -Dthread=false -Dcache=false
ninja -C "$build_dir/build" -j 4 install

license_dir=/usr/local/share/licenses/rlottie
mkdir -p "$license_dir"
cp "$source_dir/COPYING" "$source_dir"/licenses/* "$license_dir/"
printf 'Source: https://github.com/Samsung/rlottie/tree/%s\n' "$revision" > "$license_dir/SOURCE"
