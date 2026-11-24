package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Transcribe turns a recorded voice clip into text via an external
// speech-to-text command (STT_CMD). The command receives the audio file path
// as its last argument and must print the transcription — plain text or
// {"text": "..."} JSON — to stdout.
type Transcribe struct {
	cmd string
}

func NewTranscribe(cmd string) *Transcribe { return &Transcribe{cmd: cmd} }

func (t *Transcribe) Transcribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AudioBase64 string `json:"audio_base64"`
		Mime        string `json:"mime"`
	}
	if err := decodeJSON(w, r, &in); err != nil || strings.TrimSpace(in.AudioBase64) == "" {
		writeError(w, http.StatusBadRequest, "invalid body: {audio_base64, mime}")
		return
	}
	if strings.TrimSpace(t.cmd) == "" {
		writeError(w, http.StatusNotImplemented, "voice input is not configured: set STT_CMD (see stt/transcribe.py)")
		return
	}

	// The payload may be a full data URL ("data:audio/webm;base64,...").
	b64 := in.AudioBase64
	if i := strings.Index(b64, ","); i >= 0 && strings.Contains(b64[:i], "base64") {
		b64 = b64[i+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid base64 audio: "+err.Error())
		return
	}

	ext := ".webm"
	switch in.Mime {
	case "audio/ogg", "audio/ogg; codecs=opus":
		ext = ".ogg"
	case "audio/wav":
		ext = ".wav"
	case "audio/mp4":
		ext = ".m4a"
	case "audio/mpeg":
		ext = ".mp3"
	}
	f, err := os.CreateTemp("", "kenosm-voice-*"+ext)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		f.Close()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	f.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "bash", "-c", t.cmd+" "+shellQuote(f.Name())).CombinedOutput()
	if err != nil {
		writeError(w, http.StatusBadGateway, "transcription failed: "+err.Error()+": "+truncateText(string(out), 200))
		return
	}

	text := parseTextOutput(string(out))
	if text == "" {
		writeError(w, http.StatusUnprocessableEntity, "no speech recognized")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text})
}

// parseTextOutput parses an STT command's stdout: plain text, or a {"text": "..."}
// JSON object. It returns the transcription text (whitespace-trimmed).
func parseTextOutput(out string) string {
	text := strings.TrimSpace(out)
	if strings.HasPrefix(text, "{") {
		var parsed struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(text), &parsed); err == nil {
			return parsed.Text
		}
	}
	return text
}

// shellQuote wraps a path for the shell command line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
