package surfaces

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/sylvinus/agent-vm/internal/macnative"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

// ComputeAspectFitRect returns the letterboxed sub-rectangle inside a box of size (boxW, boxH)
// that preserves the aspect ratio of (contentW, contentH) centered on both axes.
func ComputeAspectFitRect(boxW, boxH, contentW, contentH float32) ui.Rect {
	if boxW <= 0 || boxH <= 0 || contentW <= 0 || contentH <= 0 {
		return ui.Rect{W: boxW, H: boxH}
	}
	scaleX := boxW / contentW
	scaleY := boxH / contentH
	s := scaleX
	if scaleY < s {
		s = scaleY
	}
	w := contentW * s
	h := contentH * s
	return ui.Rect{
		X: (boxW - w) / 2,
		Y: (boxH - h) / 2,
		W: w,
		H: h,
	}
}

// MapPointNormalized maps a local pointer position (px, py) into [0, 0.999999] relative to fitRect.
func MapPointNormalized(fitRect ui.Rect, px, py float32) (float64, float64, bool) {
	if fitRect.W <= 0 || fitRect.H <= 0 {
		return 0, 0, false
	}
	nx := float64(px-fitRect.X) / float64(fitRect.W)
	ny := float64(py-fitRect.Y) / float64(fitRect.H)
	if nx < 0 || ny < 0 || nx > 1 || ny > 1 {
		return 0, 0, false
	}
	const eps = 1e-6
	if nx >= 1 {
		nx = 1 - eps
	}
	if ny >= 1 {
		ny = 1 - eps
	}
	return nx, ny, true
}

// MapPointPixels maps a local pointer position into integer guest image pixel coordinates.
func MapPointPixels(fitRect ui.Rect, px, py float32, imgW, imgH int) (int, int, bool) {
	if imgW <= 0 || imgH <= 0 {
		return 0, 0, false
	}
	nx, ny, ok := MapPointNormalized(fitRect, px, py)
	if !ok {
		return 0, 0, false
	}
	gx := int(math.Floor(nx * float64(imgW)))
	gy := int(math.Floor(ny * float64(imgH)))
	if gx >= imgW {
		gx = imgW - 1
	}
	if gy >= imgH {
		gy = imgH - 1
	}
	return gx, gy, true
}

// MacOSIMETextClient implements ui.TextInputClient for native CJK/dead-key IME input
// over a macOS guest surface.
type MacOSIMETextClient struct {
	mu       sync.Mutex
	text     string
	selStart int
	selEnd   int
	marked   *ui.TextInputRange
	caretBox ui.Rect
	session  string
	seq      int

	onCommit func(text string)
	onIME    func(mode, session, ctxToken, text string, seq, selStart, selEnd int)
}

func NewMacOSIMETextClient(
	onCommit func(text string),
	onIME func(mode, session, ctxToken, text string, seq, selStart, selEnd int),
) *MacOSIMETextClient {
	return &MacOSIMETextClient{
		onCommit: onCommit,
		onIME:    onIME,
	}
}

func (m *MacOSIMETextClient) SetCaretBounds(r ui.Rect) {
	m.mu.Lock()
	m.caretBox = r
	m.mu.Unlock()
}

func (m *MacOSIMETextClient) TextForRange(r ui.TextInputRange) (string, ui.TextInputRange) {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := ui.UTF16Len(m.text)
	start := clampInt(r.Start, 0, total)
	end := clampInt(r.End, start, total)
	b0 := ui.UTF16ByteOffset(m.text, start)
	b1 := ui.UTF16ByteOffset(m.text, end)
	return m.text[b0:b1], ui.TextInputRange{Start: start, End: end}
}

func (m *MacOSIMETextClient) Selection() ui.TextInputSelection {
	m.mu.Lock()
	defer m.mu.Unlock()
	return ui.TextInputSelection{
		Range: ui.TextInputRange{Start: m.selStart, End: m.selEnd},
	}
}

func (m *MacOSIMETextClient) MarkedRange() (ui.TextInputRange, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.marked == nil {
		return ui.TextInputRange{}, false
	}
	return *m.marked, true
}

func (m *MacOSIMETextClient) ReplaceText(r *ui.TextInputRange, s string) {
	m.mu.Lock()
	rng := m.effectiveRangeLocked(r)
	b0 := ui.UTF16ByteOffset(m.text, rng.Start)
	b1 := ui.UTF16ByteOffset(m.text, rng.End)
	m.text = m.text[:b0] + s + m.text[b1:]
	caret := rng.Start + ui.UTF16Len(s)
	m.selStart = caret
	m.selEnd = caret
	hadMarked := m.marked != nil
	session := m.session
	m.seq++
	seq := m.seq
	m.marked = nil
	if len(m.text) > 512 {
		// Keep local shadow buffer small; guest app holds actual document text.
		keep := ui.UTF16Len(s)
		m.text = s
		m.selStart = keep
		m.selEnd = keep
	}
	onIME := m.onIME
	onCommit := m.onCommit
	m.mu.Unlock()

	if hadMarked && onIME != nil && session != "" {
		onIME("commit", session, "", s, seq, 0, ui.UTF16Len(s))
	} else if s != "" && onCommit != nil {
		onCommit(s)
	}
}

