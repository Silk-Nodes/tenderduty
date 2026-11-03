# Security exceptions

Advisories reported by `govulncheck ./...` that are accepted rather than fixed, with the reason and a date to
look again. Anything not listed here blocks a release.

`make vulncheck` runs the scan and fails on anything reachable that is not listed below. The same script runs in
CI on every push and pull request and once a week, so a new advisory shows up as a failed run.

| Advisory | Module | Reason it is accepted | Review by |
|---|---|---|---|
| GO-2026-5932 | golang.org/x/crypto/openpgp | The package is flagged as unmaintained. tenderduty never handles PGP data. The only path to it is package initialisation: `cosmossdk.io/x/upgrade/types` pulls in the cosmos-sdk keyring, which imports `openpgp/armor`. No tenderduty code calls into it. The commit that removes the package from x/crypto (v0.58.1-0.20261009181718) moves it to a separate deprecated module that the SDK then requires anyway, so the bump changes nothing. Clears on its own when cosmos-sdk drops the import. | 2027-05-01 |
