#!/usr/bin/env python3
"""Serve the Stow launch site, with content negotiation for agent clients.

Why this exists rather than `python3 -m http.server`: the site is read by two
very different kinds of client. A browser wants `text/html`. A coding agent
fetching the root URL with `Accept: text/markdown` wants the same information
as prose it can read directly, not a page of markup to strip. A plain static
server forces one of them to be second-class.

So: when a request for `/` prefers markdown over HTML, this serves
`agent.md` as `text/markdown`. Everything else is ordinary static serving with
correct content types.

    python3 site/serve.py --host 0.0.0.0 --port 8080
"""

from __future__ import annotations

import argparse
import mimetypes
import os
import posixpath
import sys
from http import HTTPStatus
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

# The document served to a markdown-preferring client at the root.
MARKDOWN_ROOT = "agent.md"

# The extension to serve for a clean URL when the client wants markdown.
MARKDOWN_SUFFIXES = (".md",)

EXTRA_TYPES = {
    ".md": "text/markdown; charset=utf-8",
    ".txt": "text/plain; charset=utf-8",
    ".svg": "image/svg+xml",
    ".webmanifest": "application/manifest+json",
}

# Long enough to make a redeploy feel instant, short enough that a corrected
# typo is not cached for a day.
CACHE_STATIC = "public, max-age=3600"
CACHE_HTML = "no-cache"


def _quality(accept: str, subtype: str) -> float:
    """Return the q-value the Accept header gives a media subtype."""
    best = -1.0
    for part in accept.split(","):
        bits = part.strip().split(";")
        token = bits[0].strip().lower()
        if token not in (subtype, f"{subtype}/*", "*/*"):
            continue
        q = 1.0
        for param in bits[1:]:
            name, _, value = param.partition("=")
            if name.strip().lower() == "q":
                try:
                    q = float(value)
                except ValueError:
                    q = 0.0
        best = max(best, q)
    return best


def prefers_markdown(accept: str) -> bool:
    """True when the client asked for markdown over HTML.

    `*/*` is treated as not preferring markdown: that is what curl, browsers,
    and most HTTP libraries send, and they all want the page.
    """
    return _quality(accept, "text/markdown") > _quality(accept, "text/html")


class StowHandler(SimpleHTTPRequestHandler):
    server_version = "stow-site"
    protocol_version = "HTTP/1.1"

    def guess_type(self, path):  # noqa: A003 - base class API
        ext = os.path.splitext(path)[1].lower()
        if ext in EXTRA_TYPES:
            return EXTRA_TYPES[ext]
        guessed, _ = mimetypes.guess_type(path)
        if not guessed:
            return "application/octet-stream"
        # Text formats are always UTF-8 here, so say so rather than letting the
        # client guess from the absence of a charset.
        if guessed.startswith("text/") and "charset" not in guessed:
            return f"{guessed}; charset=utf-8"
        return guessed

    def end_headers(self):
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Referrer-Policy", "strict-origin-when-cross-origin")
        super().end_headers()

    def _resolve(self, path: str) -> str:
        """Map a URL path to a file inside the document root, safely."""
        clean = posixpath.normpath(urlsplit(path).path)
        parts = [p for p in clean.split("/") if p and p not in (".", "..")]
        return os.path.join(self.directory, *parts)

    def do_GET(self):  # noqa: N802 - base class API
        target = self._resolve(self.path)
        is_dir = self.path.endswith("/") or os.path.isdir(target)

        if is_dir:
            index = os.path.join(target, "index.html")
            if os.path.isfile(index):
                target = index
            elif os.path.isfile(os.path.join(target, "index.md")):
                # A directory with no index.html: hand back its markdown.
                self._send(os.path.join(target, "index.md"), "text/markdown; charset=utf-8")
                return
            else:
                target = index

        if not os.path.isfile(target):
            return super().do_GET()

        content_type = self.guess_type(target)
        if os.path.splitext(target)[1] in MARKDOWN_SUFFIXES:
            content_type = EXTRA_TYPES[".md"]

        if os.path.basename(target) == "index.html" and prefers_markdown(
            self.headers.get("Accept", "")
        ):
            alternate = os.path.join(os.path.dirname(target), MARKDOWN_ROOT)
            if os.path.isfile(alternate):
                self._send(alternate, EXTRA_TYPES[".md"])
                return

        self._send(target, content_type)

    def do_HEAD(self):  # noqa: N802 - base class API
        self.do_GET()

    def _send(self, path: str, content_type: str) -> None:
        try:
            with open(path, "rb") as handle:
                body = handle.read()
        except OSError as exc:
            self.send_error(HTTPStatus.NOT_FOUND, "cannot read file")
            print(f"read failed: {exc}", file=sys.stderr)
            return

        is_html = content_type.startswith("text/html")
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", CACHE_HTML if is_html else CACHE_STATIC)
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)


def make_handler(root: str):
    """Bind a handler class to a document root.

    The root has to be passed to __init__, not set as a class attribute:
    Python 3.9+ SimpleHTTPRequestHandler.__init__ unconditionally assigns
    `self.directory = os.getcwd()` when it is not handed a directory, which
    silently clobbers the attribute and serves the wrong tree.
    """

    class BoundStowHandler(StowHandler):
        def __init__(self, *args, **kwargs):
            super().__init__(*args, directory=root, **kwargs)

    return BoundStowHandler


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default="127.0.0.1", help="interface to bind")
    parser.add_argument("--port", type=int, default=8080, help="port to bind")
    parser.add_argument(
        "--dir",
        default=os.path.dirname(os.path.abspath(__file__)),
        help="document root",
    )
    args = parser.parse_args()

    if not os.path.isdir(args.dir):
        print(f"no such directory: {args.dir}", file=sys.stderr)
        return 2

    server = ThreadingHTTPServer((args.host, args.port), make_handler(args.dir))
    shown = "localhost" if args.host in ("0.0.0.0", "127.0.0.1") else args.host
    print(f"serving {args.dir} on http://{shown}:{args.port}/ (bound {args.host})")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nstopped")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