func (m *MacOSIMETextClient) SetMarkedText(r *ui.TextInputRange, s string, sel ui.TextInputRange) {
	m.mu.Lock()
	rng := m.effectiveRangeLocked(r)
	b0 := ui.UTF16ByteOffset(m.text, rng.Start)
	b1 := ui.UTF16ByteOffset(m.text, rng.End)
	m.text = m.text[:b0] + s + m.text[b1:]
	n16 := ui.UTF16Len(s)
	started := false
	if m.marked == nil || m.session == "" {
		m.session = uuid.NewString()
		started = true
	}
	mr := ui.TextInputRange{Start: rng.Start, End: rng.Start + n16}
	m.marked = &mr
	m.selStart = rng.Start + clampInt(sel.Start, 0, n16)
	m.selEnd = rng.Start + clampInt(sel.End, 0, n16)
	m.seq++
	seq := m.seq
	session := m.session
	onIME := m.onIME
	m.mu.Unlock()

	if onIME != nil {
		if started {
			onIME("begin", session, "ime", "", 0, 0, 0)
		}
		onIME("update", session, "", s, seq, clampInt(sel.Start, 0, n16), clampInt(sel.End, 0, n16))
	}
}

func (m *MacOSIMETextClient) UnmarkText() {
	m.mu.Lock()
	hadMarked := m.marked != nil
	session := m.session
	m.seq++
	seq := m.seq
	m.marked = nil
	m.session = ""
	onIME := m.onIME
	m.mu.Unlock()

	if hadMarked && onIME != nil && session != "" {
		onIME("cancel", session, "", "", seq, 0, 0)
	}
}

func (m *MacOSIMETextClient) BoundsForRange(r ui.TextInputRange) (ui.Rect, ui.TextInputRange, bool) {
	m.mu.Lock()
	box := m.caretBox
	total := ui.UTF16Len(m.text)
	m.mu.Unlock()
	start := clampInt(r.Start, 0, total)
	end := clampInt(r.End, start, total)
	if box.W <= 0 && box.H <= 0 {
		box = ui.Rect{X: 20, Y: 20, W: 2, H: 18}
	}
	return box, ui.TextInputRange{Start: start, End: end}, true
}

func (m *MacOSIMETextClient) IndexForPoint(ui.Point) (int, bool) {
	m.mu.Lock()
	caret := m.selStart
	m.mu.Unlock()
	return caret, true
}

func (m *MacOSIMETextClient) effectiveRangeLocked(r *ui.TextInputRange) ui.TextInputRange {
	total := ui.UTF16Len(m.text)
	if r != nil {
		s := clampInt(r.Start, 0, total)
		e := clampInt(r.End, s, total)
		return ui.TextInputRange{Start: s, End: e}
	}
	if m.marked != nil {
		s := clampInt(m.marked.Start, 0, total)
		e := clampInt(m.marked.End, s, total)
		return ui.TextInputRange{Start: s, End: e}
	}
	return ui.TextInputRange{Start: clampInt(m.selStart, 0, total), End: clampInt(m.selEnd, 0, total)}
}

type macOSFrameHeader struct {
	ImageLength   int     `json:"imageLength"`
	MIME          string  `json:"mime,omitempty"`
	Sequence      string  `json:"sequence,omitempty"`
	Geometry      string  `json:"geometry,omitempty"`
	Window        uint32  `json:"window,omitempty"`
	PID           int32   `json:"pid,omitempty"`
	ContentWidth  float64 `json:"contentWidth,omitempty"`
	ContentHeight float64 `json:"contentHeight,omitempty"`
	PixelWidth    int     `json:"pixelWidth,omitempty"`
	PixelHeight   int     `json:"pixelHeight,omitempty"`
	Unchanged     bool    `json:"unchanged,omitempty"`
	Error         string  `json:"error,omitempty"`
	Revision      uint64  `json:"revision,omitempty"`
}

