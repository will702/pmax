#!/usr/bin/env python3
"""pmax laya wrapper — fast typed decisions via the laya engine
(https://github.com/NandhaKishorM/laya, Apache-2.0).

Runs INSIDE the pmax uv venv (installed by `pmax ai setup`). Loads the Router
once, then answers a batch of JSONL items from stdin:

  classify mode:  {"id": ..., "text": ...}  ->  {"id", "label", "confidence", "abstained"}
  noul mode:      {"id": ..., "text": ...}  ->  {"id", "p": P(true), "confidence", "abstained"}
  score mode:     {"id": ..., "text": ...}  ->  {"id", "score", "confidence", "abstained"}

Usage:
  laya.py classify --instructions "..." --labels '{"Docs":"...", ...}' [--min-confidence 0.9]
  laya.py noul     --instructions "..." [--min-confidence 0.9]

Everything is defensive: if laya's API drifts, we exit non-zero with a clear
message on stderr and pmax degrades gracefully (no decisions are faked).
"""
import argparse
import json
import sys

MIN_CONF = 0.90


def build_questions(mode, instructions, labels):
    if mode == "classify":
        if not labels:
            raise SystemExit("classify mode requires --labels JSON")
        return {"main": {"type": "choice", "instructions": instructions, "criteria": labels}}
    if mode == "noul":
        return {"main": {"type": "noul", "instructions": instructions}}
    return {"main": {"type": "score", "instructions": instructions,
                     "criteria": labels or ["low", "medium", "high"]}}


def main():
    global MIN_CONF
    ap = argparse.ArgumentParser()
    ap.add_argument("mode", choices=["classify", "noul", "score"])
    ap.add_argument("--instructions", required=True)
    ap.add_argument("--labels", default="", help="JSON: classify criteria map / score levels list")
    ap.add_argument("--min-confidence", type=float, default=0.90)
    args = ap.parse_args()
    MIN_CONF = args.min_confidence

    labels = json.loads(args.labels) if args.labels else None
    questions = build_questions(args.mode, args.instructions, labels)

    try:
        from laya import Router
    except ImportError as e:
        print(f"laya-not-importable: {e}", file=sys.stderr)
        sys.exit(3)

    router = Router()  # loads checkpoints once

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            item = json.loads(line)
        except json.JSONDecodeError:
            continue
        state = item.get("text", "")
        out = {"id": item.get("id")}
        try:
            res = router.predict(state, questions, min_confidence=MIN_CONF)
            ans = res["answers"]["main"]
            out["confidence"] = float(ans.get("confidence", 0.0))
            abst = ans.get("abstention") == "abstained" or bool(ans.get("low_confidence"))
            out["abstained"] = bool(abst)
            if args.mode == "classify":
                out["label"] = ans.get("choice")
            elif args.mode == "noul":
                out["p"] = float(ans.get("noul", 0.0))
            else:
                out["score"] = float(ans.get("score", 0.0))
        except Exception as e:  # noqa: BLE001 — one bad item must not kill the batch
            out["abstained"] = True
            out["confidence"] = 0.0
            if args.mode == "classify":
                out["label"] = None
            elif args.mode == "noul":
                out["p"] = 0.0
            else:
                out["score"] = 0.0
            print(f"laya-item-error: {e}", file=sys.stderr)
        print(json.dumps(out, ensure_ascii=False))
        sys.stdout.flush()


if __name__ == "__main__":
    main()
