#!/usr/bin/env -S uv run --with pillow --script
# /// script
# dependencies = ["pillow"]
# ///
"""Render the title slides that sit between demo scenes.

Usage: slides.py <out-dir> [width height]
Writes <out-dir>/slide-NN.png for every entry in SLIDES. The palette matches
the Catppuccin Mocha theme the tapes use, so a slide cuts to a terminal
frame without a background jump.
"""
import sys
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

BG = (30, 30, 46)          # Catppuccin Mocha base
ACCENT = (137, 180, 250)   # Mocha blue — the prompt color in the tapes
TITLE = (205, 214, 244)    # Mocha text
SUB = (166, 173, 200)      # Mocha subtext0
KICKER = (147, 153, 178)   # Mocha overlay2

PRODUCT = "Cloud Intelligence™ CLI"

# (file stem, kicker, title, subtitle lines)
SLIDES = [
    ("00-title", "What's new · August–September 2026", PRODUCT,
     ["Ten features that shipped between v2.5.2 and v2.7.6,",
      "recorded live against the doit.com tenant."]),
    ("01-ai-session", "Section 1 of 10", "Ask, don't query",
     ["Bare dci opens the AI session. Doers run keyless on DoiT-provided access.",
      "Ask about your cloud costs in plain English; the AI runs the commands."]),
    ("02-budgets-at-risk", "Section 2 of 10", "Question-shaped commands",
     ["dci budgets-at-risk answers “which budgets are at risk?” in one command,",
      "no filter or sort to hand-build."]),
    ("03-anomalies-recent", "Section 3 of 10", "Recent anomalies",
     ["dci anomalies-recent looks back over a window you choose",
      "and filters by severity."]),
    ("04-list-budgets", "Section 4 of 10", "Budgets at a glance",
     ["Color-graded utilization bars, the rows you came for nearest the prompt,",
      "and a hint when output runs past the screen."]),
    ("05-charts", "Section 5 of 10", "Charts in the terminal",
     ["--chart draws stacked columns per service, --chart=treemap draws each",
      "service's share, --chart=heatmap finds the hot service and the hot month."]),
    ("06-rollup-search", "Section 6 of 10", "Rollup and search",
     ["--rollup totals report rows client-side, no spreadsheet step.",
      "--search finds items in any list, across every page."]),
    ("07-help-catalog", "Section 7 of 10", "Help with real examples",
     ["Every example in --help is checked against the live API before release.",
      "dci commands --json carries the same examples for agents and scripts."]),
    ("08-datahub", "Section 8 of 10", "DataHub round trip",
     ["Export a whole dataset with --all, rewrite it with --for-reimport,",
      "and upload it to another dataset with file: @records.csv."]),
    ("09-pickers", "Section 9 of 10", "Pickers everywhere",
     ["Leave the name off any command and filter as you type.",
      "Names resolve from partial matches; Tab completes them."]),
    ("10-update", "Section 10 of 10", "Update in place",
     ["dci update knows how you installed the CLI and runs the right upgrade:",
      "Homebrew, Scoop, WinGet, .deb/.rpm, or a standalone binary."]),
    ("99-closing", "That's the tour", "Thanks for watching",
     ["Docs: help.doit.com/docs/cli",
      "Changelog: help.doit.com/docs/cli/changelog"]),
]


def font(size, bold=False):
    # Avenir Next ships with macOS as a collection: index 7 is Regular and
    # index 2 is Demi Bold. Fall back to Menlo so the script never dies.
    candidates = [
        ("/System/Library/Fonts/Avenir Next.ttc", 2 if bold else 7),
        ("/System/Library/Fonts/Menlo.ttc", 1 if bold else 0),
    ]
    for path, index in candidates:
        try:
            return ImageFont.truetype(path, size, index=index)
        except OSError:
            continue
    return ImageFont.load_default()


def render(width, height, kicker, title, subtitle):
    img = Image.new("RGB", (width, height), BG)
    draw = ImageDraw.Draw(img)
    s = width / 3840  # scale every measure from the 4K design
    margin = int(320 * s)
    bar_w = int(16 * s)
    f_kicker, f_title, f_sub = font(int(56 * s)), font(int(168 * s), bold=True), font(int(72 * s))

    # Vertical block: kicker, title, subtitle lines — centered as a group.
    gap_k, gap_t, gap_s = int(40 * s), int(72 * s), int(24 * s)
    h_k = f_kicker.getbbox("Ag")[3]
    h_t = f_title.getbbox("Ag")[3]
    h_s = f_sub.getbbox("Ag")[3]
    block = h_k + gap_k + h_t + gap_t + len(subtitle) * (h_s + gap_s)
    y = (height - block) // 2

    draw.rectangle([margin - int(64 * s), y, margin - int(64 * s) + bar_w, y + block], fill=ACCENT)
    draw.text((margin, y), kicker, font=f_kicker, fill=ACCENT)
    y += h_k + gap_k
    draw.text((margin, y), title, font=f_title, fill=TITLE)
    y += h_t + gap_t
    for line in subtitle:
        draw.text((margin, y), line, font=f_sub, fill=SUB)
        y += h_s + gap_s

    foot = font(int(40 * s))
    draw.text((margin, height - int(160 * s)), PRODUCT + "  ·  doit.com", font=foot, fill=KICKER)
    return img


def main():
    out = Path(sys.argv[1] if len(sys.argv) > 1 else "out")
    width = int(sys.argv[2]) if len(sys.argv) > 2 else 3840
    height = int(sys.argv[3]) if len(sys.argv) > 3 else 2160
    out.mkdir(parents=True, exist_ok=True)
    for stem, kicker, title, subtitle in SLIDES:
        path = out / f"slide-{stem}.png"
        render(width, height, kicker, title, subtitle).save(path)
        print(path)


if __name__ == "__main__":
    main()