type macOSWindowRequest struct {
	Surface  string   `json:"surface,omitempty"`
	Action   string   `json:"action"`
	Width    int      `json:"width,omitempty"`
	Height   int      `json:"height,omitempty"`
	Scale    float64  `json:"scale,omitempty"`
	Geometry string   `json:"geometry,omitempty"`
	Window   uint32   `json:"window,omitempty"`
	PID      int32    `json:"pid,omitempty"`
	X        *float64 `json:"x,omitempty"`
	Y        *float64 `json:"y,omitempty"`
	Delta    *float64 `json:"delta,omitempty"`
	DeltaX   *float64 `json:"deltaX,omitempty"`
	Text     string   `json:"text,omitempty"`
	Key      string   `json:"key,omitempty"`
	Code     string   `json:"code,omitempty"`
	Down     *bool    `json:"down,omitempty"`
	Phase    string   `json:"phase,omitempty"`
	Button   *int     `json:"button,omitempty"`
	Buttons  int      `json:"buttons,omitempty"`
	Clicks   int      `json:"clicks,omitempty"`
	Shift    bool     `json:"shift,omitempty"`
	Cmd      bool     `json:"command,omitempty"`
	Control  bool     `json:"control,omitempty"`
	Alt      bool     `json:"alt,omitempty"`
	Mode     string   `json:"mode,omitempty"`
	Context  string   `json:"context,omitempty"`
	Session  string   `json:"session,omitempty"`
	Sequence int      `json:"sequence,omitempty"`
}

// MacOSWindowSurface renders and drives a live macOS Guest window (/api/macos-browser/* or /api/macos/claude/*).
type MacOSWindowSurface struct {
	client     *core.ControllerClient
	browser    bool
	invalidate func()

	mu          sync.Mutex
	active      bool
	cancel      context.CancelFunc
	bitmap      *ui.Bitmap
	header      macOSFrameHeader
	lastError   string
	reqW        int
	reqH        int
	reqScale    float64
	revision    uint64
	sizeChanged chan struct{}
	buttonsMask int

	connMu   sync.Mutex
	connWS   *websocket.Conn
	rpcSeq   uint64
	pending  map[uint64]chan json.RawMessage
	ime      *MacOSIMETextClient
	boxRect  ui.Rect
	dragging bool
}

func NewMacOSWindowSurface(client *core.ControllerClient, browser bool, invalidate func()) *MacOSWindowSurface {
	s := &MacOSWindowSurface{
		client:      client,
		browser:     browser,
		invalidate:  invalidate,
		reqW:        1280,
		reqH:        800,
		reqScale:    2,
		revision:    1,
		sizeChanged: make(chan struct{}, 1),
		pending:     map[uint64]chan json.RawMessage{},
	}
	s.ime = NewMacOSIMETextClient(
		func(text string) {
			s.sendText(text)
		},
		func(mode, session, ctxToken, text string, seq, _, _ int) {
			s.sendIME(mode, session, ctxToken, text, seq)
		},
	)
	return s
}

func (s *MacOSWindowSurface) basePath() string {
	if s.browser {
		return "/api/macos-browser"
	}
	return "/api/macos/claude"
}

func (s *MacOSWindowSurface) EnsureRunning(parent context.Context, shouldRun bool) {
	s.mu.Lock()
	if shouldRun == s.active {
		s.mu.Unlock()
		return
	}
	if !shouldRun {
		cancel := s.cancel
		s.cancel = nil
		s.active = false
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.active = true
	s.mu.Unlock()

	go s.connectionLoop(ctx)
	go s.framesLoop(ctx)
}

func (s *MacOSWindowSurface) Stop() {
	s.EnsureRunning(context.Background(), false)
}

func (s *MacOSWindowSurface) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastError
}

func (s *MacOSWindowSurface) Header() macOSFrameHeader {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.header
}

func (s *MacOSWindowSurface) SetTargetSize(w, h int, scale float64) {
	w = clampInt(w, 320, 2560)
	h = clampInt(h, 240, 1600)
	if scale < 1 {
		scale = 1
	}
	if scale > 2 {
		scale = 2
	}
	s.mu.Lock()
	changed := w != s.reqW || h != s.reqH || scale != s.reqScale
	if changed {
		s.reqW = w
		s.reqH = h
		s.reqScale = scale
		s.revision++
	}
	s.mu.Unlock()
	if changed {
		select {
		case s.sizeChanged <- struct{}{}:
		default:
		}
	}
}

func (s *MacOSWindowSurface) SendWindowAction(ctx context.Context, action string) (json.RawMessage, error) {
	return s.rpc(ctx, macOSWindowRequest{Action: action})
}

func (s *MacOSWindowSurface) connectionLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		ws, _, err := s.client.DialWebSocket(ctx, s.basePath()+"/connection", false)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(1500 * time.Millisecond):
				continue
			}
		}
		s.connMu.Lock()
		s.connWS = ws
		s.connMu.Unlock()

		stopWatch := context.AfterFunc(ctx, func() { _ = ws.Close() })
		for {
			var resp struct {
				ID     uint64          `json:"id"`
				Result json.RawMessage `json:"result"`
			}
			if err := ws.ReadJSON(&resp); err != nil {
				break
			}
			s.connMu.Lock()
			ch := s.pending[resp.ID]
			delete(s.pending, resp.ID)
			s.connMu.Unlock()
			if ch != nil {
				ch <- resp.Result
				close(ch)
			}
		}
		stopWatch()
		_ = ws.Close()
		s.connMu.Lock()
		if s.connWS == ws {
			s.connWS = nil
		}
		for id, ch := range s.pending {
			close(ch)
			delete(s.pending, id)
		}
		s.connMu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}

