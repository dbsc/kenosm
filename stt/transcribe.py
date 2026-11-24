"""Transcribe one audio file to text via faster-whisper (tiny model, CPU).

Usage: transcribe.py <audio-file>
Prints {"text": "...", "language": "..."} as JSON to stdout.

Any container ffmpeg understands (webm/ogg/mp3/wav/m4a) is decoded to 16 kHz
mono PCM first, then fed to the model as a numpy array — this avoids PyAV,
whose v19 wheels (the only ones available for newer Pythons) dropped the
metadata_errors keyword that faster-whisper 1.2.x passes to av.open().
"""
import json
import os
import subprocess
import sys
import tempfile
import wave

import numpy as np
from faster_whisper import WhisperModel

model = WhisperModel("tiny", device="cpu", compute_type="int8")

wav_path = tempfile.NamedTemporaryFile(suffix=".wav", delete=False).name
try:
    subprocess.run(
        ["ffmpeg", "-y", "-loglevel", "error", "-i", sys.argv[1],
         "-ar", "16000", "-ac", "1", "-f", "wav", wav_path],
        check=True, capture_output=True,
    )
    with wave.open(wav_path, "rb") as wf:
        pcm = wf.readframes(wf.getnframes())
    audio = np.frombuffer(pcm, dtype=np.int16).astype(np.float32) / 32768.0
finally:
    try:
        os.remove(wav_path)
    except OSError:
        pass

segments, info = model.transcribe(audio, language=None, beam_size=5)
text = " ".join(s.text for s in segments).strip()
print(json.dumps({"text": text, "language": info.language}))
