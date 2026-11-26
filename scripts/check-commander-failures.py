#!/usr/bin/env python3
"""Report commander rounds that failed before reaching the model.

Why the ledger and not the journal: failCommanderRun (internal/api/commander.go:182)
covers three paths, and one of them is "cannot open the commander log"
(commander.go:348). A journal event cannot record the failure of the run whose
journal could not be opened; only the ledger row survives it. So this check
reads ledger.sqlite objects(kind='run') and needs nothing else running.

Trip conditions (two rules, newest run first):

1. Anchor/layout failures trip on the FIRST round. `prompt_anchor_missing` is
   deterministic: the prompt builder either emits the insertion anchor
   ("## board") or it does not, and no later round repairs it. Waiting for a
   threshold means spending that many rounds on a layout that cannot recover —
   measured 2026-11-22 on a real instance: the decider stopped after two failed
   rounds by itself, so a project that is paused (or runs without a heartbeat)
   would never reach a count of three.
2. Any other failure trips after --threshold consecutive rounds of the SAME
   failure class (the text before the first "：", e.g. "指挥官轮布局" or
   "打开指挥官日志失败"). Plain failure counting is not used: the project
   already carries 17 failed runs out of 497, and those are scattered across
   causes, so counting every failure would alarm during healthy operation.
   Grouping by class keeps rule 2 specific without needing a whitelist.

Matching the token alone is not enough for rule 1: the three layout failures
share the prefix "指挥官轮布局：", so matching that prefix would conflate a
missing anchor with every other layout error, which is why the hint stays the
machine-readable token.

Exit codes: 0 clean, 1 tripped, 2 cannot read the ledger (UNKNOWN is not pass),
3 self-test failed.
"""

from __future__ import annotations

import argparse
import json
import os
import sqlite3
import sys
import tempfile

DEFAULT_ANCHOR_HINTS = ("prompt_anchor_missing",)

SCHEMA = (
    "CREATE TABLE objects (kind TEXT NOT NULL, id TEXT NOT NULL, position INTEGER NOT NULL,"
    " version INTEGER NOT NULL, seq INTEGER NOT NULL, payload BLOB NOT NULL,"
    " PRIMARY KEY(kind,id))"
)


def load_runs(ledger: str) -> list[dict]:
    if not os.path.isfile(ledger):
        raise SystemExit(_fail(f"ledger not found: {ledger}"))
    con = sqlite3.connect(f"file:{ledger}?mode=ro", uri=True)
    try:
        rows = con.execute("SELECT id, payload FROM objects WHERE kind='run'").fetchall()
    except sqlite3.Error as exc:
        raise SystemExit(_fail(f"cannot read objects(kind='run') from {ledger}: {exc}"))
    finally:
        con.close()
    runs = []
    for rid, blob in rows:
        try:
            run = json.loads(blob)
        except (TypeError, ValueError):
            continue
        if not isinstance(run, dict):
            continue
        if run.get("purpose") != "commander" and not run.get("commander_round"):
            continue
        run.setdefault("id", rid)
        runs.append(run)
    return runs


def _order_key(run: dict) -> tuple:
    round_no = run.get("commander_round") or 0
    return (int(round_no), str(run.get("started_at") or ""))


def _reason(run: dict) -> str:
    return str(run.get("exit_reason") or run.get("stop_reason") or "")


def _failure_class(reason: str) -> str:
    head = reason.split("：", 1)[0].strip()
    return head or reason.strip()


def _is_anchor(reason: str, hints: tuple[str, ...]) -> bool:
    lowered = reason.lower()
    return any(h.lower() in lowered for h in hints)


def scan(runs: list[dict], threshold: int, hints: tuple[str, ...], window: int) -> dict:
    ordered = sorted(runs, key=_order_key)[-window:]
    # The newest run first: a `done` round ends the streak, so a healthy round
    # after a failure clears it.
    trailing = []
    for run in reversed(ordered):
        status = str(run.get("status") or "")
        if status == "done":
            break
        reason = _reason(run)
        trailing.append({"id": run.get("id"), "round": run.get("commander_round"),
                         "status": status, "exit_reason": reason,
                         "started_at": run.get("started_at"),
                         "failure_class": _failure_class(reason)})
    tripped_by = None
    repeated = 0
    if trailing:
        newest = trailing[0]
        if _is_anchor(newest["exit_reason"], hints):
            tripped_by = "anchor"
        else:
            same = newest["failure_class"]
            for item in trailing:
                if item["failure_class"] != same:
                    break
                repeated += 1
            if repeated >= threshold:
                tripped_by = "repeated"
    return {
        "commander_runs": len(ordered),
        "threshold": threshold,
        "trailing": trailing,
        "tripped": tripped_by is not None,
        "tripped_by": tripped_by,
        "repeated_count": repeated,
        "last_status": (ordered[-1].get("status") if ordered else None),
        "last_reason": _reason(ordered[-1]) if ordered else None,
    }


def _fail(msg: str) -> int:
    print(f"UNKNOWN: {msg}", file=sys.stderr)
    return 2


def report(result: dict) -> int:
    if result["tripped"]:
        if result["tripped_by"] == "anchor":
            print("TRIPPED: the newest commander round failed on a deterministic anchor error "
                  "(trip on the first round, threshold does not apply)")
        else:
            print(f"TRIPPED: {result['repeated_count']} consecutive commander rounds failed the same way "
                  f"(class={result['trailing'][0]['failure_class']}, threshold {result['threshold']})")
        for item in reversed(result["trailing"]):
            print(f"  round {item['round']} {item['id']} status={item['status']} exit_reason={item['exit_reason']}")
        return 1
    trailing = len(result["trailing"])
    print(f"ok: {result['commander_runs']} commander runs scanned, "
          f"{trailing} trailing failure(s), none past the trip rules "
          f"(anchor: first round; repeated class: {result['threshold']})")
    print(f"ok: last commander run status={result['last_status']} reason={result['last_reason']}")
    return 0


