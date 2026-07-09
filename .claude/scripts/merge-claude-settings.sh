#!/bin/bash
# Merges settings/settings.redact.json from this layer into the project's .claude/settings.json

set -e

# Get the directory where this layer is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LAYER_DIR="$(dirname "$SCRIPT_DIR")"

# Source settings from the layer
SOURCE_SETTINGS="$LAYER_DIR/settings/settings.redact.json"

# Target settings in the project using this layer
PROJECT_DIR="${CLAUDE_PROJECT_DIR:-.}"
TARGET_SETTINGS="$PROJECT_DIR/.claude/settings.json"

# Ensure target directory exists
mkdir -p "$(dirname "$TARGET_SETTINGS")"

# If target doesn't exist, just copy source
if [ ! -f "$TARGET_SETTINGS" ]; then
    cp "$SOURCE_SETTINGS" "$TARGET_SETTINGS"
    echo "Created $TARGET_SETTINGS from layer settings"
    exit 0
fi

# Merge using Python
python3 - "$SOURCE_SETTINGS" "$TARGET_SETTINGS" << 'EOF'
import json
import sys

def deep_merge(base, overlay):
    """Deep merge overlay into base, concatenating arrays."""
    if isinstance(base, dict) and isinstance(overlay, dict):
        result = base.copy()
        for key, value in overlay.items():
            if key in result:
                result[key] = deep_merge(result[key], value)
            else:
                result[key] = value
        return result
    elif isinstance(base, list) and isinstance(overlay, list):
        # Concatenate arrays, avoiding duplicates for simple values
        result = base.copy()
        for item in overlay:
            if item not in result:
                result.append(item)
        return result
    else:
        # Overlay wins for scalar values
        return overlay

source_path = sys.argv[1]
target_path = sys.argv[2]

with open(source_path) as f:
    source = json.load(f)

with open(target_path) as f:
    target = json.load(f)

merged = deep_merge(target, source)

with open(target_path, 'w') as f:
    json.dump(merged, f, indent=2)
    f.write('\n')

print(f"Merged layer settings into {target_path}")
EOF
