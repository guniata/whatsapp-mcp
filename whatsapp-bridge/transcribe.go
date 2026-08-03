package main

// On-device transcription of WhatsApp voice notes.
//
// WhatsApp ships voice notes as Ogg/Opus, which whisper.cpp cannot read, so we
// decode to 16 kHz mono PCM in pure Go (no cgo, no ffmpeg) and hand whisper a
// plain WAV file. Everything stays on this machine: Apple's on-device speech
// recognition has no Hebrew model, and its server-based path would upload
// audio, which this product promises not to do.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pion/opus"
	"github.com/pion/opus/pkg/oggreader"
	"go.mau.fi/whatsmeow"
	waLog "go.mau.fi/whatsmeow/util/log"
)

const (
	opusSampleRate    = 48000 // Opus always decodes at 48 kHz
	whisperSampleRate = 16000 // what whisper.cpp expects
)

func modelsDir() string { return filepath.Join(appHome(), "models") }

func whisperModelPath() string {
	if v := os.Getenv("WHATSAPP_WHISPER_MODEL"); v != "" {
		return v
	}
	return filepath.Join(modelsDir(), "ggml-large-v3-turbo.bin")
}

// whisperBinary finds the transcription binary: the copy bundled next to this
// executable first, then the usual install locations. launchd gives the bridge
// a minimal PATH, so well-known paths are checked explicitly rather than
// relying on a PATH lookup alone.
func whisperBinary() string {
	var candidates []string
	if v := os.Getenv("WHATSAPP_WHISPER_BIN"); v != "" {
		candidates = append(candidates, v)
	}
	if self, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(self), "whisper-cli"))
	}
	if p, err := exec.LookPath("whisper-cli"); err == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates,
		"/opt/homebrew/bin/whisper-cli",
		"/usr/local/bin/whisper-cli",
	)
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

const whisperModelURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin"

// transcriptionAvailable reports whether transcription can run, and why not.
func transcriptionAvailable() (bool, string) {
	if whisperBinary() == "" {
		return false, "the speech-to-text engine is not installed"
	}
	if _, err := os.Stat(whisperModelPath()); err != nil {
		return false, "the speech model has not finished downloading yet"
	}
	return true, ""
}

var modelDownloadMu sync.Mutex

func placeholdersN(n int) string {
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

// looksLikeGGMLModel checks the file really is a ggml model rather than, say, a
// captive-portal login page served with HTTP 200. Without this, one bad
// download is renamed into place and treated as valid forever.
func looksLikeGGMLModel(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.Size() < 100*1024*1024 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return false
	}
	return string(magic) == "ggml" || string(magic) == "lmgg"
}

