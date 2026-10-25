# Shell hook scripts

`burnout install-hooks` renders these with the binary's path and the
subcommand allowlist, and installs them. See
docs/system-design.md §4.2 and docs/privacy-and-security.md §4.2.

## Vendored: bash-preexec

`bash-preexec.sh` is [bash-preexec](https://github.com/rcaloras/bash-preexec)
0.5.0, unmodified, under the MIT licence in `bash-preexec.LICENSE.md`.

- sha256 `6ef95f16012bdfec0ab6ee853e8892d51eee295c8639944f9b983b3d40e0e9f7`
- Reviewed: no network or file access. The command line only goes through
  a pipe to `sed`, never into a process's arguments.
- The bash hook loads it only when nothing else has (starship, atuin and
  iTerm2 load the same library), and keeps the user's `HISTCONTROL`
  instead of letting it drop `ignorespace`.

To update it, replace the file, check the diff the same way, and update
the version and checksum here (a test checks the checksum).
