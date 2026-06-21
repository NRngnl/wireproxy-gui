#!/usr/bin/env bash
set -euo pipefail

app_name="Wireproxy GUI"
binary_name="wireproxy-gui"
bundle_id="com.github.nrngnl.wireproxy-gui"
module_path="github.com/NRngnl/wireproxy-gui"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

version="${RELEASE_VERSION:-}"
if [[ -z "$version" && -f VERSION ]]; then
	version="$(tr -d '[:space:]' < VERSION)"
fi
version="${version#refs/tags/}"
version="${version#v}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
	echo "release version must look like 0.1.0; got '$version'" >&2
	exit 1
fi

target_os="${GOOS:-$(go env GOOS)}"
target_arch="${GOARCH:-$(go env GOARCH)}"
target_cc="${CC:-}"
arch_label="$target_arch"
if [[ "$arch_label" == "arm64" ]]; then
	arch_label="aarch64"
fi
if [[ "$target_os" == "darwin" && "$target_arch" == "amd64" ]]; then
	arch_label="x86_64"
fi

commit="$(git rev-parse --short=12 HEAD 2>/dev/null || printf unknown)"
build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
release_root="dist/release"
package_name="${binary_name}_${version}_${target_os}_${arch_label}"
package_dir="${release_root}/${package_name}"
artifact_dir="${release_root}/artifacts"
executable="$binary_name"
if [[ "$target_os" == "windows" ]]; then
	executable="${binary_name}.exe"
fi

rm -rf "$package_dir"
mkdir -p "$package_dir" "$artifact_dir"

ldflags=(
	"-s"
	"-w"
	"-X" "${module_path}/internal/buildinfo.Version=${version}"
	"-X" "${module_path}/internal/buildinfo.Commit=${commit}"
	"-X" "${module_path}/internal/buildinfo.Date=${build_date}"
)
if [[ "$target_os" == "windows" ]]; then
	ldflags+=("-H=windowsgui")
fi

echo "building ${package_name}"
if [[ -n "$target_cc" ]]; then
	GOOS="$target_os" GOARCH="$target_arch" CGO_ENABLED="${CGO_ENABLED:-1}" CC="$target_cc" \
		go build -trimpath -ldflags "${ldflags[*]}" -o "${package_dir}/${executable}" ./cmd/wireproxy-gui
else
	GOOS="$target_os" GOARCH="$target_arch" CGO_ENABLED="${CGO_ENABLED:-1}" \
		go build -trimpath -ldflags "${ldflags[*]}" -o "${package_dir}/${executable}" ./cmd/wireproxy-gui
fi

cp README.md LICENSE "$package_dir/"

sha256_file() {
	local file="$1"
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$file" > "${file}.sha256"
	else
		sha256sum "$file" > "${file}.sha256"
	fi
}

zip_dir() {
	local source_dir="$1"
	local output="$2"
	if command -v ditto >/dev/null 2>&1; then
		ditto -c -k --sequesterRsrc --keepParent "$source_dir" "$output"
		return
	fi
	if [[ "$target_os" == "windows" ]] && command -v powershell.exe >/dev/null 2>&1; then
		local source_path="$source_dir"
		local output_path="$output"
		if command -v cygpath >/dev/null 2>&1; then
			source_path="$(cygpath -w "$source_dir")"
			output_path="$(cygpath -w "$output")"
		fi
		powershell.exe -NoProfile -Command "\$ErrorActionPreference = 'Stop'; Compress-Archive -Path (Join-Path '${source_path}' '*') -DestinationPath '${output_path}' -Force"
		return
	fi
	if command -v zip >/dev/null 2>&1; then
		(cd "$(dirname "$source_dir")" && zip -qry "$repo_root/$output" "$(basename "$source_dir")")
		return
	fi
	echo "no zip tool found" >&2
	exit 1
}

if [[ "$target_os" == "darwin" ]]; then
	app_dir="${package_dir}/${app_name}.app"
	mkdir -p "${app_dir}/Contents/MacOS" "${app_dir}/Contents/Resources"
	mv "${package_dir}/${executable}" "${app_dir}/Contents/MacOS/${binary_name}"
	chmod +x "${app_dir}/Contents/MacOS/${binary_name}"
	cat > "${app_dir}/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>en</string>
	<key>CFBundleDisplayName</key>
	<string>${app_name}</string>
	<key>CFBundleExecutable</key>
	<string>${binary_name}</string>
	<key>CFBundleIdentifier</key>
	<string>${bundle_id}</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleName</key>
	<string>${app_name}</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>${version}</string>
	<key>CFBundleVersion</key>
	<string>${version}</string>
	<key>LSApplicationCategoryType</key>
	<string>public.app-category.utilities</string>
	<key>LSMinimumSystemVersion</key>
	<string>12.0</string>
	<key>NSHighResolutionCapable</key>
	<true/>
</dict>
</plist>
PLIST
	if command -v plutil >/dev/null 2>&1; then
		plutil -lint "${app_dir}/Contents/Info.plist"
	fi
	if command -v codesign >/dev/null 2>&1; then
		codesign --force --deep --sign - "$app_dir"
	fi
	archive="${artifact_dir}/${package_name}.app.zip"
	rm -f "$archive" "${archive}.sha256"
	zip_dir "$app_dir" "$archive"
	sha256_file "$archive"
else
	if [[ "$target_os" == "windows" ]]; then
		archive="${artifact_dir}/${package_name}.zip"
		rm -f "$archive" "${archive}.sha256"
		zip_dir "$package_dir" "$archive"
	else
		archive="${artifact_dir}/${package_name}.tar.gz"
		rm -f "$archive" "${archive}.sha256"
		tar -czf "$archive" -C "$release_root" "$package_name"
	fi
	sha256_file "$archive"
fi

echo "wrote ${archive}"
