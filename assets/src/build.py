"""Export the quietbox 'Calm' brand assets as SVG (text outlined) and PNG."""
import os
import sys

import resvg_py
import uharfbuzz as hb
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont

FONT_DIR, OUT = sys.argv[1], sys.argv[2]
os.makedirs(OUT, exist_ok=True)

SAND, SAND_LINE, INK, MUTED, CLAY_TEXT, CLAY = "#F4EDE2", "#E2D3BE", "#2B211B", "#6B5A4C", "#9E4A2C", "#C2643F"
NIGHT, NIGHT_TEXT, NIGHT_MUTED, CLAY_DARK = "#1F1915", "#F4EDE2", "#BBA996", "#E07F57"

FONTS = {}


def font(weight):
    if weight not in FONTS:
        path = os.path.join(FONT_DIR, f"Manrope-{weight}.ttf")
        data = open(path, "rb").read()
        FONTS[weight] = (TTFont(path), hb.Font(hb.Face(data)))
    return FONTS[weight]


def text_path(text, x, y, size, weight, fill, tracking=0.0, anchor="start"):
    """Shape text with HarfBuzz and return an SVG <path> of its outlines."""
    tt, hbfont = font(weight)
    upem = tt["head"].unitsPerEm
    buf = hb.Buffer()
    buf.add_str(text)
    buf.guess_segment_properties()
    hb.shape(hbfont, buf, {"kern": True, "liga": True})
    scale = size / upem
    track = tracking * upem
    glyph_set = tt.getGlyphSet()
    order = tt.getGlyphOrder()
    advance_total = sum(p.x_advance + track for p in buf.glyph_positions) - track
    if anchor == "end":
        x -= advance_total * scale
    pen = SVGPathPen(glyph_set)
    cursor = 0.0
    for info, pos in zip(buf.glyph_infos, buf.glyph_positions):
        name = order[info.codepoint]
        tpen = TransformPen(pen, (scale, 0, 0, -scale, x + (cursor + pos.x_offset) * scale, y - pos.y_offset * scale))
        glyph_set[name].draw(tpen)
        cursor += pos.x_advance + track
    return f'<path fill="{fill}" d="{pen.getCommands()}"/>'


def mark(x, y, size, stroke, accent):
    s = size / 100
    return (
        f'<g transform="translate({x} {y}) scale({s})" fill="none" stroke-linecap="round" stroke-linejoin="round">'
        f'<rect x="16" y="22" width="68" height="58" rx="14" stroke="{stroke}" stroke-width="5"/>'
        f'<path d="M16 55 H33 L39 65 H61 L67 55 H84" stroke="{stroke}" stroke-width="5"/>'
        f'<line x1="39" y1="39" x2="61" y2="39" stroke="{accent}" stroke-width="5.5"/>'
        "</g>"
    )


def favicon_mark(stroke, accent):
    return (
        '<g fill="none" stroke-linecap="round" stroke-linejoin="round">'
        f'<rect x="3.5" y="4.5" width="25" height="23" rx="6" stroke="{stroke}" stroke-width="3"/>'
        f'<path d="M3.5 18 H10.5 L12.5 21.5 H19.5 L21.5 18 H28.5" stroke="{stroke}" stroke-width="3"/>'
        f'<line x1="12.5" y1="11" x2="19.5" y2="11" stroke="{accent}" stroke-width="3.2"/>'
        "</g>"
    )


def svg(w, h, body, title, bg=None, radius=0):
    rect = f'<rect width="{w}" height="{h}" rx="{radius}" fill="{bg}"/>' if bg else ""
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="{h}" viewBox="0 0 {w} {h}" role="img" aria-label="{title}">'
        f"<title>{title}</title>{rect}{body}</svg>\n"
    )


def write(name, content, png_scales=()):
    path = os.path.join(OUT, name + ".svg")
    with open(path, "w") as f:
        f.write(content)
    for scale, suffix in png_scales:
        png = resvg_py.svg_to_bytes(svg_string=content, zoom=scale)
        with open(os.path.join(OUT, name + suffix + ".png"), "wb") as f:
            f.write(bytes(png))


TAGLINE = "A calmer inbox, on a server you own."


def banner(dark):
    bg, ink, muted, accent = (NIGHT, NIGHT_TEXT, NIGHT_MUTED, CLAY_DARK) if dark else (SAND, INK, MUTED, CLAY)
    body = (
        mark(112, 85, 150, ink, accent)
        + text_path("quietbox", 302, 172, 96, 700, ink, tracking=-0.04)
        + text_path(TAGLINE, 302, 230, 30, 500, muted)
    )
    return svg(1280, 320, body, "quietbox — " + TAGLINE, bg)


def social():
    body = (
        mark(112, 96, 200, INK, CLAY)
        + text_path("quietbox", 360, 214, 120, 700, INK, tracking=-0.04)
        + text_path(TAGLINE, 360, 282, 36, 500, MUTED)
        + f'<rect x="112" y="478" width="1056" height="2" fill="{SAND_LINE}"/>'
        + text_path("Self-hosted inbox triage for any IMAP mailbox", 112, 534, 26, 500, MUTED)
        + text_path("quietbox.sh", 1168, 534, 26, 700, CLAY_TEXT, anchor="end")
    )
    return svg(1280, 640, body, "quietbox — " + TAGLINE, SAND)


write("banner-light", banner(False), [(2, "@2x")])
write("banner-dark", banner(True), [(2, "@2x")])
write("social-preview", social(), [(1, "")])
write("icon", svg(512, 512, mark(96, 96, 320, INK, CLAY), "quietbox", SAND), [(1, "")])
write("icon-dark", svg(512, 512, mark(96, 96, 320, NIGHT_TEXT, CLAY_DARK), "quietbox", NIGHT), [(1, "")])
write("logo-mark", svg(100, 100, mark(0, 0, 100, INK, CLAY), "quietbox"))
write("logo-mark-dark", svg(100, 100, mark(0, 0, 100, NIGHT_TEXT, CLAY_DARK), "quietbox"))
# favicon.svg adapts to the browser's colour scheme
fav = (
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">'
    "<style>.s{stroke:%s}.a{stroke:%s}@media (prefers-color-scheme:dark){.s{stroke:%s}.a{stroke:%s}}</style>"
    '<g fill="none" stroke-linecap="round" stroke-linejoin="round">'
    '<rect class="s" x="3.5" y="4.5" width="25" height="23" rx="6" stroke-width="3"/>'
    '<path class="s" d="M3.5 18 H10.5 L12.5 21.5 H19.5 L21.5 18 H28.5" stroke-width="3"/>'
    '<line class="a" x1="12.5" y1="11" x2="19.5" y2="11" stroke-width="3.2"/>'
    "</g></svg>\n"
) % (INK, CLAY, NIGHT_TEXT, CLAY_DARK)
with open(os.path.join(OUT, "favicon.svg"), "w") as f:
    f.write(fav)
fav_tile = svg(32, 32, favicon_mark(INK, CLAY), "quietbox", SAND, radius=7)
for px in (32, 180, 512):
    with open(os.path.join(OUT, f"favicon-{px}.png"), "wb") as f:
        f.write(bytes(resvg_py.svg_to_bytes(svg_string=fav_tile, zoom=px / 32)))
print("\n".join(sorted(os.listdir(OUT))))
