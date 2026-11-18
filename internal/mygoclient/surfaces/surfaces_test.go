package surfaces

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestClaudeAuthURLExtractionAndValidation(t *testing.T) {
	if !IsClaudeAuthURL("https://claude.ai/oauth/authorize?client_id=abc&state=123") {
		t.Fatal("expected claude.ai oauth URL valid")
	}
	if !IsClaudeAuthURL("https://platform.claude.com/cai/oauth/authorize/?code_challenge=xyz") {
		t.Fatal("expected platform.claude.com oauth URL valid")
	}
	if IsClaudeAuthURL("https://evil.example/oauth/authorize?a=1") {
		t.Fatal("unexpected host accepted")
	}
	if IsClaudeAuthURL("http://claude.ai/oauth/authorize?a=1") {
		t.Fatal("non-https scheme accepted")
	}

	var buf strings.Builder
	chunk := []byte("\x1b[32mOpen browser:\x1b[0m\r\nhttps://claude.ai/oauth/authorize?client_id=test\r\n&response_type=code.\r\n")
	found := ExtractClaudeAuthURL(&buf, chunk)
	want := "https://claude.ai/oauth/authorize?client_id=test&response_type=code"
	if found != want {
		t.Fatalf("extracted URL mismatch: got %q want %q", found, want)
	}
}

func TestAspectFitAndCoordinateMapping(t *testing.T) {
	// Wide box (1000x500) with 4:3 content -> 666.67 x 500 centered horizontally at X=166.67.
	fit := ComputeAspectFitRect(1000, 500, 800, 600)
	if fit.H != 500 || fit.X < 166 || fit.X > 167 || fit.W < 666 || fit.W > 667 {
		t.Fatalf("unexpected aspect fit rect: %+v", fit)
	}

	// Point in left letterbox margin should be out of bounds.
	if _, _, ok := MapPointNormalized(fit, 50, 250); ok {
		t.Fatal("letterbox point should map out of bounds")
	}

	// Center of box maps to ~(0.5, 0.5).
	nx, ny, ok := MapPointNormalized(fit, 500, 250)
	if !ok || nx < 0.49 || nx > 0.51 || ny < 0.49 || ny > 0.51 {
		t.Fatalf("center normalized mismatch: nx=%v ny=%v ok=%v", nx, ny, ok)
	}

	// Bottom-right boundary clamps strictly below 1.0 (macOS server requires < 1).
	nx, ny, ok = MapPointNormalized(fit, fit.X+fit.W, fit.Y+fit.H)
	if !ok || nx >= 1 || ny >= 1 {
		t.Fatalf("expected <1 clamp at edge, got nx=%v ny=%v ok=%v", nx, ny, ok)
	}

	gx, gy, ok := MapPointPixels(fit, fit.X+fit.W/2, fit.Y+fit.H/2, 800, 600)
	if !ok || gx != 400 || gy != 300 {
		t.Fatalf("pixel map mismatch: gx=%d gy=%d ok=%v", gx, gy, ok)
	}
}

func TestMacOSIMETextClientUTF16Lifecycle(t *testing.T) {
	var committed []string
	var imeEvents []string
	client := NewMacOSIMETextClient(
		func(text string) { committed = append(committed, text) },
		func(mode, session, ctxToken, text string, seq, s, e int) {
			imeEvents = append(imeEvents, mode+":"+text)
		},
	)

	// Compose multi-byte CJK / emoji text ("ni😀"), test UTF-16 lengths ("ni"=2, "😀"=2 => 4).
	client.SetMarkedText(nil, "ni😀", ui.TextInputRange{Start: 4, End: 4})
	mr, ok := client.MarkedRange()
	if !ok || mr.Start != 0 || mr.End != 4 {
		t.Fatalf("marked range mismatch: %+v ok=%v", mr, ok)
	}
	sel := client.Selection()
	if sel.Range.Start != 4 || sel.Range.End != 4 {
		t.Fatalf("selection mismatch: %+v", sel)
	}
	txt, actual := client.TextForRange(ui.TextInputRange{Start: 0, End: 10})
	if txt != "ni😀" || actual.End != 4 {
		t.Fatalf("TextForRange mismatch: %q %+v", txt, actual)
	}

	// Commit composition replacement.
	client.ReplaceText(nil, "你好")
	if _, ok := client.MarkedRange(); ok {
		t.Fatal("expected marked range cleared after ReplaceText")
	}
	if len(imeEvents) != 3 || imeEvents[0] != "begin:" || imeEvents[1] != "update:ni😀" || imeEvents[2] != "commit:你好" {
		t.Fatalf("unexpected IME event sequence: %v", imeEvents)
	}
	if len(committed) != 0 {
		t.Fatalf("expected marked commit to use onIME, not direct onCommit: %v", committed)
	}

	// Direct non-marked text commits through onCommit.
	client.ReplaceText(nil, "!")
	if len(committed) != 1 || committed[0] != "!" {
		t.Fatalf("expected direct commit '!', got %v", committed)
	}
}

func TestTranslateUIKey(t *testing.T) {
	code, key := TranslateUIKey(ui.CodeKeyC, ui.KeyC)
	if code != "KeyC" || key != "c" {
		t.Fatalf("expected KeyC/c, got %q/%q", code, key)
	}
	code, key = TranslateUIKey(ui.CodeEnter, ui.KeyEnter)
	if code != "Enter" || key != "Enter" {
		t.Fatalf("expected Enter/Enter, got %q/%q", code, key)
	}
	code, key = TranslateUIKey(ui.CodeUnknown, ui.KeyUp)
	if code != "ArrowUp" || key != "ArrowUp" {
		t.Fatalf("expected fallback ArrowUp, got %q/%q", code, key)
	}
}
