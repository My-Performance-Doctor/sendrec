"""Score Whisper and Amazon Transcribe against PriMock57 human transcripts.

usage: score.py <acc dir>
WER after Whisper's English normalizer (numbers, spelling, fillers).
"""
import json
import re
import sys
from pathlib import Path

import jiwer
from whisper_normalizer.english import EnglishTextNormalizer

root = Path(sys.argv[1])
norm = EnglishTextNormalizer()


def reference(name):
    text = (root / "data" / f"{name}.TextGrid").read_text(encoding="utf-8", errors="replace")
    parts = re.findall(r'^\s*text = "(.*)"\s*$', text, flags=re.M)
    joined = " ".join(p.replace('""', '"') for p in parts)
    joined = re.sub(r"<UNIN/>", " ", joined)  # unintelligible: no reference words
    joined = re.sub(r"</?[A-Z_]+/?>", " ", joined)  # keep words inside <UNSURE>…</UNSURE>
    return joined


def whisper(name):
    data = json.loads((root / "whisper" / f"{name}.json").read_text())
    return " ".join(s["text"] for s in data["transcription"])


def transcribe(name):
    data = json.loads((root / "transcribe" / f"{name}.json").read_text())
    return data["results"]["transcripts"][0]["transcript"]


names = sorted(p.stem for p in (root / "data").glob("*.wav"))
totals = {"whisper": [0, 0, 0, 0], "transcribe": [0, 0, 0, 0]}  # ref words, S, D, I
long_words = {"whisper": [0, 0], "transcribe": [0, 0]}
print(f"{'recording':28} {'words':>6} {'whisper':>8} {'transcribe':>10}")
rows = []
for name in names:
    ref = norm(reference(name))
    line = [name, len(ref.split())]
    for system, read in (("whisper", whisper), ("transcribe", transcribe)):
        try:
            hyp = norm(read(name))
        except FileNotFoundError:
            line.append(None)
            continue
        out = jiwer.process_words(ref, hyp)
        t = totals[system]
        t[0] += len(ref.split())
        t[1] += out.substitutions
        t[2] += out.deletions
        t[3] += out.insertions
        line.append(out.wer)
        # Recall of long reference words (8+ letters), a rough proxy for clinical terms.
        hyp_words = set(hyp.split())
        for w in ref.split():
            if len(w) >= 8 and w.isalpha():
                long_words[system][0] += 1
                long_words[system][1] += w in hyp_words
    rows.append(line)
    fmt = lambda v: "   n/a" if v is None else f"{v * 100:7.1f}%"
    print(f"{line[0]:28} {line[1]:6d} {fmt(line[2]):>8} {fmt(line[3]):>10}")

print()
for system, (n, s, d, i) in totals.items():
    if n:
        print(f"{system:10} pooled WER {(s + d + i) / n * 100:5.1f}%  (sub {s / n * 100:.1f}%, del {d / n * 100:.1f}%, ins {i / n * 100:.1f}%)  "
              f"long-word recall {long_words[system][1] / max(1, long_words[system][0]) * 100:.1f}% of {long_words[system][0]}")
