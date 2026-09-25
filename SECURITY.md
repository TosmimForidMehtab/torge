# Security Policy

## Supported versions

Torge is pre-1.0. Security fixes are released for the latest minor version
of the core module and of each contrib module.

| Module | Supported |
|---|---|
| `github.com/TosmimForidMehtab/torge` latest `v0.x` release | Yes |
| `github.com/TosmimForidMehtab/torge/contrib/*` latest `v0.x` release | Yes |
| Older releases | No — please upgrade |

## Reporting a vulnerability

**Please do not report security vulnerabilities through public issues,
discussions or pull requests.**

Report them privately through GitHub:

1. Open the repository's [Security tab](https://github.com/TosmimForidMehtab/torge/security).
2. Choose **Report a vulnerability**
   ([direct link](https://github.com/TosmimForidMehtab/torge/security/advisories/new)).

Please include as much of the following as you can:

- the affected module, package and version (or commit);
- the type of issue (for example request smuggling, authentication bypass,
  information disclosure, denial of service);
- steps to reproduce, ideally a minimal program or test;
- the impact you believe an attacker could achieve;
- any suggested fix.

## What to expect

- An acknowledgment within **7 days**.
- An initial assessment, including whether the report is accepted, within
  **14 days**.
- Updates as the fix progresses. Fixes are released as a new patch version
  and published as a GitHub security advisory, which also feeds the Go
  vulnerability database (`govulncheck`).
- Credit in the advisory, unless you prefer to remain anonymous.

Please give us a reasonable time to release a fix before disclosing the
issue publicly. We aim to coordinate disclosure with you.

## Scope

In scope: code in this repository — the core module, the contrib modules and
the `torge` CLI.

Out of scope:

- vulnerabilities in third-party dependencies (report those upstream; we
  will update our requirement once a fix is released);
- applications built with Torge, unless the vulnerability is caused by
  Torge itself;
- the example applications under `examples/`, which are not intended for
  production use as-is.

## Security practices for users

- Keep Torge and its dependencies up to date, and run
  [`govulncheck`](https://go.dev/doc/security/vuln/) in your CI.
- Keep the production defaults (`TORGE_ENV=production`): internal error
  details are never sent to clients, and unsafe settings stop startup.
- Configure `WithTrustedProxies` to match your load balancer, so client IPs
  and forwarded headers cannot be spoofed.
- Store secrets in `config.Secret` fields and provide them through your
  platform's secret manager.
