# Security policy

BurntOut watches how you work, so a security problem in it is also a
privacy problem. Thank you for reporting one privately.

## Reporting a vulnerability

**Please don't open a public issue.** Report it through GitHub's private
vulnerability reporting instead:
[Security → Report a vulnerability](https://github.com/kshitijdhara/burntout/security/advisories/new).

Include what you can of:

- the version (`burntout version`) and operating system;
- what an attacker can do, and what they need first (another local user,
  the same user, a malicious website, physical access, …);
- steps to reproduce, or a proof of concept.

Please don't attach your own recorded data, a `doctor --bundle` archive or
logs that may contain it unless we ask; describe what you saw instead.

## What to expect

This is a small project with one maintainer, so these are goals, not
guarantees:

- an acknowledgement within **7 days**;
- an assessment, and a fix or a plan, within **30 days** for anything
  serious;
- credit in the release notes and the advisory, if you'd like it.

Please give us a chance to ship a fix before you disclose publicly; we'll
agree a date with you.

## Supported versions

BurntOut is in alpha. Only the **latest release**, including pre-releases,
gets security fixes. Update with `burntout update` (from `v0.1.0-alpha.4`
on); installs from before the rename to BurntOut, up to `v0.1.0-alpha.3`,
reinstall with the install script.

## Scope

In scope, besides the usual (code execution, privilege escalation, data
exposure):

- anything that breaks a promise in
  [Privacy & Security](docs/privacy-and-security.md): data leaving the
  device, window titles or command arguments being stored, recording
  without the tray icon showing (AC-1), installing or starting silently
  (AC-2), or another local user reaching your data or the daemon;
- the dashboard's local web server (launch token, session cookie, CSRF);
- release signing, the install scripts and `burntout update`;
- the GNOME Shell extension's private socket.

Out of scope: problems that need an attacker who already runs code as your
own user, unless BurntOut makes them worse; and the threat model's stated
non-goals (Privacy & Security §2).
