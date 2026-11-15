#!/usr/bin/env bash
# make-deb.sh packs the already-built student binary into a Debian package
# suitable for UOS / Kylin / Debian-based Linux (they are all deb-based).
# Usage: scripts/make-deb.sh <dist-name> <debarch>
#   e.g. scripts/make-deb.sh linux-amd64 amd64
set -euo pipefail

DIST="${1:?usage: make-deb.sh <dist-name> <debarch>}"
DEBARCH="${2:?usage: make-deb.sh <dist-name> <debarch>}"
VERSION="${VERSION:-dev}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="dist/$DIST"
[ -d "$SRC" ] || { echo "dist/$DIST not found" >&2; exit 1; }

PKG="dist/pkg-tianyan-student-$DIST"
rm -rf "$PKG"
mkdir -p "$PKG/DEBIAN" \
  "$PKG/opt/tianyan/bin" \
  "$PKG/etc/xdg/autostart" \
  "$PKG/usr/share/applications"

cp "$SRC/bin/student" "$PKG/opt/tianyan/bin/student"
chmod 755 "$PKG/opt/tianyan/bin/student"
cp "$SRC/docs/README.md" "$PKG/opt/tianyan/README.md" 2>/dev/null || true

cat > "$PKG/DEBIAN/control" <<EOF
Package: tianyan-student
Version: ${VERSION#v}
Architecture: $DEBARCH
Maintainer: TianYan Project
Section: utils
Priority: optional
Description: 天眼电子教室学生端（局域网机房管控）
 学生端随登录自动后台运行，支持屏幕监控广播、键鼠管控、
 锁屏、黑白名单与文件收发；需要图形会话（X11）。
EOF

cat > "$PKG/etc/xdg/autostart/tianyan-student.desktop" <<'EOF'
[Desktop Entry]
Type=Application
Name=TianYan Student
Comment=天眼电子教室学生端
Exec=/opt/tianyan/bin/student
Hidden=false
X-GNOME-Autostart-enabled=true
EOF

cp "$PKG/etc/xdg/autostart/tianyan-student.desktop" "$PKG/usr/share/applications/tianyan-student.desktop"

mkdir -p dist
dpkg-deb --build --root-owner-group "$PKG" \
  "dist/tianyan-student_${VERSION#v}_${DEBARCH}.deb"
echo "deb ready: dist/tianyan-student_${VERSION#v}_${DEBARCH}.deb"
