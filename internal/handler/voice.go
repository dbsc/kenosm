package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Voice captures audio server-side from the host's default microphone and
// feeds it to the STT command. Capture happens outside the browser on
// purpose: WebKitGTK denies getUserMedia permission requests, so in-browser
// recording is not an option for the native app. The API is a toggle —
// start a capture, speak, stop it — and the stop response carries the text.
type Voice struct {
	stt        *Transcribe
	mu         sync.Mutex
	caps       map[int]*voiceCapture
	nextID     int
	args       string // capture prefix, e.g. "ffmpeg -f pulse -i default"
	dir        string
	maxSeconds int
}

type voiceCapture struct {
	cmd  *exec.Cmd
	file string
}

func NewVoice(stt *Transcribe, args string) *Voice {
	if strings.TrimSpace(args) == "" {
		args = "ffmpeg -loglevel error -f pulse -i default"
	}
	return &Voice{
		stt:        stt,
		caps:       map[int]*voiceCapture{},
		args:       args,
		dir:        filepath.Join(os.TempDir(), "kenosm-voice"),
		maxSeconds: 60,
	}
}

// stopCapture ends a capture gracefully: SIGTERM first so ffmpeg flushes its
// AVIO buffer and finalizes the WAV header (a SIGKILL would leave the file
// empty — the buffered data dies with the process), then a hard kill if it
// does not exit within a few seconds.
func stopCapture(c *voiceCapture) {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_, _ = c.cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
	}
}

// Start launches the capture process in the background and returns its id.
func (v *Voice) Start(w http.ResponseWriter, _ *http.Request) {
	v.mu.Lock()
	defer v.mu.Unlock()
	_ = os.MkdirAll(v.dir, 0o755)
	id := v.nextID
	v.nextID++
	file := filepath.Join(v.dir, fmt.Sprintf("cap-%d.wav", id))
	errLog, _ := os.Create(filepath.Join(v.dir, fmt.Sprintf("cap-%d.err", id)))
	// -flush_packets 1 makes ffmpeg write every audio packet to disk as it
	// arrives: even if the process is killed hard later, the file holds the
	// audio captured so far (no AVIO buffering losses).
	cmd := exec.Command("bash", "-c", v.args+" -y -flush_packets 1 -ar 16000 -ac 1 "+shellQuote(file))
	if errLog != nil {
		cmd.Stderr = errLog
	}
	if err := cmd.Start(); err != nil {
		if errLog != nil {
			errLog.Close()
		}
		writeError(w, http.StatusInternalServerError, "cannot start capture: "+err.Error()+" (is a microphone connected?)")
		return
	}
	if errLog != nil {
		// The child inherited its own copy of the fd; close the parent's so a
		// capture does not leak a file descriptor for its whole lifetime.
		_ = errLog.Close()
	}
	v.caps[id] = &voiceCapture{cmd: cmd, file: file}
	// Hard cap: a capture can never outlive its slot, so a forgotten stop
	// cannot leak ffmpeg processes.
	go func() {
		time.Sleep(time.Duration(v.maxSeconds) * time.Second)
		v.mu.Lock()
		if c, ok := v.caps[id]; ok {
			delete(v.caps, id)
			stopCapture(c)
			_ = os.Remove(c.file)
		}
		v.mu.Unlock()
	}()
	writeJSON(w, http.StatusOK, map[string]any{"capture_id": id})
}

// Stop kills the capture and returns the transcription of what was recorded.
func (v *Voice) Stop(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CaptureID int `json:"capture_id"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: {capture_id}")
		return
	}
	v.mu.Lock()
	c, ok := v.caps[in.CaptureID]
	if ok {
		delete(v.caps, in.CaptureID)
	}
	v.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "capture not found (already stopped or expired)")
		return
	}
	stopCapture(c)
	defer os.Remove(c.file)
	errFile := filepath.Join(v.dir, strings.TrimSuffix(filepath.Base(c.file), ".wav")+".err")
	defer os.Remove(errFile)

	if st, err := os.Stat(c.file); err != nil || st.Size() < 1000 {
		detail := ""
		if b, rerr := os.ReadFile(errFile); rerr == nil {
			detail = ": " + truncateText(string(b), 300)
		}
		writeError(w, http.StatusUnprocessableEntity, "no audio captured (microphone may be unavailable)"+detail)
		return
	}

	text, err := v.runSTT(c.file)
	if err != nil {
		writeError(w, http.StatusBadGateway, "transcription failed: "+err.Error())
		return
	}
	if text == "" {
		writeError(w, http.StatusUnprocessableEntity, "no speech recognized")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text})
}

// runSTT runs the configured STT command on the recorded file and parses the
// output (plain text or {"text": "..."}).
func (v *Voice) runSTT(file string) (string, error) {
	if strings.TrimSpace(v.stt.cmd) == "" {
		return "", fmt.Errorf("voice input is not configured: set STT_CMD (see stt/transcribe.py)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "bash", "-c", v.stt.cmd+" "+shellQuote(file)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, truncateText(string(out), 200))
	}
	return parseTextOutput(string(out)), nil
}
