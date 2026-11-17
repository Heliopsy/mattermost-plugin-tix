#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""End-to-end tests: the plugin bundle in a real Mattermost against a real tix, both in podman.

Usage: python3 e2e/run.py [--only SUBSTRING] dist/cc.heliopsy.tix-<version>.tar.gz

Environment: E2E_KEEP=1 keeps the images pulled for the run; E2E_TIX_IMAGE and E2E_MM_IMAGE pick
the images; E2E_ENGINE=docker uses docker instead of podman (CI only).
"""

import argparse
import os
import signal
import sys
import time
import traceback

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import harness  # noqa: E402
import scenarios  # noqa: E402


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("bundle")
    ap.add_argument("--only", action="append", default=[], help="run only scenarios whose name contains this (setup always runs)")
    args = ap.parse_args()

    def on_signal(signum, _frame):
        raise KeyboardInterrupt(f"signal {signum}")
    signal.signal(signal.SIGTERM, on_signal)
    signal.signal(signal.SIGHUP, on_signal)

    env = harness.Env(os.path.abspath(args.bundle), keep_images=os.environ.get("E2E_KEEP") == "1")
    results = []
    xfails = []
    try:
        env.up()
        suite = scenarios.Suite(env)
        suite.setup()
        for name, fn in scenarios.SCENARIOS:
            if args.only and not any(o.lower() in name.lower() for o in args.only):
                continue
            start = time.time()
            xfail = getattr(fn, "xfail", "")
            try:
                fn(suite)
                results.append((name, True, ""))
                print(f"{'XPASS' if xfail else 'PASS '} {name} ({time.time() - start:.1f}s)", flush=True)
            except Exception as e:  # noqa: BLE001 - every failure is reported, then the next scenario runs
                detail = str(e) if isinstance(e, scenarios.Fail) else traceback.format_exc(limit=3)
                if xfail:
                    xfails.append((name, xfail))
                    print(f"XFAIL {name}: {xfail}", flush=True)
                    continue
                results.append((name, False, detail))
                print(f"FAIL  {name}: {detail}", flush=True)
    except KeyboardInterrupt as e:
        print(f"[e2e] interrupted ({e})", flush=True)
        results.append(("run", False, "interrupted"))
    except Exception:  # noqa: BLE001
        traceback.print_exc()
        env.dump_logs()
        results.append(("environment", False, "setup failed"))
    finally:
        failed = [r for r in results if not r[1]]
        if failed:
            env.dump_logs()
        print("[e2e] tearing down", flush=True)
        env.down()

    passed = sum(1 for r in results if r[1])
    print(f"\n{passed} passed, {len(failed)} failed, {len(xfails)} known gaps")
    for name, why in xfails:
        print(f"  XFAIL {name}: {why}")
    for name, _, detail in failed:
        print(f"  FAIL {name}: {detail.splitlines()[0] if detail else ''}")
    return 1 if failed or not results else 0


if __name__ == "__main__":
    sys.exit(main())
