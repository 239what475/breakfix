#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/../.." && pwd)
manifest="$repo_root/docs-site/manifest.yaml"
cache_root=${DOCS_CACHE_DIR:-"$repo_root/.local/docs"}
upstream_dir="$cache_root/upstream"
public_dir="$cache_root/public"

manifest_value() {
	local key=$1
	awk -F: -v key="$key" '
		$1 == key {
			value = $0
			sub(/^[^:]*:[[:space:]]*/, "", value)
			gsub(/^"|"$/, "", value)
			print value
			exit
		}
	' "$manifest"
}

source_name=$(manifest_value source)
repository=$(manifest_value repository)
revision=$(manifest_value revision)
version=$(manifest_value version)
locale=$(manifest_value locale)
docs_prefix=$(manifest_value docs_prefix)
default_base_url=$(manifest_value default_base_url)
hugo_version=$(manifest_value hugo_version)
node_version=$(manifest_value node_version)

for value_name in source_name repository revision version locale docs_prefix default_base_url hugo_version node_version; do
	if [[ -z ${!value_name} ]]; then
		echo "manifest value is empty: $value_name" >&2
		exit 2
	fi
done

case "$docs_prefix" in
	/*/)
		;;
	*)
		echo "docs_prefix must start and end with '/': $docs_prefix" >&2
		exit 2
		;;
esac

case "$default_base_url" in
	*/)
		;;
	*)
		echo "default_base_url must end with '/': $default_base_url" >&2
		exit 2
		;;
esac

require_command() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "required command is not installed: $1" >&2
		exit 2
	}
}

resolve_hugo() {
	require_command hugo
	local version_output
	version_output=$(hugo version)
	if [[ "$version_output" != *"hugo v${hugo_version}"* || "$version_output" != *"+extended"* ]]; then
		echo "Hugo $hugo_version extended is required; found: $version_output" >&2
		exit 2
	fi
	command -v hugo
}

resolve_node() {
	require_command node
	local version_output
	version_output=$(node --version)
	if [[ "$version_output" != "v${node_version}" ]]; then
		echo "Node.js ${node_version} is required; found: $version_output" >&2
		exit 2
	fi
	command -v node
}

clean_directory() {
	local directory=$1
	mkdir -p "$directory"
	find "$directory" -mindepth 1 -delete
}

sync_upstream() {
	require_command git
	mkdir -p "$cache_root"
	if [[ ! -d "$upstream_dir/.git" ]]; then
		git clone --depth=1 --no-checkout "$repository" "$upstream_dir"
	fi
	git -C "$upstream_dir" fetch --depth=1 origin "$revision"
	git -C "$upstream_dir" checkout --detach --force "$revision"
	if [[ -n $(git -C "$upstream_dir" status --porcelain) ]]; then
		echo "upstream checkout has local changes: $upstream_dir" >&2
		exit 2
	fi
	git -C "$upstream_dir" submodule update --init --recursive --depth 1
	local actual_revision
	actual_revision=$(git -C "$upstream_dir" rev-parse HEAD)
	if [[ "$actual_revision" != "$revision" ]]; then
		echo "checked out revision mismatch: $actual_revision" >&2
		exit 1
	fi
}

write_build_info() {
	local base_url=$1
	local build_time
	build_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	cat >"$public_dir/build-info.json" <<EOF
{
  "source": "$source_name",
  "repository": "$repository",
  "revision": "$revision",
  "version": "$version",
  "locale": "$locale",
  "base_url": "$base_url",
  "built_at": "$build_time"
}
EOF
}

check_public() {
	if [[ ! -f "$public_dir/build-info.json" ]]; then
		echo "missing build-info.json" >&2
		return 1
	fi
	if [[ ! -f "$public_dir${docs_prefix}index.html" ]]; then
		echo "missing documentation entry page: $public_dir${docs_prefix}index.html" >&2
		return 1
	fi
	if [[ ! -f "$public_dir/index.html" || ! -f "$public_dir/404.html" || ! -f "$public_dir/sitemap.xml" ]]; then
		echo "upstream public tree is incomplete" >&2
		return 1
	fi
	if ! find "$public_dir/scss" -maxdepth 1 -type f -name '*.css' -print -quit | rg -q .; then
		echo "missing upstream stylesheet assets" >&2
		return 1
	fi
	if ! find "$public_dir/js" -maxdepth 1 -type f -name '*.js' -print -quit | rg -q .; then
		echo "missing upstream JavaScript assets" >&2
		return 1
	fi
	if ! rg -F -q "$revision" "$public_dir/build-info.json"; then
		echo "build-info.json does not contain pinned revision" >&2
		return 1
	fi
	local built_base_url
	built_base_url=$(awk -F'"' '/"base_url"/ { print $4; exit }' "$public_dir/build-info.json")
	if [[ -z "$built_base_url" ]] || ! rg -F -q "$built_base_url${docs_prefix#/}" "$public_dir${docs_prefix}index.html"; then
		echo "documentation entry page does not contain configured base URL" >&2
		return 1
	fi
	echo "documentation mirror output passed official public tree checks"
}

build_site() {
	sync_upstream
	local hugo_bin
	hugo_bin=$(resolve_hugo)
	require_command make
	local node_bin
	node_bin=$(resolve_node)
	require_command npm
	if [[ ! -d "$upstream_dir/node_modules/docsy" ]]; then
		(
			cd "$upstream_dir"
			npm ci --ignore-scripts
		)
	fi
	local base_url=${DOCS_BASE_URL:-$default_base_url}
	case "$base_url" in
	*/)
		;;
	*)
		echo "DOCS_BASE_URL must end in '/': $base_url" >&2
		exit 2
		;;
	esac
	clean_directory "$upstream_dir/public"
	(
		cd "$upstream_dir"
		HUGO_BASEURL="$base_url" PATH="$(dirname "$hugo_bin"):$(dirname "$node_bin"):$PATH" make production-build
	)
	clean_directory "$public_dir"
	cp -a "$upstream_dir/public/." "$public_dir/"
	write_build_info "$base_url"
	check_public
	echo "documentation mirror built at $public_dir"
}

serve_site() {
	require_command python3
	check_public
	local port=${DOCS_PORT:-1313}
	python3 -m http.server "$port" --bind 127.0.0.1 --directory "$public_dir"
}

case "${1:-}" in
	sync)
		sync_upstream
	;;
	build)
		build_site
	;;
	check)
		check_public
	;;
	serve)
		serve_site
	;;
	*)
		echo "usage: $0 {sync|build|check|serve}" >&2
		exit 2
		;;
esac
