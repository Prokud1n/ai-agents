#!/usr/bin/env python3
"""Generates hostile PDF fixtures for extract tests: python3 make_pdf_fixtures.py (in this folder)."""
import struct
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

# /Contents lists one 4 MB stream of numbers (no operators) 20 times: each reference must be paid for.
numbers = zlib.compress(b"1 " * 2_000_000, 9)
refs = b" ".join([b"4 0 R"] * 20)
write("pdf-refs20.pdf", [catalog, b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
                         b"<< /Type /Page /Parent 2 0 R /Contents [" + refs + b"] >>",
                         stream(numbers, b"/Filter /FlateDecode")])


def objstm_self(name):
    """An xref stream says object 5 (an object stream) lives inside object stream 5 itself:
    resolving it recurses until the stack overflows."""
    out = bytearray(b"%PDF-1.5\n")
    offsets = {}

    def add(num, body):
        offsets[num] = len(out)
        out.extend(b"%d 0 obj\n" % num + body + b"\nendobj\n")

    add(1, catalog)
    add(2, b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
    add(3, b"<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>")
    add(5, b"<< /Type /ObjStm /N 1 /First 4 /Length 8 >>\nstream\n4 0 1 \nendstream")
    xref_num = 6
    offsets[xref_num] = len(out)
    rows = [struct.pack(">BIH", 0, 0, 65535)]
    rows += [struct.pack(">BIH", 1, offsets[k], 0) for k in (1, 2, 3)]
    rows += [struct.pack(">BIH", 2, 5, 0), struct.pack(">BIH", 2, 5, 1)]  # objects 4 and 5 in stream 5
    rows.append(struct.pack(">BIH", 1, offsets[xref_num], 0))
    data = b"".join(rows)
    out.extend(b"%d 0 obj\n<< /Type /XRef /Size 7 /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n" % (xref_num, len(data))
               + data + b"\nendstream\nendobj\n")
    out.extend(b"startxref\n%d\n%%%%EOF\n" % offsets[xref_num])
    with open(name, "wb") as f:
        f.write(out)


objstm_self("pdf-objstm-self.pdf")
