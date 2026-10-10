// Package computer implements real browser and Mac operations without an agent loop.
package computer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	agentmodel "eino-cli/deepagent/model"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/google/uuid"
)

var ErrOutcomeUnknown = errors.New("computer action outcome is unknown; do not retry automatically")

type Browser struct {
	closeOnce       sync.Once
	mu              sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	cancelAllocator context.CancelFunc
	observation     *agentmodel.ComputerObservation
	closed          bool
}

func NewBrowser(ctx context.Context, profileDir string) (*Browser, error) {
	if profileDir == "" {
		return nil, errors.New("browser profile directory is required")
	}
	options := append([]chromedp.ExecAllocatorOption(nil), chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.UserDataDir(profileDir), chromedp.Flag("headless", os.Getenv("DEEPAGENT_BROWSER_HEADLESS") == "1"), chromedp.WindowSize(1280, 800))
	allocatorCtx, cancelAllocator := chromedp.NewExecAllocator(context.WithoutCancel(ctx), options...)
	browserCtx, cancel := chromedp.NewContext(allocatorCtx)
	browser := &Browser{ctx: browserCtx, cancel: cancel, cancelAllocator: cancelAllocator}
	stopStartup := context.AfterFunc(ctx, cancel)
	err := chromedp.Run(browserCtx, chromedp.EmulateViewport(1280, 800))
	stopStartup()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		browser.Close(context.Background())
		return nil, err
	}
	return browser, nil
}

func (browser *Browser) newOperation(ctx context.Context) (context.Context, context.CancelFunc) {
	operationCtx, cancel := context.WithTimeout(browser.ctx, 20*time.Second)
	stop := context.AfterFunc(ctx, cancel)
	return operationCtx, func() { stop(); cancel() }
}

func (browser *Browser) PerformAction(ctx context.Context, operation string, action agentmodel.ComputerAction) (observation *agentmodel.ComputerObservation, err error) {
	browser.mu.Lock()
	defer browser.mu.Unlock()
	err = ctx.Err()
	if err != nil {
		return nil, err
	}
	if browser.closed {
		return nil, errors.New("browser is closed")
	}
	actionStarted := false
	defer func() {
		if err != nil && actionStarted {
			err = errors.Join(ErrOutcomeUnknown, err)
		}
	}()
	operationCtx, cancel := browser.newOperation(ctx)
	defer cancel()
	var currentURL string
	err = chromedp.Run(operationCtx, chromedp.Location(&currentURL))
	if err != nil {
		return nil, err
	}
	expectedURL := currentURL
	if operation != "open" {
		if action.URL != "" && action.URL != currentURL {
			return nil, errors.New("stale_observation: page URL changed")
		}
		if operation != "observe" {
			err = browser.validateObservation(operationCtx, operation, action, currentURL)
			if err != nil {
				return nil, err
			}
		}
	}
	var actions []chromedp.Action
	switch operation {
	case "open":
		parsedURL, parseErr := url.Parse(action.URL)
		if parseErr != nil || parsedURL.Host == "" || parsedURL.User != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
			return nil, errors.New("valid HTTP(S) URL is required")
		}
		expectedURL = action.URL
		actions = []chromedp.Action{chromedp.Navigate(action.URL), chromedp.WaitReady("body", chromedp.ByQuery)}
	case "observe":
	case "click":
		x, y := 0.0, 0.0
		if action.ElementID > 0 {
			element := browser.observation.Elements[action.ElementID-1]
			x, y = element.X+element.Width/2, element.Y+element.Height/2
		} else {
			if action.X == nil || action.Y == nil {
				return nil, errors.New("element_id or x/y is required")
			}
			x, y = *action.X, *action.Y
		}
		if x < 0 || y < 0 || x >= float64(browser.observation.Width) || y >= float64(browser.observation.Height) {
			return nil, errors.New("coordinates outside screenshot")
		}
		actions = []chromedp.Action{chromedp.MouseClickXY(x, y)}
	case "type_text":
		if action.ElementID <= 0 {
			return nil, errors.New("element_id is required for browser text input")
		}
		script := fmt.Sprintf(`(() => {const e=window.__deepagentElements[%d];e.focus();if(e.select)e.select();else {const r=document.createRange();r.selectNodeContents(e);const s=getSelection();s.removeAllRanges();s.addRange(r)}})()`, action.ElementID-1)
		actions = []chromedp.Action{chromedp.Evaluate(script, nil), chromedp.ActionFunc(func(ctx context.Context) error { return input.InsertText(action.Text).Do(ctx) })}
	case "press_key":
		key, exists := map[string]string{"Enter": kb.Enter, "Tab": kb.Tab, "Escape": kb.Escape, "Backspace": kb.Backspace, "ArrowUp": kb.ArrowUp, "ArrowDown": kb.ArrowDown, "ArrowLeft": kb.ArrowLeft, "ArrowRight": kb.ArrowRight}[action.Key]
		if !exists {
			return nil, errors.New("unsupported browser key")
		}
		actions = []chromedp.Action{chromedp.KeyEvent(key)}
	case "scroll":
		actions = []chromedp.Action{chromedp.Evaluate(fmt.Sprintf("window.scrollBy(%d,%d)", action.DeltaX, action.DeltaY), nil)}
	default:
		return nil, fmt.Errorf("unknown browser operation %q", operation)
	}
	if len(actions) > 0 {
		actionStarted = true
		err = chromedp.Run(operationCtx, actions...)
		if err != nil {
			return nil, err
		}
	}
	// A navigation can leave the scope approved by Policy. Do not disclose that page.
	var resultURL string
	err = chromedp.Run(operationCtx, chromedp.Location(&resultURL))
	if err != nil {
		return nil, err
	}
	expectedOrigin, _ := url.Parse(expectedURL)
	resultOrigin, _ := url.Parse(resultURL)
	if expectedOrigin.Scheme != resultOrigin.Scheme || expectedOrigin.Host != resultOrigin.Host {
		browser.observation = nil
		return &agentmodel.ComputerObservation{URL: resultURL, Text: "Origin changed. Call browser_observe with this URL so Policy can authorize it."}, nil
	}
	return browser.observe(operationCtx, resultURL)
}

