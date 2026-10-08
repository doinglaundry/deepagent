package computer

import (
	"bytes"
	"context"
	"encoding/base64"
	"github.com/chromedp/chromedp"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBrowser_RealActionsAndStaleObservation(t *testing.T) {
	t.Setenv("DEEPAGENT_BROWSER_HEADLESS", "1")
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<input placeholder="Name"><button onclick="document.querySelector('output').textContent=document.querySelector('input').value">Submit</button><output></output><div style="height:2200px">Scroll target</div>`))
	}))
	defer page.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	browser, err := NewBrowser(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close(context.Background())
	observation, err := browser.PerformAction(ctx, "open", Action{URL: page.URL})
	if err != nil {
		t.Fatal(err)
	}
	imageBytes, err := base64.StdEncoding.DecodeString(observation.Image)
	if err != nil {
		t.Fatal(err)
	}
	size, err := png.DecodeConfig(bytes.NewReader(imageBytes))
	if err != nil || size.Width > 1280 || size.Height > 1280 {
		t.Fatalf("invalid screenshot: %v %v", size, err)
	}
	inputID, buttonID := 0, 0
	for _, element := range observation.Elements {
		if element.Role == "input" {
			inputID = element.ID
		}
		if element.Role == "button" {
			buttonID = element.ID
		}
	}
	if inputID == 0 || buttonID == 0 {
		t.Fatalf("missing page elements: url=%s dimensions=%dx%d text=%q elements=%+v", observation.URL, observation.Width, observation.Height, observation.Text, observation.Elements)
	}
	oldID := observation.ID
	observation, err = browser.PerformAction(ctx, "type_text", Action{ObservationID: observation.ID, ElementID: inputID, Text: "DeepAgent"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = browser.PerformAction(ctx, "click", Action{ObservationID: oldID, ElementID: buttonID})
	if err == nil {
		t.Fatal("stale observation executed")
	}
	observation, err = browser.PerformAction(ctx, "click", Action{ObservationID: observation.ID, ElementID: buttonID})
	if err != nil || !strings.Contains(observation.Text, "DeepAgent") {
		t.Fatalf("click did not update page: %+v %v", observation, err)
	}
	observation, err = browser.PerformAction(ctx, "scroll", Action{ObservationID: observation.ID, DeltaY: 600})
	if err != nil {
		t.Fatal(err)
	}
	if observation.ScrollY < 500 {
		t.Fatalf("page did not scroll: %v", observation.ScrollY)
	}
	canceledCtx, stop := context.WithCancel(ctx)
	stop()
	_, err = browser.PerformAction(canceledCtx, "observe", Action{})
	if err == nil {
		t.Fatal("canceled request succeeded")
	}
	_, err = browser.PerformAction(ctx, "observe", Action{})
	if err != nil {
		t.Fatalf("request cancellation closed browser: %v", err)
	}
}

func TestBrowser_OpenRejectsInvalidURL(t *testing.T) {
	t.Setenv("DEEPAGENT_BROWSER_HEADLESS", "1")
	browser, err := NewBrowser(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close(context.Background())
	_, err = browser.PerformAction(context.Background(), "open", Action{URL: "file:///etc/passwd"})
	if err == nil {
		t.Fatal("non-web URL accepted")
	}
}

func TestBrowser_OverlayCannotReceiveApprovedElementClick(t *testing.T) {
	t.Setenv("DEEPAGENT_BROWSER_HEADLESS", "1")
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<button>Approve</button>`))
	}))
	defer page.Close()
	browser, err := NewBrowser(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close(context.Background())
	observation, err := browser.PerformAction(context.Background(), "open", Action{URL: page.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = chromedp.Run(browser.ctx, chromedp.Evaluate(`let overlay=document.createElement('button');overlay.textContent='Unapproved';overlay.style='position:fixed;inset:0;width:100%;height:100%;z-index:99';document.body.append(overlay)`, nil))
	if err != nil {
		t.Fatal(err)
	}
	_, err = browser.PerformAction(context.Background(), "click", Action{URL: observation.URL, ObservationID: observation.ID, ElementID: 1})
	if err == nil || !strings.Contains(err.Error(), "stale_observation") {
		t.Fatalf("overlay was clicked: %v", err)
	}
}
