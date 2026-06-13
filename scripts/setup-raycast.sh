#!/usr/bin/env bash
# setup-raycast.sh: drop tape script-commands into Raycast's user
# directory and (optionally) install Alfred workflow stubs.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/chenhg5/tape/main/scripts/setup-raycast.sh | bash
#
# Creates one script per common verb (sync / ls / search / export
# / overview). Each one is a tiny shell wrapper that launches
# `tape <verb>` in your default Terminal so the interactive picker
# can actually run. Raycast script-commands fire-and-forget by
# design — they can't host a TUI themselves.

set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
    echo "Raycast/Alfred quick-launchers are macOS only; on Linux/Windows just bind tape commands to your launcher manually." >&2
    exit 1
fi

RAYCAST_DIR="$HOME/Library/Application Support/Raycast/script-commands/tape"
mkdir -p "$RAYCAST_DIR"

write_script() {
    local file="$1" title="$2" subtitle="$3" body="$4"
    cat >"$RAYCAST_DIR/$file" <<EOF
#!/usr/bin/env bash
# @raycast.schemaVersion 1
# @raycast.title $title
# @raycast.mode silent
# @raycast.packageName Tape
# @raycast.subtitle $subtitle
# @raycast.icon 📼
# @raycast.author tape
# @raycast.authorURL https://github.com/chenhg5/tape

$body
EOF
    chmod +x "$RAYCAST_DIR/$file"
}

# Each verb opens Terminal.app and runs the command there. This
# beats "run silently and pipe output back" because every useful
# tape command on a TTY is interactive (picker, picker, picker).
launch_tape() {
    cat <<'EOF'
exec /usr/bin/osascript -e 'tell application "Terminal" to do script "tape '"$1"'"' >/dev/null
EOF
}

write_script tape-sync.sh        "Tape Sync"      "Archive new sessions from every installed agent"  "$(launch_tape sync)"
write_script tape-ls.sh          "Tape Browse"    "Browse archived sessions (interactive picker)"    "$(launch_tape ls)"
write_script tape-search.sh      "Tape Search"    "Full-text search across archived sessions"        "$(launch_tape search)"
write_script tape-export.sh      "Tape Export"    "Snapshot the archive as a tar.zst"                "$(launch_tape export)"
write_script tape-overview.sh    "Tape Overview"  "Show archive activity dashboard"                  "$(launch_tape overview)"

echo "✓ wrote 5 script-commands to:"
echo "    $RAYCAST_DIR"
echo
echo "Next steps in Raycast:"
echo "  1. Open Raycast Settings (⌘ + ,)"
echo "  2. Extensions → Script Commands"
echo "  3. Add Script Directory… → ~/Library/Application Support/Raycast/script-commands"
echo "  4. Reload Script Directories"
echo
echo "For Alfred users:"
echo "  Create a Keyword workflow whose action is \"Run Script /bin/bash\""
echo "  with body: tape <verb>. Same idea, less templating."