// ensureWhisperModel downloads the speech model on first use. It is ~1.5 GB, so
// it cannot ship inside the extension; the download runs once, in the
// background, and transcription simply starts working when it lands.
func ensureWhisperModel(logger waLog.Logger) bool {
	modelDownloadMu.Lock()
	defer modelDownloadMu.Unlock()

	if looksLikeGGMLModel(whisperModelPath()) {
		return true
	}
	// A file that exists but is not a model (truncated, or an error page saved
	// with HTTP 200) must be discarded, or it would be trusted forever.
	if _, err := os.Stat(whisperModelPath()); err == nil {
		logger.Warnf("speech model at %s is not a valid model; re-downloading", whisperModelPath())
		os.Remove(whisperModelPath())
	}
	if err := os.MkdirAll(modelsDir(), 0755); err != nil {
		logger.Warnf("cannot create models directory: %v", err)
		return false
	}
	tmp := whisperModelPath() + ".part"
	os.Remove(tmp)

	fmt.Println("Downloading the speech model for voice-note transcription (about 1.5 GB, one time)…")
	client := &http.Client{Timeout: 2 * time.Hour}
	resp, err := client.Get(whisperModelURL)
	if err != nil {
		logger.Warnf("speech model download failed: %v", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logger.Warnf("speech model download failed: HTTP %d", resp.StatusCode)
		return false
	}
	f, err := os.Create(tmp)
	if err != nil {
		logger.Warnf("cannot write speech model: %v", err)
		return false
	}
	// Cap the transfer so a misbehaving endpoint cannot fill the disk.
	n, err := io.Copy(f, io.LimitReader(resp.Body, 4<<30))
	f.Close()
	if err != nil {
		os.Remove(tmp)
		logger.Warnf("speech model download interrupted: %v", err)
		return false
	}
	if !looksLikeGGMLModel(tmp) {
		os.Remove(tmp)
		logger.Warnf("downloaded speech model failed its integrity check (%d bytes); will retry later", n)
		return false
	}
	// Rename only after a complete, verified download so a partial or bogus
	// file is never mistaken for a usable model.
	if err := os.Rename(tmp, whisperModelPath()); err != nil {
		logger.Warnf("cannot finalise speech model: %v", err)
		return false
	}
	fmt.Printf("Speech model ready (%d MB). Voice notes will now be transcribed.\n", n/(1024*1024))
	return true
}

// decodeOggOpusToPCM decodes an Ogg/Opus file to mono 16 kHz 16-bit samples.
func decodeOggOpusToPCM(path string) ([]int16, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	ogg, _, err := oggreader.NewWith(f)
	if err != nil {
		return nil, fmt.Errorf("not a readable Ogg file: %w", err)
	}

	decoder := opus.NewDecoder()
	// 120 ms at 48 kHz is the longest frame Opus can produce; double it so a
	// stereo frame also fits.
	pcm := make([]int16, opusSampleRate/1000*120*2)

	var samples48k []int16
	// An Opus packet can be split across several Ogg segments: a segment of
	// exactly 255 bytes means "continues in the next one". Feeding fragments
	// to the decoder yields "malformed packet" and silently drops audio, so
	// reassemble before decoding.
	var packet []byte
	decode := func(p []byte) {
		if len(p) == 0 ||
			bytes.HasPrefix(p, []byte("OpusHead")) || bytes.HasPrefix(p, []byte("OpusTags")) {
			return
		}
		// sampleCount is per channel. WhatsApp voice notes are mono; if a
		// stereo file ever appears we take the left channel, which stays
		// intelligible.
		n, err := decoder.DecodeToInt16(p, pcm)
		if err != nil || n <= 0 {
			// A damaged packet shouldn't abandon the whole recording.
			return
		}
		if n > len(pcm) {
			n = len(pcm)
		}
		samples48k = append(samples48k, pcm[:n]...)
	}

	for {
		segments, _, err := ogg.ParseNextPage()
		if err != nil {
			break // includes io.EOF
		}
		for _, segment := range segments {
			packet = append(packet, segment...)
			if len(segment) < 255 {
				decode(packet)
				packet = packet[:0]
			}
		}
	}
	decode(packet) // trailing packet, if the stream ended mid-lacing

	if len(samples48k) == 0 {
		return nil, fmt.Errorf("no audio could be decoded")
	}

	// 48 kHz -> 16 kHz by averaging each group of 3 samples (cheap low-pass,
	// plenty for speech).
	out := make([]int16, 0, len(samples48k)/3)
	for i := 0; i+2 < len(samples48k); i += 3 {
		out = append(out, int16((int32(samples48k[i])+int32(samples48k[i+1])+int32(samples48k[i+2]))/3))
	}
	return out, nil
}

func writeWAV(path string, samples []int16) error {
	var buf bytes.Buffer
	dataLen := len(samples) * 2
	write := func(v any) { binary.Write(&buf, binary.LittleEndian, v) }

	buf.WriteString("RIFF")
	write(uint32(36 + dataLen))
	buf.WriteString("WAVEfmt ")
	write(uint32(16))                // PCM chunk size
	write(uint16(1))                 // PCM format
	write(uint16(1))                 // mono
	write(uint32(whisperSampleRate)) // sample rate
	write(uint32(whisperSampleRate * 2))
	write(uint16(2))  // block align
	write(uint16(16)) // bits per sample
	buf.WriteString("data")
	write(uint32(dataLen))
	for _, s := range samples {
		write(s)
	}
	return os.WriteFile(path, buf.Bytes(), 0644)
}

// transcribeAudioFile turns an Ogg/Opus voice note into text.
func transcribeAudioFile(oggPath string) (string, error) {
	if ok, why := transcriptionAvailable(); !ok {
		return "", fmt.Errorf("%s", why)
	}

	samples, err := decodeOggOpusToPCM(oggPath)
	if err != nil {
		return "", err
	}
	// Whisper needs at least a moment of audio to say anything useful.
	if len(samples) < whisperSampleRate/2 {
		return "", fmt.Errorf("recording too short to transcribe")
	}

	tmp, err := os.CreateTemp("", "wa-transcribe-*.wav")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	if os.Getenv("WHATSAPP_KEEP_WAV") != "" {
		tmpPath = os.Getenv("WHATSAPP_KEEP_WAV")
	} else {
		defer os.Remove(tmpPath)
	}

	if err := writeWAV(tmpPath, samples); err != nil {
		return "", err
	}

	// -nt strips timestamps, -np strips progress noise, "auto" lets whisper
	// detect the language so mixed-language chats work without configuration.
	lang := os.Getenv("WHATSAPP_WHISPER_LANG")
	if lang == "" {
		lang = "auto"
	}
	bin := whisperBinary()
	cmd := exec.Command(bin,
		"-m", whisperModelPath(),
		"-f", tmpPath,
		"-l", lang,
		"-nt", "-np",
		// Discard segments whisper itself judges to contain no speech, and
		// suppress non-speech tokens. Both reduce invented text on near-silent
		// clips before the repetition check below has to catch it.
		"-nth", "0.6",
		"-sns",
	)
	// ggml has Homebrew's Cellar path compiled in as its backend directory and
	// does NOT fall back to searching beside the library or the executable
	// (verified: without that path it aborts outright). GGML_BACKEND_PATH names
	// exactly one .so — no directories, no lists — so point it at the bundled
	// CPU backend, which is the one ggml cannot run without.
	if so := bundledCPUBackend(bin); so != "" {
		cmd.Env = append(os.Environ(), "GGML_BACKEND_PATH="+so)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("transcription failed: %v (%s)", err, lastLine(stderr.String()))
	}

	text := strings.TrimSpace(stdout.String())
	// whisper emits bracketed markers for silence/noise; they are not speech.
	if text == "" || (strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]")) {
		return "", nil
	}
	if isHallucinatedTranscript(text) {
		return "", nil
	}
	return text, nil
}

