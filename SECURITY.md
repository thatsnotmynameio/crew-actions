# Security policy

This is crew's own policy. It replaces the default policy of the thatsnotmynameio repositories.

## Supported versions

Only the latest release gets fixes. Update before reporting: a crew installed from a release updates with `crew upgrade`, as the README's [Upgrading crew](README.md#upgrading-crew) says. A crew built with `go install` or in a checkout is updated the way it was built.

## Reporting a vulnerability

Report it privately through GitHub, with the form at <https://github.com/thatsnotmynameio/crew/security/advisories/new>, or through **Security → Report a vulnerability** on the repository. Please don't open a public issue, pull request or discussion about it.

Include:

- crew's version, as `crew --version` prints it;
- your OS and architecture;
- how to reproduce it, with anything private removed;
- what an attacker could do with it.

## What counts as a vulnerability

These are in scope:

- crew posting a secret, or what a session or a script printed, on a public issue, pull request or comment.
- A bot's private key or token leaving the bots' files (`$XDG_CONFIG_HOME/crew/bots`, else `~/.config/crew/bots`) or the private gh config directories crew writes for its bots.
- A comment counting as an answer to a question when crew's rule says it must not: its author is neither a code owner nor an App that may answer (the Apps on `answering_apps`, or crew's bots without that key), a code owner's login posts it as an App or a listed App's login posts it as a user, or it holds `<!-- crew:`.
- `crew upgrade` installing an archive that does not match the release's `checksums.txt`, or writing anywhere other than the path of the crew it replaces.
- A release archive that does not match the release's `checksums.txt`, or a release that crew's Release workflow did not build.

These are not:

- What a repository's own `.crew/config.yaml` tells crew to run. Shell actions, functions and agent sessions run with your rights by design, and the config is code you trust.
- Vulnerabilities in Claude Code, Codex, `gh` or `git` themselves. Report those to their own projects.
