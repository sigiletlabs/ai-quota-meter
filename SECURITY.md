# Security policy

## Reporting a vulnerability

Report it privately, through GitHub:

**[Open a security advisory](https://github.com/sigiletlabs/ai-quota-meter/security/advisories/new)**

Only the repository maintainers can see it. Please do not open a public issue
for a vulnerability.

Include what you did, what happened, and the version (`ai-quota-meter --version`).
A proof of concept helps but is not required to file.

This is a small project maintained by one person. Expect a first reply within a
week. If a week passes with nothing, a plain issue saying "I filed an advisory"
— with no detail in it — is a reasonable nudge.

## Supported versions

The latest release. There are no backported fixes; a fix ships as a new release.

## What is in scope

This program reads local files, writes local files, and prints a line. It has
no network access of its own, with one exception: the optional alerting
feature, which posts to an ntfy topic you configure yourself.

Worth reporting:

- **Anything that leaks a credential or an account identifier.** The capture
  files are keyed by account, and the design rule is that readings from
  different accounts never pool. A path that writes one account's figures under
  another's key is a bug worth reporting here rather than as a normal issue.
- **Anything that writes outside its own directories.** It edits Claude Code's
  `settings.json` and Codex's `config.toml`, and backs both up first. A path
  that writes somewhere it should not, or that corrupts one of those files, is
  in scope.
- **Anything in the release pipeline.** Release binaries are built by GitHub
  Actions from a tag and carry a build provenance attestation. A way to get a
  binary attested that did not come from this repository is in scope.
- **Command injection or unsafe handling of the JSON payload** the tool
  receives on stdin.

## What is not in scope

- **The quota numbers being wrong.** This program reports the vendor's figures
  and calculates nothing on the bar. If Anthropic or OpenAI send a wrong
  number, a wrong number is displayed. That is a normal issue, if it is
  anything.
- **The blank bar.** Claude Code shows nothing at all for a status line command
  it cannot run. That is Claude Code's behaviour, and
  `ai-quota-meter --doctor` exists to diagnose it.
- **Your ntfy topic being a bearer credential.** Anyone holding the topic name
  can publish to it. That is how ntfy works, and
  [docs/alerting.md](docs/alerting.md) says so and tells you to use 32 random
  characters. The program never prints the topic, on success or on failure — if
  you find a path where it does, that one *is* in scope.
- Findings from an automated scanner with no working path to exploitation.

## Verifying a release

Every released binary carries a signed record of which commit and workflow
produced it:

```sh
gh attestation verify ai-quota-meter-linux-amd64 --repo sigiletlabs/ai-quota-meter
```

Exit 0 and no output means genuine. Exit 1 means the file is not what this
repository built.

The binaries are not code-signed yet, so Windows will want an `Unblock-File`
and macOS a right-click Open the first time. That is not a security failure,
just an unsigned binary.