// isHallucinatedTranscript detects Whisper's signature output for clips with no
// speech: one short phrase repeated over and over ("Thank you. Thank you. …").
// Storing nothing is recoverable; storing invented words is not, because
// nothing downstream can tell them apart from a real transcript.
func isHallucinatedTranscript(text string) bool {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == '\n'
	})
	var phrases []string
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			phrases = append(phrases, strings.ToLower(f))
		}
	}
	if len(phrases) < 4 {
		return false
	}
	counts := map[string]int{}
	for _, p := range phrases {
		counts[p]++
	}
	most, mostPhrase := 0, ""
	for p, c := range counts {
		if c > most {
			most, mostPhrase = c, p
		}
	}
	// Only treat it as fabricated when a *short* phrase dominates almost the
	// whole transcript. Real speech can legitimately repeat ("No! No! No!"), so
	// the bar is deliberately high: discarding a real message is unrecoverable,
	// because "" is the sentinel for "nothing was said".
	if len(mostPhrase) > 40 {
		return false
	}
	return most >= 4 && float64(most)/float64(len(phrases)) > 0.8
}

// bundledCPUBackend returns the ggml CPU backend shipped alongside our whisper
// copy, or "" when whisper came from a system install that can find its own.
// The apple_mN variants are interchangeable in practice (an m1 build loads on
// an m4), so the newest available is fine.
func bundledCPUBackend(whisperPath string) string {
	libs := filepath.Join(filepath.Dir(whisperPath), "lib")
	for _, name := range []string{
		"libggml-cpu-apple_m4.so",
		"libggml-cpu-apple_m2_m3.so",
		"libggml-cpu-apple_m1.so",
	} {
		p := filepath.Join(libs, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

// transcriptionWorker transcribes voice notes in the background: new ones as
// they arrive, plus any backlog left from before the feature existed. Newest
// first, so a large backlog doesn't delay today's messages.
func (store *MessageStore) transcriptionWorker(client *whatsmeow.Client, logger waLog.Logger) {
	if whisperBinary() == "" {
		fmt.Println("Voice-note transcription is off: the speech-to-text engine is not installed.")
		return
	}
	// The bridge starts at login, often before the network is up, so keep
	// retrying rather than disabling transcription for the process lifetime.
	for !ensureWhisperModel(logger) {
		time.Sleep(5 * time.Minute)
	}
	fmt.Println("Voice-note transcription is on.")

	// Messages that failed this run; retried on the next restart, but not in a
	// tight loop. Media whose URL has expired can never succeed.
	failed := map[string]bool{}

	for {
		n := store.transcribePass(client, logger, failed, 8)
		if n == 0 {
			time.Sleep(30 * time.Second)
		}
	}
}

// transcribePass transcribes up to limit pending voice notes, returning how
// many it completed.
func (store *MessageStore) transcribePass(client *whatsmeow.Client, logger waLog.Logger, failed map[string]bool, limit int) int {
	// Exclude already-failed messages in SQL, not after the fact. Failures stay
	// NULL in the database (so a restart retries them), and a fixed LIMIT would
	// otherwise keep returning the same unrecoverable rows forever while every
	// older voice note behind them is never even looked at.
	q := `SELECT id, chat_jid FROM messages WHERE media_type = 'audio' AND transcription IS NULL`
	params := []any{}
	if len(failed) > 0 {
		keys := make([]string, 0, len(failed))
		for k := range failed {
			keys = append(keys, k)
		}
		q += " AND (chat_jid || '/' || id) NOT IN (" + placeholdersN(len(keys)) + ")"
		for _, k := range keys {
			params = append(params, k)
		}
	}
	q += " ORDER BY timestamp DESC LIMIT ?"
	params = append(params, limit)

	rows, err := store.db.Query(q, params...)
	if err != nil {
		logger.Warnf("transcription query failed: %v", err)
		return 0
	}
	type job struct{ id, chatJID string }
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.chatJID); err != nil {
			continue
		}
		jobs = append(jobs, j)
	}
	rows.Close()

	done := 0
	for _, j := range jobs {
		text, err := store.transcribeMessage(client, j.id, j.chatJID)
		if err != nil {
			failed[j.chatJID+"/"+j.id] = true
			logger.Warnf("could not transcribe %s: %v", j.id, err)
			continue
		}
		done++
		if text != "" {
			preview := []rune(text)
			if len(preview) > 60 {
				preview = preview[:60]
			}
			fmt.Printf("Transcribed voice note: %s…\n", string(preview))
		}
	}
	return done
}

// transcribeMessage downloads (if needed) and transcribes one voice note,
// storing the result. An empty result is stored as "" so it is not retried.
func (store *MessageStore) transcribeMessage(client *whatsmeow.Client, messageID, chatJID string) (string, error) {
	var filename string
	if err := store.db.QueryRow("SELECT filename FROM messages WHERE id = ? AND chat_jid = ?",
		messageID, chatJID).Scan(&filename); err != nil {
		return "", err
	}

	path := filepath.Join(storeDir(), strings.ReplaceAll(chatJID, ":", "_"), mediaFileName(messageID, filename))
	if _, err := os.Stat(path); err != nil {
		// Not fetched yet (or fetched before we stored voice notes eagerly).
		if client == nil {
			return "", fmt.Errorf("audio not downloaded and no WhatsApp connection")
		}
		ok, _, _, absPath, derr := downloadMedia(client, store, messageID, chatJID)
		if derr != nil || !ok {
			return "", fmt.Errorf("download failed: %v", derr)
		}
		path = absPath
	}

	text, err := transcribeAudioFile(path)
	if err != nil {
		return "", err
	}
	if _, err := store.db.Exec(
		"UPDATE messages SET transcription = ? WHERE id = ? AND chat_jid = ?",
		text, messageID, chatJID); err != nil {
		return "", err
	}
	return text, nil
}
