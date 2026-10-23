# CI performance

Merge only after the build, backend and browser jobs all pass. There is no
separate gate job: it billed a minute per run, and this private repository has no
branch protection that could require it. Race tests run with `-count=1`; vet, formatting, frontend unit tests,
production builds and both vulnerability scans remain enabled. The browser job
uses its own synthetic fixture database and one worker.

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
against the official Microsoft registry and run the full suite. The image has its
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
Do not shorten production intervals or add retries to conceal flakes. Increasing
workers requires separate fixture state and ports first.

## Actions minutes

Hosted runners bill every job in whole minutes, and each browser job spends about
60-75 s on container start-up and setup before any test runs. Three balanced shards
(PR 38) cut wall time to about 3m15 but billed roughly 14 minutes per run; they
duplicated that overhead three times and each shard rounded up to 3 minutes.
The browser suite is now one job (about 3.5 minutes of tests), which should bill
about 5 minutes, at the price of roughly 5.5 minutes of wall time. These figures
are estimates until hosted runs confirm them.

The Go race-test cache is a rolling `actions/cache`: every run restores the newest
entry (pull requests read main's), and only pushes to main save a new one. This
avoids the cold 265-270 s backend runs that happened whenever `ci.yml` or `go.sum`
changed under the earlier fixed-key cache, and keeps the cache from going stale.

If wall time matters more than minutes again, shard the browser job. PR 38
(`b1289c5`) has a working version that selects spec files by measured duration.

## Measured results (2026-10-23)

Wall time is build plus the slowest dependent job; hosted runners vary.

| Run | Build | Backend | Browser jobs | Wall |
|---|---|---|---|---|
| 39873327485, baseline (2 shards) | 39 s | 111 s | 220 s, 154 s | 4m30 |
| fc311ee, pinned image + controlled clock | 48 s | 103 s | 149 s, 178 s | ~3m54 |
| PR 38, three duration-based shards | 51 s | 128 s | 127 s, 125 s, 140 s | ~3m15 (about 14 billed minutes) |
| 39904521461, main after PR 38 | 42 s | 265 s | 128 s, 129 s, 120 s | 5m20 |

The controlled-clock tests removed most of the phone-sync spec's waiting, and the
pinned image removed the rare 470-570 s browser-install stalls at the cost of
about 35-45 s of container initialization per browser job. With balanced shards the
backend job became the critical path, and its duration varied between 93 and 270 s
depending only on whether the Go cache was warm: race tests took 65-89 s warm and
118-171 s cold, and `go vet` 0 s warm and 37-59 s cold.

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
