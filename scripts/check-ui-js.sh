#!/bin/bash
# Extract and check syntax of inline <script> blocks from HTML pages
set -e

NODE="/Users/djm56/.nvm/versions/node/v22.22.1/bin/node"

for page in missions triage performance crew; do
  echo "Checking $page.html..."
  # Extract all inline script content (between <script> and </script>)
  # and pipe to node --check
  grep -oP '(?<=<script>).+?(?=</script>)' "/Users/djm56/bishop-memory/internal/ui/${page}.html" | while read -r script; do
    echo "$script" | "$NODE" --check 2>&1 || {
      echo "Syntax error in $page.html: $script"
      exit 1
    }
  done
done

echo "All scripts passed syntax check"
