package browser

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// stealthJS is injected via page.AddScriptToEvaluateOnNewDocument to
// persist across navigations. It overrides browser automation fingerprints
// to bypass Cloudflare/OpenAI anti-bot detection.
const stealthJS = `(function(){
// Hide webdriver
try{Object.defineProperty(navigator,'webdriver',{get:()=>false})}catch(e){}

// Fake plugins
try{Object.defineProperty(navigator,'plugins',{get:()=>{const p=[{name:'Chrome PDF Plugin',filename:'internal-pdf-viewer',description:'Portable Document Format',length:1},{name:'Chrome PDF Viewer',filename:'mhjfbmdgcfjbbpaeojofohoefgiehjai',description:'',length:1},{name:'Native Client',filename:'internal-nacl-plugin',description:'',length:2}];p.item=i=>p[i];p.namedItem=n=>p.find(x=>x.name===n)||null;p.refresh=()=>{};return p}})}catch(e){}

// Navigator overrides
try{Object.defineProperty(navigator,'languages',{get:()=>['en-US','en','zh-CN']})}catch(e){}
try{Object.defineProperty(navigator,'platform',{get:()=>'Win32'})}catch(e){}
try{Object.defineProperty(navigator,'hardwareConcurrency',{get:()=>8})}catch(e){}
try{Object.defineProperty(navigator,'deviceMemory',{get:()=>8})}catch(e){}
try{Object.defineProperty(navigator,'maxTouchPoints',{get:()=>0})}catch(e){}
try{Object.defineProperty(navigator,'productSub',{get:()=>'20030107'})}catch(e){}
try{Object.defineProperty(navigator,'vendor',{get:()=>'Google Inc.'})}catch(e){}
try{Object.defineProperty(navigator,'vendorSub',{get:()=>''})}catch(e){}

// Screen dimensions
try{Object.defineProperty(screen,'width',{get:()=>1920})}catch(e){}
try{Object.defineProperty(screen,'height',{get:()=>1080})}catch(e){}
try{Object.defineProperty(screen,'availWidth',{get:()=>1920})}catch(e){}
try{Object.defineProperty(screen,'availHeight',{get:()=>1040})}catch(e){}
try{Object.defineProperty(screen,'colorDepth',{get:()=>24})}catch(e){}
try{Object.defineProperty(screen,'pixelDepth',{get:()=>24})}catch(e){}

// Connection info
try{Object.defineProperty(navigator,'connection',{get:()=>({effectiveType:'4g',rtt:50,downlink:10,saveData:false})})}catch(e){}

// chrome.runtime
try{window.chrome={runtime:{},loadTimes:()=>{},csi:()=>{},app:{}}}catch(e){}

// Permissions API
try{const o=navigator.permissions.query;navigator.permissions.query=p=>(p.name==='notifications'?Promise.resolve({state:Notification.permission}):o(p))}catch(e){}
try{Notification.requestPermission=()=>Promise.resolve('default')}catch(e){}

// WebGL fingerprint spoofing
try{const g=WebGLRenderingContext.prototype.getParameter;WebGLRenderingContext.prototype.getParameter=function(p){if(p===37445)return'Intel Inc.';if(p===37446)return'Intel Iris OpenGL Engine';return g.call(this,p)}}catch(e){}
try{const g2=WebGL2RenderingContext.prototype.getParameter;WebGL2RenderingContext.prototype.getParameter=function(p){if(p===37445)return'Intel Inc.';if(p===37446)return'Intel Iris OpenGL Engine';return g2.call(this,p)}}catch(e){}

// Canvas fingerprint noise (minimal, per-context)
try{const orig=HTMLCanvasElement.prototype.toDataURL;HTMLCanvasElement.prototype.toDataURL=function(){const ctx=this.getContext('2d');if(ctx){const d=ctx.getImageData(0,0,1,1);d.data[0]=d.data[0]^1;ctx.putImageData(d,0,0)}return orig.apply(this,arguments)}}catch(e){}

// window dimensions
try{Object.defineProperty(window,'outerWidth',{get:()=>1920})}catch(e){}
try{Object.defineProperty(window,'outerHeight',{get:()=>1080})}catch(e){}
try{Object.defineProperty(window,'innerWidth',{get:()=>1920})}catch(e){}
try{Object.defineProperty(window,'innerHeight',{get:()=>950})}catch(e){}
})()`

// BrowserlessClient 通过远程 browserless/chromium 实例提供基于 CDP 的浏览器自动化。
// 它封装了 chromedp，提供常见的浏览器操作，
// 如导航、元素交互、截图和内容提取。
type BrowserlessClient struct {
	wsURL   string
	timeout time.Duration
}

// NewBrowserlessClient 创建一个新的 BrowserlessClient，连接到给定的
// browserless WebSocket URL（v2 端口 3000，格式：ws://host:3000）。
// 当调用方未提供截止时间时，timeout 将应用于 NewContext 中的父上下文。
func NewBrowserlessClient(wsURL string, timeout time.Duration) *BrowserlessClient {
	return &BrowserlessClient{
		wsURL:   wsURL,
		timeout: timeout,
	}
}

// stealthInjected tracks which contexts have already had stealth JS injected.
// Using sync.Map avoids locking on every operation.
var stealthInjected sync.Map

