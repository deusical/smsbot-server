# CI performance

The required `service` check continues to require build, backend and both browser
shards. Race tests run with `-count=1`; vet, formatting, frontend unit tests,
production builds and both vulnerability scans remain enabled. Browser shards
have separate synthetic fixture databases and one worker each.

## Baseline (2026-10-22)

GitHub-hosted run 39873327485 finished in 4m30s: build 39s, backend 111s,
browser jobs 220s and 154s. Their browser installation steps took 15s each;
test execution took 195s and 129s. PR runs 39871148310, 39863247225 and
39857199580 took roughly 12-14 minutes. Browser installation alone took 470s,
510s and 570s on their slower runner. Run 39871148310 downloaded 24.9 MB of
Ubuntu dependencies in 7m31s; the actual browser download took about 3s.

## Predictable browser setup

Browser jobs use the official Playwright v1.64.0 Noble image, pinned by its
registry manifest digest. Browsers and system dependencies are already present;
normal jobs must not run `playwright install --with-deps` or apt installation.
The package still installs through `npm ci`. A compatibility step checks its
locked version and executes the downloaded service binary before the full suite.
Build/backend runners use Ubuntu 24.04 to align with the image's distro generation.

When upgrading Playwright, update package.json, package-lock.json, the container
version/digest and the workflow version assertion together. Verify the digest
against the official Microsoft registry and run both shards. The image has its
own pull cost; measure the entire job, including container initialization, rather
than reporting only the removed installation step.

Install/test/audit steps have separate names and bounded timeouts. A timeout must
fail CI and the final gate. Preserve failed browser diagnostics. Do not bypass a
failed image pull with a different browser or cached test result.

## Browser timer tests

Next, replace wall-clock waiting in mocked frontend polling tests with browser
clock advancement. Preserve before-deadline silence, eventual retry, settled
silence, focus/visibility and read-receipt assertions. Browser clocks do not
advance the Go server; tests that exercise server timing retain real time.
Do not shorten production intervals or add retries to conceal flakes. Rebalance
independent files only after measuring the new shard distribution. Increasing
workers requires separate fixture state and ports first.

## Validation and comparison

For each slice, validate the workflow with actionlint and run the affected tests.
For the container change, execute the full browser suite using the pinned image,
then require all hosted checks on the exact PR HEAD. Hosted checks validate the
actual Linux build artifact against the container, including file/export flows.
Compare several completed hosted runs (including fresh image pulls), recording
queue time, image setup, dependency installation, test time and total elapsed time.
Initial acceptance is removal of apt-related outliers with identical test coverage;
any improvement to the normal 4m30s run must be measured, not assumed.

Run [39881904666](run 39881904666)
passed all five checks in both attempts on the same implementation commit:

| Measurement | Attempt 1 | Attempt 2 |
| --- | ---: | ---: |
| Total elapsed (including runner scheduling) | 5m38s | 5m01s |
| Build job | 78s | 39s |
| Backend job | 228s | 120s |
| Browser 1 container initialization | 45s | 35s |
| Browser 2 container initialization | 50s | 31s |
| Browser 1 test execution | 192s | 198s |
| Browser 2 test execution | 127s | 138s |

The first backend run had a Go cache miss. Both attempts used fresh hosted
browser jobs, verified the downloaded Linux binary, and ran every existing test
without an apt/browser install step. Container initialization costs more than
the baseline's fast 15-second installation: the repeat total is 31 seconds slower
than the 4m30s baseline. This is a setup-predictability tradeoff, not a measured
fast-path speedup. Two attempts cannot establish tail latency or guarantee that
image pulls will never stall. Timer-test optimization remains the next slice.

Local container validation passed all 89 active browser tests (3 documentation
screenshot cases intentionally skipped) in 5.4 minutes using a Linux volume.
An earlier Windows bind-mounted run passed the pagination assertions but timed
out during browser-context teardown; the Linux-volume run passed that case in
6.7 seconds without test changes or retries. Use Linux filesystem storage when
reproducing the hosted container environment. The local fixture was cross-compiled
with CGO disabled; hosted checks must still validate the actual Linux-built binary.

References: [Playwright Docker](https://playwright.dev/docs/docker),
[CI](https://playwright.dev/docs/ci), [Clock](https://playwright.dev/docs/clock).