func (browser *Browser) validateObservation(ctx context.Context, operation string, action agentmodel.ComputerAction, pageURL string) error {
	observation := browser.observation
	if observation == nil || action.ObservationID == "" || action.ObservationID != observation.ID || observation.URL != pageURL {
		return errors.New("stale_observation: observe the page again")
	}
	if action.ElementID < 0 || action.ElementID > len(observation.Elements) {
		return errors.New("invalid element_id")
	}
	var dimensions []int
	err := chromedp.Run(ctx, chromedp.Evaluate("[innerWidth,innerHeight]", &dimensions))
	if err != nil {
		return err
	}
	if len(dimensions) != 2 || dimensions[0] != observation.Width || dimensions[1] != observation.Height {
		return errors.New("stale_observation: viewport changed")
	}
	if action.ElementID > 0 {
		var actual agentmodel.ComputerElement
		script := fmt.Sprintf(`(() => {const e=window.__deepagentElements?.[%d];if(!e?.isConnected)return null;const r=e.getBoundingClientRect();return {element_id:%d,role:e.tagName.toLowerCase(),name:(e.innerText||e.getAttribute('aria-label')||e.placeholder||'').slice(0,120),x:r.x,y:r.y,width:r.width,height:r.height}})()`, action.ElementID-1, action.ElementID)
		err = chromedp.Run(ctx, chromedp.Evaluate(script, &actual))
		if err != nil {
			return err
		}
		if actual != observation.Elements[action.ElementID-1] {
			return errors.New("stale_observation: element changed")
		}
		var hitTarget bool
		script = fmt.Sprintf(`(() => {const e=window.__deepagentElements[%d];const r=e.getBoundingClientRect();const hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return hit===e||e.contains(hit)})()`, action.ElementID-1)
		err = chromedp.Run(ctx, chromedp.Evaluate(script, &hitTarget))
		if err != nil {
			return err
		}
		if !hitTarget {
			return errors.New("stale_observation: target is covered by another element")
		}

	} else if operation == "click" {
		var screenshot []byte
		err = chromedp.Run(ctx, chromedp.CaptureScreenshot(&screenshot))
		if err != nil {
			return err
		}
		if base64.StdEncoding.EncodeToString(screenshot) != observation.Image {
			return errors.New("stale_observation: screen changed")
		}
	} else {
		var unchanged bool
		err = chromedp.Run(ctx, chromedp.Evaluate("document.activeElement === window.__deepagentFocused && scrollY === window.__deepagentScrollY", &unchanged))
		if err != nil {
			return err
		}
		if !unchanged {
			return errors.New("stale_observation: focus or scroll position changed")
		}
	}
	return nil
}

func (browser *Browser) observe(ctx context.Context, pageURL string) (*agentmodel.ComputerObservation, error) {
	observation := &agentmodel.ComputerObservation{ID: uuid.NewString()}
	script := `(() => {
  const elements=[...document.querySelectorAll('button,a,input,textarea,select,[role="button"],[contenteditable="true"]')].filter(e=>{const r=e.getBoundingClientRect();return r.width>0&&r.height>0&&r.x>=0&&r.y>=0&&r.bottom<=innerHeight&&r.right<=innerWidth}).slice(0,150);
  window.__deepagentElements=elements;window.__deepagentFocused=document.activeElement;window.__deepagentScrollY=scrollY;
  return {url:location.href,text:(document.body?.innerText||'').slice(0,8000),width:innerWidth,height:innerHeight,scroll_y:scrollY,elements:elements.map((e,i)=>{const r=e.getBoundingClientRect();return {element_id:i+1,role:e.tagName.toLowerCase(),name:(e.innerText||e.getAttribute('aria-label')||e.placeholder||'').slice(0,120),x:r.x,y:r.y,width:r.width,height:r.height}})};
 })()`
	var raw json.RawMessage
	var screenshot []byte
	err := chromedp.Run(ctx, chromedp.Evaluate(script, &raw), chromedp.CaptureScreenshot(&screenshot))
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(raw, observation)
	if err != nil {
		return nil, err
	}
	var finalURL string
	err = chromedp.Run(ctx, chromedp.Location(&finalURL))
	if err != nil {
		return nil, err
	}
	if observation.URL != pageURL || finalURL != pageURL {
		return nil, errors.New("stale_observation: page changed while capturing")
	}
	observation.Image = base64.StdEncoding.EncodeToString(screenshot)
	browser.observation = observation
	return observation, nil
}

func (browser *Browser) Close(context.Context) error {
	browser.closeOnce.Do(func() {
		// 已启动的 Chrome 先限时正常退出；启动失败时直接走取消清理。
		browserContext := chromedp.FromContext(browser.ctx)
		if browserContext.Browser != nil {
			closeContext, cancel := context.WithTimeout(browser.ctx, 5*time.Second)
			_ = chromedp.Cancel(closeContext)
			cancel()
		}
		browser.cancel()
		browser.cancelAllocator()
		browser.mu.Lock()
		browser.closed = true
		browser.observation = nil
		browser.mu.Unlock()
	})
	return nil
}
