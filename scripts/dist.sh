#!/usr/bin/env bash
# dist.sh builds the teacher/student binaries for one platform target and
# assembles a distributable directory (dist/<name>/) with docs and
# platform-specific helpers (systemd units, autostart desktop entry,
# macOS .app bundle). CI compresses the directory per-platform.
#
# Usage: scripts/dist.sh <name> <goos> [debarch]
# Env:   VERSION (release tag), CGO_ENABLED, GOARCH via go env or env
set -euo pipefail

NAME="${1:?usage: dist.sh <name> <goos> [debarch]}"
GOOS="${2:?usage: dist.sh <name> <goos> [debarch]}"
DEBARCH="${3:-}"

VERSION="${VERSION:-dev}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

LDFLAGS="-s -w -X tianyan/internal/student.Version=${VERSION}"
OUT="dist/$NAME"
mkdir -p "$OUT/bin" "$OUT/docs"

EXE=""
if [ "$GOOS" = "windows" ]; then EXE=".exe"; fi

build() { # build <pkg> <output>
  # darwin/amd64 on an arm64 runner: point clang at the x86_64 slice.
  if [ "$GOOS" = "darwin" ] && [ "${GOARCH:-}" = "amd64" ]; then
    export CC="$(xcrun -f clang) -arch x86_64"
  fi
  CGO_ENABLED="${CGO_ENABLED:-0}" GOOS="$GOOS" GOARCH="${GOARCH:-}" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$1" "$2"
}

# The teacher binary is only meaningful on desktop platforms.
case "$GOOS" in
windows | darwin | linux)
  build "$OUT/bin/teacher$EXE" ./cmd/teacher
  ;;
esac
build "$OUT/bin/student$EXE" ./cmd/student

cp README.md LICENSE "$OUT/docs/"
if [ -d docs ] && ls docs/*.svg >/dev/null 2>&1; then cp docs/*.svg "$OUT/docs/"; fi

# Linux: systemd units + a graphical autostart entry for the student.
if [ "$GOOS" = "linux" ]; then
  mkdir -p "$OUT/systemd"
  cat > "$OUT/systemd/tianyan-student.service" <<'EOF'
[Unit]
Description=TianYan student client
After=graphical-session.target

[Service]
Type=simple
ExecStart=/opt/tianyan/bin/student
Restart=always
RestartSec=3

[Install]
WantedBy=graphical-session.target
EOF
  cat > "$OUT/systemd/tianyan-teacher.service" <<'EOF'
[Unit]
Description=TianYan teacher server
After=network.target

[Service]
Type=simple
ExecStart=/opt/tianyan/bin/teacher
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
  mkdir -p "$OUT/autostart"
  cat > "$OUT/autostart/tianyan-student.desktop" <<'EOF'
[Desktop Entry]
Type=Application
Name=TianYan Student
Exec=/opt/tianyan/bin/student
Hidden=false
X-GNOME-Autostart-enabled=true
EOF
fi

# macOS: wrap the binaries in .app bundles (unsigned; right-click → open
# on first launch, or `xattr -cr` after download).
if [ "$GOOS" = "darwin" ]; then
  for app in student teacher; do
    name="TianYan-Student"
    bin="student"
    if [ "$app" = "teacher" ]; then name="TianYan-Teacher"; bin="teacher"; fi
    mkdir -p "$OUT/$name.app/Contents/MacOS"
    cp "$OUT/bin/$bin" "$OUT/$name.app/Contents/MacOS/$bin"
    cat > "$OUT/$name.app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>$name</string>
  <key>CFBundleExecutable</key><string>$bin</string>
  <key>CFBundleIdentifier</key><string>io.tianyan.$app</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>LSMinimumSystemVersion</key><string>10.13</string>
</dict></plist>
EOF
    codesign --force -s - "$OUT/$name.app" 2>/dev/null || true
    rm "$OUT/$name.app/Contents/MacOS/$bin.tmp" 2>/dev/null || true
  done
fi

echo "dist ready: $OUT"
