package core

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"sync"
	"time"

	"github.com/egoist/mygo/audio"
	"github.com/gorilla/websocket"
)

const (
	AudioSampleRate = 48000
	AudioChannels   = 2
	// Keep at most 250ms of queued audio so guest sound never falls noticeably behind video.
	audioRingCapacity = AudioSampleRate * AudioChannels * 2 // 2s backing array
	audioMaxQueued    = AudioSampleRate * AudioChannels / 4 // 250ms latency cap
)

// PCMDecoder implements audio.Decoder over a lock-protected float32 ring buffer
// fed by 48kHz stereo signed 16-bit little-endian PCM packets from /api/audio.
type PCMDecoder struct {
	mu     sync.Mutex
	buf    []float32
	head   int
	count  int
	muted  bool
	closed bool
}

func NewPCMDecoder() *PCMDecoder {
	return &PCMDecoder{
		buf: make([]float32, audioRingCapacity),
	}
}

func (d *PCMDecoder) SampleRate() int { return AudioSampleRate }
func (d *PCMDecoder) Channels() int   { return AudioChannels }
func (d *PCMDecoder) Len() int64      { return -1 }

func (d *PCMDecoder) SetPosition(int64) error {
	return nil
}

func (d *PCMDecoder) SetMuted(muted bool) {
	d.mu.Lock()
	d.muted = muted
	if muted {
		d.head = 0
		d.count = 0
	}
	d.mu.Unlock()
}

func (d *PCMDecoder) Close() {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
}

func (d *PCMDecoder) Reset() {
	d.mu.Lock()
	d.head = 0
	d.count = 0
	d.closed = false
	d.mu.Unlock()
}

func (d *PCMDecoder) QueuedSamples() int {
	d.mu.Lock()
	n := d.count
	d.mu.Unlock()
	return n
}

func (d *PCMDecoder) WritePCM16LE(pcm []byte) {
	samples := len(pcm) / 2
	if samples == 0 {
		return
	}
	// Align to whole stereo frames.
	samples &^= 1
	if samples == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.muted {
		return
	}
	for i := 0; i < samples; i++ {
		raw := int16(binary.LittleEndian.Uint16(pcm[i*2 : i*2+2]))
		val := float32(raw) / 32768.0
		if d.count == len(d.buf) {
			d.head = (d.head + 1) % len(d.buf)
			d.count--
		}
		tail := (d.head + d.count) % len(d.buf)
		d.buf[tail] = val
		d.count++
	}
	// Trim excess latency down to target max queued while staying frame-aligned.
	if d.count > audioMaxQueued {
		drop := (d.count - audioMaxQueued) &^ 1
		d.head = (d.head + drop) % len(d.buf)
		d.count -= drop
	}
}

func (d *PCMDecoder) Read(out []float32) (int, error) {
	n := len(out) &^ 1
	if n == 0 {
		return 0, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed && d.count == 0 {
		return 0, io.EOF
	}
	if d.muted {
		clear(out[:n])
		return n, nil
	}
	avail := d.count &^ 1
	take := avail
	if take > n {
		take = n
	}
	for i := 0; i < take; i++ {
		out[i] = d.buf[d.head]
		d.head = (d.head + 1) % len(d.buf)
	}
	d.count -= take
	if take < n {
		clear(out[take:n])
	}
	return n, nil
}

// AudioStream manages the background WebSocket /api/audio reader and mygo audio.Player.
type AudioStream struct {
	client *ControllerClient

	mu      sync.Mutex
	decoder *PCMDecoder
	player  *audio.Player
	cancel  context.CancelFunc
	active  bool
	muted   bool
	volume  float64
}

func NewAudioStream(client *ControllerClient) *AudioStream {
	audio.SetLatency(audio.LatencyInteractive)
	return &AudioStream{
		client: client,
		volume: 1.0,
	}
}

func (a *AudioStream) SetMuted(muted bool) {
	a.mu.Lock()
	a.muted = muted
	dec := a.decoder
	pl := a.player
	vol := a.volume
	a.mu.Unlock()
	if dec != nil {
		dec.SetMuted(muted)
	}
	if pl != nil {
		if muted {
			pl.SetVolume(0)
		} else {
			pl.SetVolume(vol)
		}
	}
}

func (a *AudioStream) SetVolume(v float64) {
	if math.IsNaN(v) || v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	a.mu.Lock()
	a.volume = v
	pl := a.player
	muted := a.muted
	a.mu.Unlock()
	if pl != nil && !muted {
		pl.SetVolume(v)
	}
}

func (a *AudioStream) EnsureRunning(parent context.Context, shouldRun bool) {
	a.mu.Lock()
	currentlyActive := a.active
	if shouldRun == currentlyActive {
		a.mu.Unlock()
		return
	}
	if !shouldRun {
		cancel := a.cancel
		pl := a.player
		dec := a.decoder
		a.cancel = nil
		a.player = nil
		a.decoder = nil
		a.active = false
		a.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if dec != nil {
			dec.Close()
		}
		if pl != nil {
			pl.Close()
		}
		return
	}

	ctx, cancel := context.WithCancel(parent)
	dec := NewPCMDecoder()
	dec.SetMuted(a.muted)
	pl := audio.NewPlayer(dec)
	if a.muted {
		pl.SetVolume(0)
	} else {
		pl.SetVolume(a.volume)
	}
	pl.Play()

	a.cancel = cancel
	a.decoder = dec
	a.player = pl
	a.active = true
	a.mu.Unlock()

	go a.readLoop(ctx, dec)
}

func (a *AudioStream) Stop() {
	a.EnsureRunning(context.Background(), false)
}

func (a *AudioStream) readLoop(ctx context.Context, dec *PCMDecoder) {
	for {
		if ctx.Err() != nil {
			return
		}
		ws, _, err := a.client.DialWebSocket(ctx, "/api/audio", true)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		stopWatch := context.AfterFunc(ctx, func() {
			_ = ws.Close()
		})
		for {
			msgType, data, err := ws.ReadMessage()
			if err != nil {
				break
			}
			if msgType == websocket.BinaryMessage && len(data) > 0 {
				dec.WritePCM16LE(data)
			}
		}
		stopWatch()
		_ = ws.Close()
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}
