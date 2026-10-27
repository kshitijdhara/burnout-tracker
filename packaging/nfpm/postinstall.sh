#!/bin/sh
# Runs as root after the .deb or .rpm is installed. It only prints next
# steps: a package must never start collection, register the service or
# enable the GNOME Shell extension (privacy & security §5.2, AC-2). The
# service and the extension are per user, so the user sets them up from
# their own shell.
cat <<'MSG'

burnout is installed. Nothing is recorded yet.

Next steps, as your own user (not root):
  burnout setup             review what is collected and consent
  burnout service install   run in the background from login

On GNOME with Wayland, burnout reads windows through a small GNOME Shell
extension, installed but off. To turn it on, log out and back in (GNOME
only sees new extensions at login), then run:
  gnome-extensions enable burnout-bridge@kshitijdhara.github.io

MSG
exit 0