func (s *MacOSWindowSurface) framesLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		ws, _, err := s.client.DialWebSocket(ctx, s.basePath()+"/frames", false)
		if err != nil {
			s.mu.Lock()
			s.lastError = err.Error()
			s.mu.Unlock()
			if s.invalidate != nil {
				s.invalidate()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(1500 * time.Millisecond):
				continue
			}
		}
		s.mu.Lock()
		s.lastError = ""
		s.revision++
		s.mu.Unlock()

		stopWatch := context.AfterFunc(ctx, func() { _ = ws.Close() })
		subCtx, subCancel := context.WithCancel(ctx)
		var wsWriteMu sync.Mutex
		sendSub := func() error {
			s.mu.Lock()
			sub := map[string]any{
				"width":    s.reqW,
				"height":   s.reqH,
				"scale":    s.reqScale,
				"revision": s.revision,
			}
			s.mu.Unlock()
			wsWriteMu.Lock()
			defer wsWriteMu.Unlock()
			_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
			return ws.WriteJSON(sub)
		}
		_ = sendSub()
		go func() {
			for {
				select {
				case <-subCtx.Done():
					return
				case <-s.sizeChanged:
					_ = sendSub()
				}
			}
		}()

		for {
			msgType, data, err := ws.ReadMessage()
			if err != nil {
				break
			}
			if msgType != websocket.BinaryMessage || len(data) < 4 {
				continue
			}
			metaLen := int(binary.BigEndian.Uint32(data[:4]))
			if metaLen <= 0 || 4+metaLen > len(data) {
				continue
			}
			var hdr macOSFrameHeader
			if json.Unmarshal(data[4:4+metaLen], &hdr) != nil {
				continue
			}
			pixels := data[4+metaLen:]
			if len(pixels) > 0 {
				if bmp, err := ui.DecodeBitmap(pixels); err == nil {
					s.mu.Lock()
					s.bitmap = bmp
					s.header = hdr
					s.lastError = hdr.Error
					s.mu.Unlock()
					if s.invalidate != nil {
						s.invalidate()
					}
				}
			} else if hdr.Error != "" {
				s.mu.Lock()
				s.header = hdr
				s.lastError = hdr.Error
				s.mu.Unlock()
				if s.invalidate != nil {
					s.invalidate()
				}
			}
			if hdr.Sequence != "" {
				wsWriteMu.Lock()
				_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = ws.WriteJSON(map[string]string{"ack": hdr.Sequence})
				wsWriteMu.Unlock()
			}
		}
		subCancel()
		stopWatch()
		_ = ws.Close()

		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
	}
}

func (s *MacOSWindowSurface) rpc(ctx context.Context, req macOSWindowRequest) (json.RawMessage, error) {
	s.connMu.Lock()
	ws := s.connWS
	if ws != nil {
		s.rpcSeq++
		id := s.rpcSeq
		ch := make(chan json.RawMessage, 1)
		s.pending[id] = ch
		_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
		err := ws.WriteJSON(struct {
			ID      uint64             `json:"id"`
			Request macOSWindowRequest `json:"request"`
		}{ID: id, Request: req})
		s.connMu.Unlock()
		if err == nil {
			select {
			case raw, ok := <-ch:
				if !ok {
					return nil, errors.New("客机窗口连接已断开")
				}
				return raw, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		s.connMu.Lock()
		delete(s.pending, id)
		s.connMu.Unlock()
	} else {
		s.connMu.Unlock()
	}

	var out json.RawMessage
	err := s.client.PostJSON(ctx, s.basePath()+"/window", req, &out)
	return out, err
}

func (s *MacOSWindowSurface) sendAsync(req macOSWindowRequest) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		_, _ = s.rpc(ctx, req)
	}()
}

func (s *MacOSWindowSurface) sendText(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	hdr := s.header
	s.mu.Unlock()
	s.sendAsync(macOSWindowRequest{
		Action:   "text",
		Text:     text,
		Window:   hdr.Window,
		PID:      hdr.PID,
		Geometry: hdr.Geometry,
	})
}

func (s *MacOSWindowSurface) sendIME(mode, session, ctxToken, text string, seq int) {
	s.mu.Lock()
	hdr := s.header
	s.mu.Unlock()
	if hdr.PID <= 0 || hdr.Window == 0 || hdr.Geometry == "" {
		return
	}
	s.sendAsync(macOSWindowRequest{
		Action:   "ime",
		Mode:     mode,
		Session:  session,
		Context:  ctxToken,
		Text:     text,
		Sequence: seq,
		Window:   hdr.Window,
		PID:      hdr.PID,
		Geometry: hdr.Geometry,
	})
}

