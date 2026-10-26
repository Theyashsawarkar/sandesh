#!/usr/bin/env bash
# sandesh as a tmux plugin: TPM entry point (also works with a plain
# `run-shell` on this file). Binds prefix + @sandesh-key to open sandesh in
# a popup, and fills #{sandesh_status} in your status line with the unread
# counts.
#
# Options (set them before TPM's `run` line):
#   @sandesh-key       key after the prefix (default e; "none" binds nothing)
#   @sandesh-width     popup width  (default 96%)
#   @sandesh-height    popup height (default 94%)
#   @sandesh-title     popup title  (default " sandesh ")
#   @sandesh-launcher  your own popup command instead of display-popup: it
#                      is run with display-popup's arguments appended
#                      (-E -d DIR -T TITLE CMD), tmux formats expanded, e.g.
#                      "~/.tmux/scripts/popup.sh '#{client_name}'"
#
# Status line: put #{sandesh_status} in status-right (or status-left); it
# becomes "󰇮 3  󰭹 2" (unread mail threads, chat spaces), empty when
# there is nothing new.

dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
run="$dir/scripts/sandesh-run"

opt() { local v; v=$(tmux show-option -gqv "$1"); printf '%s' "${v:-$2}"; }
key=$(opt @sandesh-key e)
width=$(opt @sandesh-width 96%)
height=$(opt @sandesh-height 94%)
title=$(opt @sandesh-title ' sandesh ')
launcher=$(opt @sandesh-launcher '')

if [ "$key" != none ]; then
  cmd="'$run' --popup"
  if [ -n "$launcher" ]; then
    open="run -b \"$launcher -E -d '#{pane_current_path}' -T '$title' $cmd\""
  else
    open="display-popup -E -w '$width' -h '$height' -d '#{pane_current_path}' -T '$title' \"$cmd\""
  fi
  tmux bind-key -N "Open sandesh mail and chat popup (Gmail, Google Chat)" "$key" "$open"
fi

# #{sandesh_status} -> #('.../sandesh-run' status --tmux)
for side in status-right status-left; do
  value=$(tmux show-option -gqv "$side")
  case $value in
    *'#{sandesh_status}'*)
      tmux set-option -gq "$side" "${value//\#\{sandesh_status\}/#($run status --tmux)}" ;;
  esac
done
