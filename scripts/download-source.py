"""Portable anonymous adapter for the installed Rednote engine."""
import argparse
import contextlib
import json
from pathlib import Path
import sys
import time
from unittest import mock


def fetch(root, target_url, output, receipt, timeout=900):
    sys.path.insert(0, str(root / "research/rednote_downloader"))
    from app.downloader import DownloaderConfig, MediaDownloader
    from app.platforms import identify_url
    from app.models import Platform
    from playwright.sync_api import BrowserType
    from scripts.douyin_item_live_smoke import anonymous_browser_adapter

    target = identify_url(target_url)
    if target.kind.value == "short_link":
        import urllib.request
        with urllib.request.urlopen(urllib.request.Request(target.url, headers={"User-Agent": "Mozilla/5.0"}), timeout=15) as response:
            target = identify_url(response.geturl())
    if target.platform not in (Platform.DOUYIN, Platform.XIAOHONGSHU):
        raise ValueError("unsupported_platform")
    deadline = time.monotonic() + timeout
    douyin = target.platform == Platform.DOUYIN
    engine = MediaDownloader(DownloaderConfig(cookie_browser="chrome" if douyin else None, allow_cookie_fallback=False))
    original_launch = BrowserType.launch

    def launch(browser_type, *args, **kwargs):
        # Linux uses Playwright's isolated bundled Chromium, not a personal
        # Chrome installation/profile. No upstream source modifications.
        if sys.platform.startswith("linux") and kwargs.get("channel") == "chrome":
            kwargs["channel"] = "chromium"
        return original_launch(browser_type, *args, **kwargs)

    with contextlib.ExitStack() as stack:
        if douyin:
            stack.enter_context(anonymous_browser_adapter([]))
        stack.enter_context(mock.patch.object(BrowserType, "launch", launch))
        found = engine.discover(target.url, target.platform, target.kind, should_cancel=lambda: time.monotonic() >= deadline)
        if len(found.items) != 1:
            raise ValueError("single_video_required")
        result = engine.download_item(found.items[0], target.platform, output, should_cancel=lambda: time.monotonic() >= deadline)
        receipt.write_text(json.dumps({"files": [str(Path(p).resolve()) for p in result.output_paths]}), encoding="utf-8")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--timeout", type=int, default=900)
    parser.add_argument("url")
    args = parser.parse_args()
    fetch(args.root, args.url, args.output, args.receipt, args.timeout)