// NewContext 创建一个连接到远程 browserless 实例的 chromedp 上下文。
// 返回的 cancel 函数必须被调用以清理浏览器标签页
// 和底层的 allocator。
//
// 如果父上下文没有截止时间，则应用客户端的 timeout。
// 使用 NoModifyURL 可以防止 chromedp 通过 /json/version 重写
// WebSocket URL（该端点返回从主机无法访问的内部容器地址）。
func (b *BrowserlessClient) NewContext(parent context.Context) (context.Context, context.CancelFunc) {
	var pCancel context.CancelFunc
	if _, ok := parent.Deadline(); !ok {
		parent, pCancel = context.WithTimeout(parent, b.timeout)
	}
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(parent, b.wsURL, chromedp.NoModifyURL)
	ctx, cancel := chromedp.NewContext(allocCtx)
	return ctx, func() {
		cancel()
		allocCancel()
		if pCancel != nil {
			pCancel()
		}
	}
}

// run executes the given actions via chromedp.Run. On the first call per context,
// stealth JS is injected via page.AddScriptToEvaluateOnNewDocument so it persists
// across all navigations. Subsequent calls skip injection (tracked via sync.Map).
func (b *BrowserlessClient) run(ctx context.Context, actions ...chromedp.Action) error {
	all := make([]chromedp.Action, 0, len(actions)+2)
	if _, already := stealthInjected.LoadOrStore(ctx, struct{}{}); !already {
		all = append(all,
			chromedp.EmulateViewport(1920, 1080, chromedp.EmulateScale(1)),
			chromedp.ActionFunc(func(actionCtx context.Context) error {
				_, err := page.AddScriptToEvaluateOnNewDocument(stealthJS).Do(actionCtx)
				return err
			}),
		)
	}
	all = append(all, actions...)
	return chromedp.Run(ctx, all...)
}

// Navigate 指示浏览器导航到给定的 URL。
// 它会阻塞直到页面加载事件触发。
func (b *BrowserlessClient) Navigate(ctx context.Context, url string) error {
	return b.run(ctx, chromedp.Navigate(url))
}

// WaitForSelector 等待匹配 CSS 选择器的元素在页面上可见。
func (b *BrowserlessClient) WaitForSelector(ctx context.Context, selector string) error {
	return b.run(ctx, chromedp.WaitVisible(selector, chromedp.ByQuery))
}

// SendKeys 向由 CSS 选择器标识的输入元素中输入文本。
func (b *BrowserlessClient) SendKeys(ctx context.Context, selector, text string) error {
	return b.run(ctx, chromedp.SendKeys(selector, text, chromedp.ByQuery))
}

// Click 对匹配 CSS 选择器的元素执行鼠标点击。
func (b *BrowserlessClient) Click(ctx context.Context, selector string) error {
	return b.run(ctx, chromedp.Click(selector, chromedp.ByQuery))
}

// CurrentURL 返回当前页面的 URL。
func (b *BrowserlessClient) CurrentURL(ctx context.Context) (string, error) {
	var url string
	if err := b.run(ctx, chromedp.Location(&url)); err != nil {
		return "", err
	}
	return url, nil
}

// Screenshot 截取全页面截图并将其作为 PNG 文件写入给定的路径。
func (b *BrowserlessClient) Screenshot(ctx context.Context, path string) error {
	var buf []byte
	if err := b.run(ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
		return err
	}
	return os.WriteFile(path, buf, 0o644)
}

// TextContent 返回匹配 CSS 选择器的第一个元素的可见文本内容。
func (b *BrowserlessClient) TextContent(ctx context.Context, selector string) (string, error) {
	var text string
	if err := b.run(ctx, chromedp.TextContent(selector, &text, chromedp.ByQuery)); err != nil {
		return "", err
	}
	return text, nil
}

// AttributeValue 返回匹配 CSS 选择器的第一个元素上
// 指定属性的值。
func (b *BrowserlessClient) AttributeValue(ctx context.Context, selector, attr string) (string, error) {
	var val string
	var ok bool
	if err := b.run(ctx, chromedp.AttributeValue(selector, attr, &val, &ok, chromedp.ByQuery)); err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("attribute %q not found on %q", attr, selector)
	}
	return val, nil
}

// EvalJS 在页面中执行给定的 JavaScript 表达式，
// 并以字符串形式返回结果。
func (b *BrowserlessClient) EvalJS(ctx context.Context, script string) (string, error) {
	var result string
	if err := b.run(ctx, chromedp.Evaluate(script, &result)); err != nil {
		return "", err
	}
	return result, nil
}

// SetAuthCookies injects session cookies into the browser via CDP Network.setCookie.
// CSRF tokens and other non-essential cookies with special characters are skipped.
func (b *BrowserlessClient) SetAuthCookies(ctx context.Context, cookies []*http.Cookie) error {
	actions := make([]chromedp.Action, 0, len(cookies))
	skipped := 0
	for _, c := range cookies {
		if strings.Contains(c.Name, "csrf") || strings.Contains(c.Name, "_dev_") {
			skipped++
			continue
		}
		if c.Domain == "" {
			skipped++
			continue
		}
		// Skip cookies with empty or invalid domains.
		if c.Domain == "" {
			continue
		}
		c := c // capture for closure
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			expires := time.Now().Add(365 * 24 * time.Hour)
			if !c.Expires.IsZero() {
				expires = c.Expires
			}
			exp := cdp.TimeSinceEpoch(expires)

			url := "https://" + strings.TrimPrefix(c.Domain, ".") + c.Path
			if c.Path == "" {
				url = "https://" + strings.TrimPrefix(c.Domain, ".") + "/"
			}

			return network.SetCookie(c.Name, c.Value).
				WithURL(url).
				WithDomain(c.Domain).
				WithPath(c.Path).
				WithSecure(c.Secure).
				WithHTTPOnly(c.HttpOnly).
				WithExpires(&exp).
				Do(ctx)
		}))
	}
	if len(actions) == 0 {
		return nil
	}
	return b.run(ctx, actions...)
}
