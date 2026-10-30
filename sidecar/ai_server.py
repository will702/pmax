#!/usr/bin/env python3
"""pmax MLX sidecar — resident, Apple-Silicon-native inference.

Loads the embedding model (bge-small) and chat model (Qwen2.5-3B) ONCE into
unified memory, then serves warm requests on localhost:

    POST /health           -> {"status":"ok","embed":bool,"chat":bool,...}
    POST /embed  {"texts": [...]}                        -> {"embeddings":[[...]],"dim":N}
    POST /chat   {"messages":[{"role","content"}], "max_tokens":512} -> {"text":"..."}

All inference runs under MLX (Metal GPU). Stdlib HTTP; the only deps are the
MLX packages installed by `pmax ai setup`.
"""
import argparse
import json
import os
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

STARTED = time.time()
INFER_LOCK = threading.Lock()  # MLX models are not thread-safe

CHAT = None        # (model, tokenizer) | False
EMBED = None       # (model, tokenizer, Embedding, mx) | False
EMBED_ERRORS = []

EMBED_MODEL = os.environ.get("PMAX_EMBED_MODEL", "mlx-community/bge-small-en-v1.5")
CHAT_MODEL = os.environ.get("PMAX_CHAT_MODEL", "mlx-community/Qwen2.5-3B-Instruct-4bit")


def log(*a):
    print(f"[ai_server {time.strftime('%H:%M:%S')}]", *a, file=sys.stderr, flush=True)


def load_chat():
    global CHAT
    if CHAT is not None:
        return CHAT
    try:
        from mlx_lm import load
        model, tokenizer = load(CHAT_MODEL)
        CHAT = (model, tokenizer)
        log("chat model ready:", CHAT_MODEL)
    except Exception as e:  # noqa: BLE001
        log("chat model FAILED:", e)
        CHAT = False
    return CHAT


def load_embed():
    global EMBED
    if EMBED is not None:
        return EMBED
    try:
        import mlx.core as mx
        from mlx_embeddings import Embedding, Models

        model, tokenizer = Models.load(EMBED_MODEL)
        Embedding.embed(model, tokenizer, ["warmup"])  # smoke + compile
        EMBED = (model, tokenizer, Embedding, mx)
        log("embed model ready:", EMBED_MODEL)
    except Exception as e:  # noqa: BLE001
        log("embed model FAILED:", e)
        EMBED_ERRORS.append(str(e))
        EMBED = False
    return EMBED


def embed_texts(texts):
    e = load_embed()
    if not e:
        raise RuntimeError("embedding model unavailable: " + "; ".join(EMBED_ERRORS))
    model, tokenizer, Embedding, mx = e
    out = Embedding.embed(model, tokenizer, list(texts))
    try:
        vecs = out.tolist()
    except AttributeError:
        vecs = mx.array(out).tolist()
    return vecs


def chat_reply(messages, max_tokens=512, temperature=0.2):
    c = load_chat()
    if not c:
        raise RuntimeError("chat model unavailable")
    model, tokenizer = c
    from mlx_lm import generate

    prompt = tokenizer.apply_chat_template(messages, tokenize=False, add_generation_prompt=True)
    return generate(model, tokenizer, prompt=prompt,
                    max_tokens=int(max_tokens), temperature=float(temperature), verbose=False)


class Handler(BaseHTTPRequestHandler):
    def _json(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):  # quiet access log
        pass

    def do_GET(self):
        if self.path == "/health":
            self._json(200, {
                "status": "ok",
                "uptime_s": int(time.time() - STARTED),
                "embed": load_embed() is not False,
                "chat": load_chat() is not False,
                "embed_model": EMBED_MODEL if load_embed() else None,
                "chat_model": CHAT_MODEL if load_chat() else None,
                "embed_errors": EMBED_ERRORS,
            })
        else:
            self._json(404, {"error": "not found"})

    def do_POST(self):
        try:
            length = int(self.headers.get("Content-Length") or 0)
            req = json.loads(self.rfile.read(length) or b"{}")
        except Exception as e:  # noqa: BLE001
            self._json(400, {"error": f"bad json: {e}"})
            return
        try:
            with INFER_LOCK:
                if self.path == "/embed":
                    texts = req.get("texts") or []
                    if not texts:
                        raise RuntimeError("texts[] required")
                    vecs = embed_texts(texts)
                    self._json(200, {"embeddings": vecs, "dim": len(vecs[0])})
                elif self.path == "/chat":
                    messages = req.get("messages") or []
                    if not messages:
                        raise RuntimeError("messages[] required")
                    text = chat_reply(messages,
                                      max_tokens=req.get("max_tokens", 512),
                                      temperature=req.get("temperature", 0.2))
                    self._json(200, {"text": text})
                else:
                    self._json(404, {"error": "not found"})
        except Exception as e:  # noqa: BLE001
            log("request error:", e)
            self._json(503, {"error": str(e)})


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=int(os.environ.get("PMAX_AI_PORT", 8765)))
    ap.add_argument("--preload", action="store_true")
    args = ap.parse_args()

    if args.preload:
        load_embed()
        load_chat()

    srv = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    log(f"listening on 127.0.0.1:{args.port} (pid {os.getpid()})")
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
