package browser

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/chromedp/chromedp"
)

// BrowserlessClient 通过远程 browserless/chromium 实例提供基于 CDP 的浏览器自动化。
// 它封装了 chromedp，提供常见的浏览器操作，
// 如导航、元素交互、截图和内容提取。
type BrowserlessClient struct {
	wsURL   string
	timeout time.Duration
}

// NewBrowserlessClient 创建一个新的 BrowserlessClient，连接到给定的
// browserless WebSocket URL（格式：ws://host:port/chromium）。
// 当调用方未提供截止时间时，timeout 将应用于 NewContext 中的父上下文。
func NewBrowserlessClient(wsURL string, timeout time.Duration) *BrowserlessClient {
	return &BrowserlessClient{
		wsURL:   wsURL,
		timeout: timeout,
	}
}

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

// Navigate 指示浏览器导航到给定的 URL。
// 它会阻塞直到页面加载事件触发。
func (b *BrowserlessClient) Navigate(ctx context.Context, url string) error {
	return chromedp.Run(ctx, chromedp.Navigate(url))
}

// WaitForSelector 等待匹配 CSS 选择器的元素在页面上可见。
func (b *BrowserlessClient) WaitForSelector(ctx context.Context, selector string) error {
	return chromedp.Run(ctx, chromedp.WaitVisible(selector, chromedp.ByQuery))
}

// SendKeys 向由 CSS 选择器标识的输入元素中输入文本。
func (b *BrowserlessClient) SendKeys(ctx context.Context, selector, text string) error {
	return chromedp.Run(ctx, chromedp.SendKeys(selector, text, chromedp.ByQuery))
}

// Click 对匹配 CSS 选择器的元素执行鼠标点击。
func (b *BrowserlessClient) Click(ctx context.Context, selector string) error {
	return chromedp.Run(ctx, chromedp.Click(selector, chromedp.ByQuery))
}

// CurrentURL 返回当前页面的 URL。
func (b *BrowserlessClient) CurrentURL(ctx context.Context) (string, error) {
	var url string
	if err := chromedp.Run(ctx, chromedp.Location(&url)); err != nil {
		return "", err
	}
	return url, nil
}

// Screenshot 截取全页面截图并将其作为 PNG 文件写入给定的路径。
func (b *BrowserlessClient) Screenshot(ctx context.Context, path string) error {
	var buf []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
		return err
	}
	return os.WriteFile(path, buf, 0o644)
}

// TextContent 返回匹配 CSS 选择器的第一个元素的可见文本内容。
func (b *BrowserlessClient) TextContent(ctx context.Context, selector string) (string, error) {
	var text string
	if err := chromedp.Run(ctx, chromedp.TextContent(selector, &text, chromedp.ByQuery)); err != nil {
		return "", err
	}
	return text, nil
}

// AttributeValue 返回匹配 CSS 选择器的第一个元素上
// 指定属性的值。
func (b *BrowserlessClient) AttributeValue(ctx context.Context, selector, attr string) (string, error) {
	var val string
	var ok bool
	if err := chromedp.Run(ctx, chromedp.AttributeValue(selector, attr, &val, &ok, chromedp.ByQuery)); err != nil {
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
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &result)); err != nil {
		return "", err
	}
	return result, nil
}