func (s *MacOSWindowSurface) View(c *ui.Context) ui.Element {
	s.mu.Lock()
	bmp := s.bitmap
	hdr := s.header
	s.mu.Unlock()

	if bmp == nil {
		return ui.Column(c).Fill().Center().Background(ui.Hex("#f3f4f6")).Children(func() {
			ui.Spinner(c)
			ui.Text(c, "正在连接 macOS 客机画面…").Margin(8, 0, 0, 0).TextColor(ui.Hex("#6b7280"))
		})
	}

	// Place inside a neutral uniform background container; capture layout bounds for coordinate mapping.
	box := ui.Box(c).Fill().Background(ui.Hex("#eef0f4")).Focusable().InputMethod(true).HandleTextInput(s.ime)
	box.HandleInput(func(ev ui.InputEvent) bool {
		return s.handleInput(ev)
	})
	el := box.Children(func() {
		ui.Image(c, bmp).Fit(ui.Contain).Fill()
	})
	b := el.Bounds()
	if b.W >= 320 && b.H >= 240 {
		s.mu.Lock()
		s.boxRect = b
		s.mu.Unlock()
		s.SetTargetSize(int(b.W), int(b.H), 2)
		cw, ch := float32(hdr.PixelWidth), float32(hdr.PixelHeight)
		if hdr.ContentWidth > 0 && hdr.ContentHeight > 0 {
			cw, ch = float32(hdr.ContentWidth), float32(hdr.ContentHeight)
		}
		fit := ComputeAspectFitRect(b.W, b.H, cw, ch)
		s.ime.SetCaretBounds(ui.Rect{X: fit.X + 24, Y: fit.Y + 24, W: 2, H: 18})
	}
	return el
}

func (s *MacOSWindowSurface) handleInput(ev ui.InputEvent) bool {
	s.mu.Lock()
	hdr := s.header
	box := s.boxRect
	s.mu.Unlock()

	if hdr.PID <= 0 || hdr.Window == 0 || hdr.Geometry == "" {
		return false
	}
	cw, ch := float32(hdr.PixelWidth), float32(hdr.PixelHeight)
	if hdr.ContentWidth > 0 && hdr.ContentHeight > 0 {
		cw, ch = float32(hdr.ContentWidth), float32(hdr.ContentHeight)
	}
	fit := ComputeAspectFitRect(box.W, box.H, cw, ch)

	switch ev.Kind {
	case ui.InputPointerDown, ui.InputPointerMove, ui.InputPointerUp:
		nx, ny, ok := MapPointNormalized(fit, ev.X, ev.Y)
		if !ok {
			if ev.Kind == ui.InputPointerUp {
				s.mu.Lock()
				s.buttonsMask &^= (1 << uint(clampInt(ev.Button, 0, 2)))
				s.dragging = false
				s.mu.Unlock()
			}
			return false
		}
		btn := clampInt(ev.Button, 0, 2)
		s.mu.Lock()
		phase := "move"
		if ev.Kind == ui.InputPointerDown {
			phase = "down"
			s.buttonsMask |= (1 << uint(btn))
			s.dragging = true
		} else if ev.Kind == ui.InputPointerUp {
			phase = "up"
			s.buttonsMask &^= (1 << uint(btn))
			s.dragging = false
		}
		mask := s.buttonsMask
		s.mu.Unlock()

		clicks := ev.Clicks
		if clicks < 1 {
			clicks = 1
		}
		if clicks > 3 {
			clicks = 3
		}
		req := macOSWindowRequest{
			Action:   "pointer",
			Phase:    phase,
			X:        &nx,
			Y:        &ny,
			Buttons:  mask & 7,
			Window:   hdr.Window,
			PID:      hdr.PID,
			Geometry: hdr.Geometry,
			Shift:    ev.Mods&ui.Shift != 0,
			Cmd:      ev.Mods&ui.Super != 0,
			Control:  ev.Mods&ui.Ctrl != 0,
			Alt:      ev.Mods&ui.Alt != 0,
		}
		if phase != "move" {
			req.Button = &btn
			req.Clicks = clicks
		}
		s.sendAsync(req)
		return true

	case ui.InputScroll:
		nx, ny, ok := MapPointNormalized(fit, ev.X, ev.Y)
		if !ok {
			return false
		}
		dx, dy := float64(-ev.DX), float64(-ev.DY)
		if !ev.Precise {
			dy *= 24
			dx *= 24
		}
		s.sendAsync(macOSWindowRequest{
			Action:   "scroll",
			X:        &nx,
			Y:        &ny,
			DeltaX:   &dx,
			Delta:    &dy,
			Window:   hdr.Window,
			PID:      hdr.PID,
			Geometry: hdr.Geometry,
		})
		return true

	case ui.InputText:
		if ev.Text != "" {
			s.sendText(ev.Text)
			return true
		}

	case ui.InputCommand:
		switch ev.Text {
		case "copy":
			s.sendKeyChord(hdr, "KeyC", "c", true, false)
			return true
		case "cut":
			s.sendKeyChord(hdr, "KeyX", "x", true, false)
			return true
		case "paste", "pasteAndMatchStyle":
			s.sendKeyChord(hdr, "KeyV", "v", true, false)
			return true
		case "selectAll":
			s.sendKeyChord(hdr, "KeyA", "a", true, false)
			return true
		case "undo":
			s.sendKeyChord(hdr, "KeyZ", "z", true, false)
			return true
		case "redo":
			s.sendKeyChord(hdr, "KeyZ", "z", true, true)
			return true
		}

	case ui.InputKeyDown, ui.InputKeyUp:
		codeStr, keyStr := TranslateUIKey(ev.Code, ev.Key)
		if codeStr == "" && keyStr == "" {
			return false
		}
		down := ev.Kind == ui.InputKeyDown
		s.sendAsync(macOSWindowRequest{
			Action:   "key",
			Code:     codeStr,
			Key:      keyStr,
			Down:     &down,
			Shift:    ev.Mods&ui.Shift != 0,
			Cmd:      ev.Mods&ui.Super != 0,
			Control:  ev.Mods&ui.Ctrl != 0,
			Alt:      ev.Mods&ui.Alt != 0,
			Window:   hdr.Window,
			PID:      hdr.PID,
			Geometry: hdr.Geometry,
		})
		return true
	}
	return false
}

