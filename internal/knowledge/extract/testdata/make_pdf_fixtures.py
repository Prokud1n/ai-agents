#!/usr/bin/env python3
"""Generates hostile PDF fixtures for extract tests: python3 make_pdf_fixtures.py (in this folder)."""
import zlib

FONT = b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"
CONTENT = b"BT /F1 12 Tf 72 700 Td (Hello brand) Tj ET"


def stream(data, extra=b""):
    return b"<< /Length %d %s >>\nstream\n" % (len(data), extra) + data + b"\nendstream"


def page(contents_ref=4):
    return b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents %d 0 R >>" % contents_ref


def write(name, objects):
    out = bytearray(b"%PDF-1.4\n")
    offsets = []
    for i, body in enumerate(objects, start=1):
        offsets.append(len(out))
        out += b"%d 0 obj\n" % i + body + b"\nendobj\n"
    xref = len(out)
    out += b"xref\n0 %d\n0000000000 65535 f \n" % (len(objects) + 1)
    for off in offsets:
        out += b"%010d 00000 n \n" % off
    out += b"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (len(objects) + 1, xref)
    with open(name, "wb") as f:
        f.write(out)


catalog = b"<< /Type /Catalog /Pages 2 0 R >>"

# /Count lies: the library would allocate by it; the page tree has one page.
write("pdf-count-1e9.pdf", [catalog, b"<< /Type /Pages /Kids [3 0 R] /Count 1000000000 >>", page(), stream(CONTENT), FONT])
# /Kids points back to the /Pages node itself: the library's Page(1) loops forever.
write("pdf-kids-cycle.pdf", [catalog, b"<< /Type /Pages /Kids [2 0 R] /Count 1 >>", page(), stream(CONTENT), FONT])
# Unknown filter: the library panics while reading the stream (after NewReader succeeded).
write("pdf-bad-filter.pdf", [catalog, b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>", page(), stream(CONTENT, b"/Filter /FooDecode"), FONT])
# 8 MB of spaces deflated to a few KB: rejected before the library inflates it.
bomb = zlib.compress(b" " * (8 << 20), 9)
write("pdf-stream-bomb.pdf", [catalog, b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>", page(), stream(bomb, b"/Filter /FlateDecode"), FONT])