def self_test(threshold: int, hints: tuple[str, ...]) -> int:
    """Negative samples first: each case must produce the stated verdict."""
    cases = [
        ("one anchor failure -> trip immediately", [
            ("failed", "指挥官轮布局：prompt_anchor_missing: ## board"),
        ], True, "anchor"),
        ("another anchor hint mid-schedule -> trip immediately", [
            ("failed", "指挥官轮布局：prompt_anchor_missing: ## 授权边界（当前口径）"),
        ], True, "anchor"),
        ("newest of two anchor failures -> trip immediately", [
            ("failed", "指挥官轮布局：prompt_anchor_missing: ## board"),
            ("failed", "指挥官轮布局：prompt_anchor_missing: ## board"),
        ], True, "anchor"),
        ("three same-class layout failures without the marker -> trip at threshold", [
            ("failed", "指挥官轮布局：详情段为空"),
            ("failed", "指挥官轮布局：本轮指派段为空"),
            ("failed", "指挥官轮布局：可续读入口段为空"),
        ], True, "repeated"),
        ("three same-class plain failures -> trip at threshold", [
            ("failed", "打开指挥官日志失败：permission denied"),
            ("failed", "打开指挥官日志失败：permission denied"),
            ("failed", "打开指挥官日志失败：permission denied"),
        ], True, "repeated"),
        ("three failures of different classes in a row -> no trip", [
            ("failed", "指挥官笔记身份：token 已失效"),
            ("failed", "打开指挥官日志失败：permission denied"),
            ("failed", "准备指挥官日志目录失败：disk full"),
        ], False, None),
        ("two same-class failures -> no trip", [
            ("failed", "打开指挥官日志失败：permission denied"),
            ("failed", "打开指挥官日志失败：permission denied"),
        ], False, None),
        ("one non-anchor failure -> no trip", [
            ("failed", "dial tcp 127.0.0.1:18799: connectex: 由于目标计算机积极拒绝，无法连接"),
        ], False, None),
        ("anchor failures broken by a done round -> newest two still trip", [
            ("failed", "指挥官轮布局：prompt_anchor_missing: ## board"),
            ("done", "completed"),
            ("failed", "指挥官轮布局：prompt_anchor_missing: ## board"),
            ("failed", "指挥官轮布局：prompt_anchor_missing: ## board"),
        ], True, "anchor"),
        ("marker text on a done round -> no trip", [
            ("done", "completed，prompt_anchor_missing: ## board"),
        ], False, None),
        ("anchor failure followed by an unrelated newest failure -> repeated rule only", [
            ("failed", "指挥官轮布局：prompt_anchor_missing: ## board"),
            ("failed", "打开指挥官日志失败：permission denied"),
        ], False, None),
    ]
    failures = []
    for label, rows, want, want_by in cases:
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "ledger.sqlite")
            con = sqlite3.connect(path)
            con.execute(SCHEMA)
            for i, (status, reason) in enumerate(rows, start=1):
                payload = {"id": f"r_case_{i}", "purpose": "commander", "commander_round": i,
                           "status": status, "exit_reason": reason,
                           "started_at": f"2026-11-22T00:0{i}:00+08:00"}
                con.execute("INSERT INTO objects(kind,id,position,version,seq,payload) VALUES(?,?,?,?,?,?)",
                            ("run", payload["id"], i, 1, i, json.dumps(payload).encode()))
            con.commit()
            con.close()
            result = scan(load_runs(path), threshold, hints, window=50)
        got, got_by = result["tripped"], result["tripped_by"]
        verdict = "ok" if (got, got_by) == (want, want_by) else "WRONG"
        print(f"  self-test [{verdict}] {label}: tripped={got}/{got_by} want={want}/{want_by}")
        if (got, got_by) != (want, want_by):
            failures.append(label)
    if failures:
        print(f"SELF-TEST FAILED: {len(failures)}/{len(cases)} cases wrong", file=sys.stderr)
        return 3
    print(f"  self-test: {len(cases)}/{len(cases)} cases as expected")
    return 0


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    # No baked-in ledger path: a default pointing at a real project would ship a
    # project id and silently scan the wrong ledger.
    ap.add_argument("--ledger", default=os.environ.get("SAKER_LEDGER_PATH"),
                    help="ledger.sqlite to scan (or set SAKER_LEDGER_PATH); required unless --self-test")
    ap.add_argument("--threshold", type=int, default=3,
                    help="consecutive same-class failures that trip the non-anchor rule "
                         "(default 3); an anchor failure trips on its own")
    ap.add_argument("--window", type=int, default=50)
    ap.add_argument("--anchor-hint", action="append", dest="hints",
                    help=f"substring that marks an anchor failure (default: {', '.join(DEFAULT_ANCHOR_HINTS)})")
    ap.add_argument("--json", action="store_true", help="print the scan result as JSON")
    ap.add_argument("--self-test", action="store_true", help="run the negative-sample cases and exit")
    args = ap.parse_args(argv)

    hints = tuple(args.hints) if args.hints else DEFAULT_ANCHOR_HINTS
    if args.self_test:
        return self_test(args.threshold, hints)
    if not args.ledger:
        print("缺少 --ledger（或 SAKER_LEDGER_PATH）：脚本不内置账本路径", file=sys.stderr)
        return 2
    result = scan(load_runs(args.ledger), args.threshold, hints, args.window)
    if args.json:
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 1 if result["tripped"] else 0
    return report(result)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