func (s *MacOSWindowSurface) sendKeyChord(hdr macOSFrameHeader, code, key string, cmd, shift bool) {
	down, up := true, false
	s.sendAsync(macOSWindowRequest{
		Action: "key", Code: code, Key: key, Down: &down, Cmd: cmd, Shift: shift,
		Window: hdr.Window, PID: hdr.PID, Geometry: hdr.Geometry,
	})
	s.sendAsync(macOSWindowRequest{
		Action: "key", Code: code, Key: key, Down: &up, Cmd: cmd, Shift: shift,
		Window: hdr.Window, PID: hdr.PID, Geometry: hdr.Geometry,
	})
}

// MacOSLoginSurface renders /api/macos/{first-setup,login,maintenance} interactive snapshots.
type MacOSLoginSurface struct {
	client      *core.ControllerClient
	endpoint    string
	maintenance string
	invalidate  func()

	mu            sync.Mutex
	active        bool
	cancel        context.CancelFunc
	session       string
	bitmap        *ui.Bitmap
	imgW, imgH    int
	geometry      string
	administrator string
	recovery      string
	lastError     string
	closed        bool
	boxRect       ui.Rect
}

func NewMacOSLoginSurface(client *core.ControllerClient, endpoint, maintenance string, invalidate func()) *MacOSLoginSurface {
	return &MacOSLoginSurface{
		client:      client,
		endpoint:    endpoint,
		maintenance: maintenance,
		invalidate:  invalidate,
	}
}

func (s *MacOSLoginSurface) Open(parent context.Context) error {
	s.Stop()
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.cancel = cancel
	s.active = true
	s.closed = false
	s.lastError = ""
	s.mu.Unlock()

	reply, err := s.post(ctx, map[string]any{
		"action":      "open",
		"maintenance": s.maintenance,
	})
	if err != nil {
		s.mu.Lock()
		s.lastError = err.Error()
		s.active = false
		s.mu.Unlock()
		cancel()
		if s.invalidate != nil {
			s.invalidate()
		}
		return err
	}
	s.applyReply(reply)
	go s.pollFrames(ctx)
	return nil
}

func (s *MacOSLoginSurface) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	sess := s.session
	active := s.active
	s.cancel = nil
	s.active = false
	s.session = ""
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if active && sess != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = s.post(ctx, map[string]any{
				"action":      "close",
				"session":     sess,
				"maintenance": s.maintenance,
			})
		}()
	}
}

func (s *MacOSLoginSurface) FillPassword(ctx context.Context) error {
	s.mu.Lock()
	sess := s.session
	geom := s.geometry
	s.mu.Unlock()
	reply, err := s.post(ctx, map[string]any{
		"action":      "fill-password",
		"session":     sess,
		"geometry":    geom,
		"maintenance": s.maintenance,
	})
	if err != nil {
		return err
	}
	s.applyReply(reply)
	return nil
}

func (s *MacOSLoginSurface) SelectAll(ctx context.Context) error {
	s.mu.Lock()
	sess := s.session
	geom := s.geometry
	s.mu.Unlock()
	reply, err := s.post(ctx, map[string]any{
		"action":      "select-all",
		"session":     sess,
		"geometry":    geom,
		"maintenance": s.maintenance,
	})
	if err != nil {
		return err
	}
	s.applyReply(reply)
	return nil
}

func (s *MacOSLoginSurface) State() (closed bool, admin, recovery, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed, s.administrator, s.recovery, s.lastError
}

