#!/usr/bin/env bash
set -eu

binary=$1
profile=${2:-"$HOME/.bashrc"}
marker='# keys-switcher Bash integration'

if [ -d "$profile" ]; then
  printf '%s is a directory. Rename or remove it, then run the installer again.\n' "$profile" >&2
  exit 1
fi

if ! grep -Fqx "$marker" "$profile" 2>/dev/null; then
  {
    printf '\n%s\n' "$marker"
    printf 'eval "$(%q shell-init --binary %q bash)"\n' "$binary" "$binary"
    printf 'eval "$(%q env bash 2>/dev/null)"\n' "$binary"
  } >> "$profile"
fi

printf 'Bash integration ready. Run: source ~/.bashrc\n'
