#!/usr/bin/env python3
"""A/B benchmark for Hungarian Whisper. Synthesizes known Hungarian sentences via the
live TTS service, transcribes them on a candidate Whisper URL, and reports WER + latency
+ GPU memory. Stdlib + curl + nvidia-smi only (no pip installs on the server)."""

import argparse, json, os, re, subprocess, time

SENTENCES = [
    "Kapcsold fel a nappali lámpát.",
    "Hány óra van most Budapesten?",
    "Milyen lesz a holnapi időjárás?",
    "Játssz egy kis zenét a konyhában.",
    "Emlékeztess, hogy tíz perc múlva indulnom kell.",
    "Mennyibe kerül egy kiló alma a boltban?",
]


def normalize(s):
    s = s.lower()
    s = re.sub(r"[^0-9a-záéíóöőúüű ]+", " ", s)
    return s.split()


def wer(ref, hyp):
    r, h = normalize(ref), normalize(hyp)
    n, m = len(r), len(h)
    if n == 0:
        return 0.0 if m == 0 else 1.0
    d = list(range(m + 1))
    for i in range(1, n + 1):
        prev, d[0] = d[0], i
        for j in range(1, m + 1):
            cur = d[j]
            cost = 0 if r[i - 1] == h[j - 1] else 1
            d[j] = min(d[j] + 1, d[j - 1] + 1, prev + cost)
            prev = cur
    return d[m] / n


def gpu_used_mib():
    out = subprocess.check_output(
        ["nvidia-smi", "--query-gpu=memory.used", "--format=csv,noheader,nounits"]
    ).decode().strip().splitlines()
    return int(out[0])


def tts(text, voice, tts_url, path):
    payload = json.dumps({"input": text, "voice": voice, "response_format": "opus"})
    subprocess.check_call([
        "curl", "-sS", "-X", "POST", f"{tts_url}/v1/audio/speech",
        "-H", "Content-Type: application/json", "-d", payload, "-o", path,
    ])
    if os.path.getsize(path) == 0:
        raise RuntimeError(f"TTS produced empty file for: {text!r}")


def transcribe(path, whisper_url):
    out = subprocess.check_output([
        "curl", "-sS", "-X", "POST", f"{whisper_url}/v1/audio/transcriptions",
        "-F", f"file=@{path};filename=clip.ogg", "-F", "language=hu",
    ]).decode()
    return json.loads(out).get("text", "")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--whisper-url", default="http://127.0.0.1:13001")
    ap.add_argument("--tts-url", default="http://127.0.0.1:14000")
    ap.add_argument("--voice", default="hu-HU-NoemiNeural")
    ap.add_argument("--out-dir", default="/tmp/whisper-bench")
    args = ap.parse_args()
    os.makedirs(args.out_dir, exist_ok=True)

    idle = gpu_used_mib()
    print(f"GPU used (candidate idle): {idle} MiB")
    rows, peak = [], idle
    for i, text in enumerate(SENTENCES):
        clip = os.path.join(args.out_dir, f"clip_{i}.ogg")
        if not os.path.exists(clip):
            tts(text, args.voice, args.tts_url, clip)
        # warm-up first call is not timed (model graph/JIT); time a second pass
        transcribe(clip, args.whisper_url)
        t0 = time.time()
        hyp = transcribe(clip, args.whisper_url)
        dt = time.time() - t0
        peak = max(peak, gpu_used_mib())
        e = wer(text, hyp)
        rows.append((e, dt))
        print(f"[{i}] WER={e:.3f} {dt*1000:6.0f}ms  ref={text!r}  hyp={hyp!r}")

    avg_wer = sum(r[0] for r in rows) / len(rows)
    avg_ms = sum(r[1] for r in rows) / len(rows) * 1000
    print("-" * 70)
    print(f"avg WER: {avg_wer:.3f} | avg latency: {avg_ms:.0f} ms | "
          f"peak GPU: {peak} MiB (footprint ~{peak - idle} MiB over idle)")


if __name__ == "__main__":
    main()
