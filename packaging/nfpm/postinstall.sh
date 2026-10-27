#!/bin/sh
# Runs as root after the .deb or .rpm is installed. It only prints next
# steps: a package must never start collection or register the service
# (privacy & security §5.2, AC-2). The service is per user, so the user
# sets it up from their own shell.
cat <<'MSG'

burnout is installed. Nothing is recorded yet.

Next steps, as your own user (not root):
  burnout setup             review what is collected and consent
  burnout service install   run in the background from login

MSG
exit 0