func (s *MacOSLoginSurface) post(ctx context.Context, body map[string]any) (struct {
	MIME          string `json:"mime"`
	Session       string `json:"session"`
	Image         []byte `json:"image"`
	Error         string `json:"error"`
	Closed        bool   `json:"closed"`
	Geometry      string `json:"geometry"`
	Region        []int  `json:"region"`
	Recovery      string `json:"recovery"`
	Administrator string `json:"administrator"`
}, error) {
	var reply struct {
		MIME          string `json:"mime"`
		Session       string `json:"session"`
		Image         []byte `json:"image"`
		Error         string `json:"error"`
		Closed        bool   `json:"closed"`
		Geometry      string `json:"geometry"`
		Region        []int  `json:"region"`
		Recovery      string `json:"recovery"`
		Administrator string `json:"administrator"`
	}
	err := s.client.PostJSON(ctx, s.endpoint, body, &reply)
	return reply, err
}

func (s *MacOSLoginSurface) applyReply(reply struct {
	MIME          string `json:"mime"`
	Session       string `json:"session"`
	Image         []byte `json:"image"`
	Error         string `json:"error"`
	Closed        bool   `json:"closed"`
	Geometry      string `json:"geometry"`
	Region        []int  `json:"region"`
	Recovery      string `json:"recovery"`
	Administrator string `json:"administrator"`
}) {
	var bmp *ui.Bitmap
	var w, h int
	if len(reply.Image) > 0 {
		if decoded, err := ui.DecodeBitmap(reply.Image); err == nil {
			bmp = decoded
			fw, fh := ui.ImageSize(decoded)
			w, h = int(fw), int(fh)
		}
	}
	s.mu.Lock()
	if reply.Session != "" {
		s.session = reply.Session
	}
	if reply.Geometry != "" {
		s.geometry = reply.Geometry
	}
	if len(reply.Region) >= 4 {
		if reply.Region[2] > 0 && reply.Region[3] > 0 {
			w, h = reply.Region[2], reply.Region[3]
		}
	}
	if bmp != nil {
		s.bitmap = bmp
		if w > 0 && h > 0 {
			s.imgW, s.imgH = w, h
		}
	}
	s.closed = reply.Closed
	if reply.Administrator != "" {
		s.administrator = reply.Administrator
	}
	if reply.Recovery != "" {
		s.recovery = reply.Recovery
	}
	if reply.Error != "" {
		s.lastError = reply.Error
	}
	s.mu.Unlock()
	if s.invalidate != nil {
		s.invalidate()
	}
}

func (s *MacOSLoginSurface) pollFrames(ctx context.Context) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			sess := s.session
			active := s.active && !s.closed
			s.mu.Unlock()
			if !active || sess == "" {
				return
			}
			cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			reply, err := s.post(cctx, map[string]any{
				"action":      "frame",
				"session":     sess,
				"maintenance": s.maintenance,
			})
			cancel()
			if err != nil {
				continue
			}
			s.applyReply(reply)
			if reply.Closed {
				return
			}
		}
	}
}

func (s *MacOSLoginSurface) View(c *ui.Context) ui.Element {
	s.mu.Lock()
	bmp := s.bitmap
	imgW, imgH := s.imgW, s.imgH
	sess := s.session
	geom := s.geometry
	s.mu.Unlock()

	if bmp == nil {
		return ui.Column(c).Fill().Center().Background(ui.Hex("#f3f4f6")).Children(func() {
			ui.Spinner(c)
			ui.Text(c, "正在加载 macOS 登录画面…").Margin(8, 0, 0, 0).TextColor(ui.Hex("#6b7280"))
		})
	}

	box := ui.Box(c).Fill().Background(ui.Hex("#111827")).Focusable().HandleInput(func(ev ui.InputEvent) bool {
		s.mu.Lock()
		b := s.boxRect
		s.mu.Unlock()
		fit := ComputeAspectFitRect(b.W, b.H, float32(imgW), float32(imgH))

		switch ev.Kind {
		case ui.InputPointerDown:
			gx, gy, ok := MapPointPixels(fit, ev.X, ev.Y, imgW, imgH)
			if !ok {
				return false
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				reply, err := s.post(ctx, map[string]any{
					"action":      "click",
					"session":     sess,
					"geometry":    geom,
					"maintenance": s.maintenance,
					"x":           gx,
					"y":           gy,
				})
				if err == nil {
					s.applyReply(reply)
				}
			}()
			return true
		case ui.InputScroll:
			gx, gy, ok := MapPointPixels(fit, ev.X, ev.Y, imgW, imgH)
			if !ok {
				return false
			}
			dx, dy := int(-ev.DX), int(-ev.DY)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				reply, err := s.post(ctx, map[string]any{
					"action":      "scroll",
					"session":     sess,
					"geometry":    geom,
					"maintenance": s.maintenance,
					"x":           gx,
					"y":           gy,
					"delta_x":     dx,
					"delta_y":     dy,
				})
				if err == nil {
					s.applyReply(reply)
				}
			}()
			return true
		case ui.InputKeyDown:
			codeStr, keyStr := TranslateUIKey(ev.Code, ev.Key)
			vk, ok := macnative.VirtualKey(codeStr, keyStr, false)
			if !ok {
				return false
			}
			shift := ev.Mods&ui.Shift != 0
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				reply, err := s.post(ctx, map[string]any{
					"action":      "key",
					"session":     sess,
					"geometry":    geom,
					"maintenance": s.maintenance,
					"code":        int(vk),
					"shift":       shift,
				})
				if err == nil {
					s.applyReply(reply)
				}
			}()
			return true
		}
		return false
	})

	el := box.Children(func() {
		ui.Image(c, bmp).Fit(ui.Contain).Fill()
	})
	b := el.Bounds()
	if b.W > 0 && b.H > 0 {
		s.mu.Lock()
		s.boxRect = b
		s.mu.Unlock()
	}
	return el
}

// TranslateUIKey converts MyGo ui.KeyCode / ui.Key into W3C KeyboardEvent code & key strings.
func TranslateUIKey(code ui.KeyCode, key ui.Key) (string, string) {
	if code >= ui.CodeKeyA && code <= ui.CodeKeyZ {
		ch := string(rune('A' + (code - ui.CodeKeyA)))
		return "Key" + ch, string(rune('a' + (code - ui.CodeKeyA)))
	}
	if code >= ui.CodeDigit1 && code <= ui.CodeDigit9 {
		ch := string(rune('1' + (code - ui.CodeDigit1)))
		return "Digit" + ch, ch
	}
	if code == ui.CodeDigit0 {
		return "Digit0", "0"
	}
	switch code {
	case ui.CodeEnter:
		return "Enter", "Enter"
	case ui.CodeEscape:
		return "Escape", "Escape"
	case ui.CodeBackspace:
		return "Backspace", "Backspace"
	case ui.CodeTab:
		return "Tab", "Tab"
	case ui.CodeSpace:
		return "Space", " "
	case ui.CodeMinus:
		return "Minus", "-"
	case ui.CodeEqual:
		return "Equal", "="
	case ui.CodeBracketLeft:
		return "BracketLeft", "["
	case ui.CodeBracketRight:
		return "BracketRight", "]"
	case ui.CodeBackslash:
		return "Backslash", `\`
	case ui.CodeSemicolon:
		return "Semicolon", ";"
	case ui.CodeQuote:
		return "Quote", "'"
	case ui.CodeBackquote:
		return "Backquote", "`"
	case ui.CodeComma:
		return "Comma", ","
	case ui.CodePeriod:
		return "Period", "."
	case ui.CodeSlash:
		return "Slash", "/"
	case ui.CodeArrowLeft:
		return "ArrowLeft", "ArrowLeft"
	case ui.CodeArrowRight:
		return "ArrowRight", "ArrowRight"
	case ui.CodeArrowUp:
		return "ArrowUp", "ArrowUp"
	case ui.CodeArrowDown:
		return "ArrowDown", "ArrowDown"
	case ui.CodeHome:
		return "Home", "Home"
	case ui.CodeEnd:
		return "End", "End"
	case ui.CodePageUp:
		return "PageUp", "PageUp"
	case ui.CodePageDown:
		return "PageDown", "PageDown"
	case ui.CodeDelete:
		return "Delete", "Delete"
	case ui.CodeInsert:
		return "Insert", "Insert"
	}
	if code >= ui.CodeF1 && code <= ui.CodeF12 {
		n := int(code-ui.CodeF1) + 1
		name := fmt.Sprintf("F%d", n)
		return name, name
	}
	switch key {
	case ui.KeyEnter:
		return "Enter", "Enter"
	case ui.KeyEscape:
		return "Escape", "Escape"
	case ui.KeyBackspace:
		return "Backspace", "Backspace"
	case ui.KeyTab:
		return "Tab", "Tab"
	case ui.KeySpace:
		return "Space", " "
	case ui.KeyLeft:
		return "ArrowLeft", "ArrowLeft"
	case ui.KeyRight:
		return "ArrowRight", "ArrowRight"
	case ui.KeyUp:
		return "ArrowUp", "ArrowUp"
	case ui.KeyDown:
		return "ArrowDown", "ArrowDown"
	case ui.KeyDelete:
		return "Delete", "Delete"
	case ui.KeyHome:
		return "Home", "Home"
	case ui.KeyEnd:
		return "End", "End"
	case ui.KeyPageUp:
		return "PageUp", "PageUp"
	case ui.KeyPageDown:
		return "PageDown", "PageDown"
	}
	if key >= ui.KeyA && key <= ui.KeyZ {
		ch := string(rune('a' + (key - ui.KeyA)))
		upper := string(rune('A' + (key - ui.KeyA)))
		return "Key" + upper, ch
	}
	if key >= ui.Key0 && key <= ui.Key9 {
		ch := string(rune('0' + (key - ui.Key0)))
		return "Digit" + ch, ch
	}
	return "", ""
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
