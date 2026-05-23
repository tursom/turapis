#!/usr/bin/env python3
"""
Codex OAuth browser automation using undetected-chromedriver.

Usage:
    python3 codex_oauth_browser.py --callback-port 1455 --auth-url "https://..." --cookies '[...]'

Steps:
1. Starts local HTTP server for OAuth callback
2. Launches undetected Chrome (stealth patched) in headless mode
3. Injects session cookies from HTTP registration
4. Navigates to Codex OAuth authorize URL
5. Clicks consent/authorize button if present
6. Waits for auth server to redirect to local callback
7. Outputs authorization code as JSON to stdout
"""

import argparse
import json
import sys
import time
import threading
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import urlparse, parse_qs

# Lazy imports to avoid interfering with PyInstaller-built socket module
uc = None
By = None
WebDriverWait = None
EC = None


class CallbackHandler(BaseHTTPRequestHandler):
    """Captures the OAuth authorization code from the callback."""
    code = None
    error = None
    email_code = None  # verification code received via POST /auth/code

    def do_GET(self):
        parsed = urlparse(self.path)
        params = parse_qs(parsed.query)

        if parsed.path == "/auth/callback":
            code_list = params.get("code", [])
            error_list = params.get("error", [])

            if code_list:
                CallbackHandler.code = code_list[0]
                self.send_response(200)
                self.send_header("Content-Type", "text/html")
                self.end_headers()
                self.wfile.write(b"Authorization successful. You may close this window.")
            elif error_list:
                CallbackHandler.error = error_list[0]
                self.send_response(400)
                self.end_headers()
                self.wfile.write(b"Authorization failed.")
            else:
                self.send_response(400)
                self.end_headers()
                self.wfile.write(b"Missing code parameter.")
        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        if self.path == "/auth/code":
            length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(length).decode("utf-8").strip()
            CallbackHandler.email_code = body
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, format, *args):
        pass  # Suppress HTTP server log noise


def start_callback_server(port):
    """Starts the OAuth callback server in a background thread.
    Returns (server, thread, actual_port). If port is 0, OS picks one."""
    try:
        HTTPServer.allow_reuse_address = True
        server = HTTPServer(("", port), CallbackHandler)
        actual_port = server.server_address[1]
    except OSError as e:
        # Try with port 0 (OS-assigned) if the requested port is in use
        if port != 0:
            HTTPServer.allow_reuse_address = True
            server = HTTPServer(("", 0), CallbackHandler)
            actual_port = server.server_address[1]
        else:
            print(json.dumps({"error": f"cannot bind any port: {e}"}), flush=True)
            sys.exit(1)
    server.timeout = 120  # 2-minute timeout
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, thread, actual_port


def inject_cookies(driver, cookies_json):
    """Inject session cookies into the browser via CDP."""
    cookies = json.loads(cookies_json)
    cdp_cookies = []
    for c in cookies:
        domain = c.get("Domain", c.get("domain", ".auth.openai.com"))
        # CDP Network.setCookies rejects domains with leading dot
        domain = domain.lstrip(".")
        # Host-only cookies have no domain; CDP requires a non-empty domain
        if not domain:
            domain = "auth.openai.com"
        cdp_cookie = {
            "name": c.get("Name", c.get("name", "")),
            "value": c.get("Value", c.get("value", "")),
            "domain": domain,
            "path": c.get("Path", c.get("path", "/")),
            "httpOnly": c.get("HttpOnly", c.get("httpOnly", False)),
            "secure": c.get("Secure", c.get("secure", True)),
        }
        if cdp_cookie["name"] and cdp_cookie["value"]:
            cdp_cookies.append(cdp_cookie)

    if cdp_cookies:
        print(f"STEP: injecting {len(cdp_cookies)} cookies to {cdp_cookies[0]['domain']}", file=sys.stderr, flush=True)
        driver.execute_cdp_cmd("Network.setCookies", {"cookies": cdp_cookies})


def click_authorize(driver, timeout=10):
    """Try to click the consent/authorize button."""
    selectors = [
        "button[value='accept']",
        "button[type='submit']",
        "[data-action-button-primary='true']",
        "button[name='action'][value='accept']",
    ]
    for sel in selectors:
        try:
            btn = WebDriverWait(driver, timeout).until(
                EC.element_to_be_clickable((By.CSS_SELECTOR, sel))
            )
            btn.click()
            return True
        except Exception:
            continue
    return False


def main():
    parser = argparse.ArgumentParser(description="Codex OAuth Browser")
    parser.add_argument("--callback-port", type=int, default=1455, help="Port for local OAuth callback server")
    parser.add_argument("--auth-url", required=True, help="Full Codex OAuth authorize URL")
    parser.add_argument("--cookies", default="[]", help="JSON array of cookies to inject")
    parser.add_argument("--email", default="", help="Email address for browser-based login (skip cookie injection)")
    parser.add_argument("--password", default="", help="Password for login (used when 'Welcome back' prompts for password)")
    parser.add_argument("--timeout", type=int, default=120, help="Maximum wait time in seconds")
    parser.add_argument("--proxy", default="", help="SOCKS5 proxy URL (e.g. socks5://host:port)")
    args = parser.parse_args()

    # options built after lazy import

    driver = None
    server = None
    try:
        server, thread, actual_port = start_callback_server(args.callback_port)
        CallbackHandler.code = None
        CallbackHandler.error = None

        # Lazy import after socket is initialized
        import undetected_chromedriver as uc
        from selenium.webdriver.common.by import By
        from selenium.webdriver.support.ui import WebDriverWait
        from selenium.webdriver.support import expected_conditions as EC

        options = uc.ChromeOptions()
        options.add_argument("--headless=new")
        options.add_argument("--no-sandbox")
        options.add_argument("--disable-dev-shm-usage")
        options.add_argument("--disable-gpu")
        options.add_argument("--window-size=1920,1080")
        options.add_argument("--disable-blink-features=AutomationControlled")
        options.set_capability("goog:loggingPrefs", {"performance": "ALL"})
        if args.proxy:
            options.add_argument(f"--proxy-server={args.proxy}")
            options.add_argument("--proxy-bypass-list=<-loopback>")

        print("STEP: launching chrome...", file=sys.stderr, flush=True)
        driver = uc.Chrome(
            options=options,
            headless=True,
            use_subprocess=False,
            version_main=148,
            driver_executable_path="/usr/bin/chromedriver",
            browser_executable_path="/usr/bin/chromium-browser",
        )
        print("STEP: chrome launched", file=sys.stderr, flush=True)

        page_seq = [0]  # mutable counter for page numbering

        def save_page(driver, label=""):
            page_seq[0] += 1
            seq = page_seq[0]
            try:
                src = driver.page_source[:80000]
                url = driver.current_url[:200]
                title = driver.title
                fname = f"/tmp/page_{seq:02d}_{label}.html"
                with open(fname, "w") as f:
                    f.write(f"<!-- URL: {url} -->\n")
                    f.write(f"<!-- TITLE: {title} -->\n")
                    f.write(src)
                print(f"PAGE_SAVED: {fname} title={title}", file=sys.stderr, flush=True)
            except Exception as e:
                print(f"PAGE_SAVE_FAILED: {e}", file=sys.stderr, flush=True)

        if args.email:
            driver.delete_all_cookies()
            driver.set_page_load_timeout(30)
            for login_url in ["https://auth.openai.com/login"]:  # chatgpt.com hangs; use auth.openai.com
                print(f"STEP: navigating to {login_url}", file=sys.stderr, flush=True)
                try:
                    driver.get(login_url)
                    save_page(driver, "login")
                except Exception as e:
                    print(f"WARN: page load failed for {login_url}: {e}", file=sys.stderr, flush=True)
                    driver.execute_script("window.stop()")
                    continue
                print(f"LOGIN PAGE ({login_url}): title={driver.title}", flush=True, file=sys.stderr)
                try:
                    body = driver.find_element(By.TAG_NAME, "body").text[:300]
                    print(f"LOGIN PAGE BODY: {body}", flush=True, file=sys.stderr)
                except:
                    pass
                save_page(driver, "login_load")

                # If we see "session ended", click "Log in"
                if "Your session has ended" in driver.title:
                    print("STEP: clicking Log in link", file=sys.stderr, flush=True)
                    try:
                        driver.find_element(By.LINK_TEXT, "Log in").click()
                        time.sleep(3)
                        print(f"AFTER LOGIN CLICK: {driver.title}", file=sys.stderr, flush=True)
                        save_page(driver, "after_login_click")
                    except Exception as e:
                        print(f"WARN: Log in link not found: {e}", file=sys.stderr, flush=True)

                # Check if email input is present
                try:
                    email_input = WebDriverWait(driver, 5).until(
                        EC.presence_of_element_located((By.CSS_SELECTOR, "input[type=email]"))
                    )
                    email_input.send_keys(args.email)
                    print(f"STEP: email_entered {args.email}", file=sys.stderr, flush=True)
                    break
                except Exception:
                    print(f"WARN: email input not found on {login_url}", file=sys.stderr, flush=True)
            else:
                print(json.dumps({"error": "email input not found on any login page"}), flush=True)
                sys.exit(1)

            try:
                driver.find_element(By.CSS_SELECTOR, "button[type=submit]").click()
                print("STEP: email_submitted", file=sys.stderr, flush=True)
            except Exception as e:
                print(json.dumps({"error": f"submit button not found: {e}"}), flush=True)
                sys.exit(1)

            # Wait for page to load after email submit
            time.sleep(2)
            print(f"STEP: after_email_submit url={driver.current_url[:100]} title={driver.title}", file=sys.stderr, flush=True)
            save_page(driver, "after_email_submit")

            try:
                print("STEP: waiting for password or code input...", file=sys.stderr, flush=True)
                WebDriverWait(driver, 15).until(
                    EC.presence_of_element_located((By.CSS_SELECTOR, "input[type=text], input[type=password]"))
                )
                page_body = driver.find_element(By.TAG_NAME, "body").text[:300]
                print(f"POST_SUBMIT PAGE: {page_body}", file=sys.stderr, flush=True)
                save_page(driver, "post_submit")

                pwd_fields = driver.find_elements(By.CSS_SELECTOR, "input[type=password]")
                if pwd_fields:
                    print("STEP: password_prompt_detected, trying one-time code instead", file=sys.stderr, flush=True)
                    try:
                        driver.find_element(By.CSS_SELECTOR, "button[value='passwordless_login_send_otp']").click()
                        print("STEP: clicked one-time code button", file=sys.stderr, flush=True)
                        time.sleep(3)
                        print(f"STEP: after_code_btn url={driver.current_url[:100]} title={driver.title}", file=sys.stderr, flush=True)
                    except Exception as e:
                        print(f"WARN: one-time code link not found ({e}), entering password", file=sys.stderr, flush=True)
                        pwd_fields[0].send_keys(args.password)
                        driver.find_element(By.CSS_SELECTOR, "button[type=submit]").click()
                        print("STEP: password_submitted", file=sys.stderr, flush=True)
                        time.sleep(3)
                        save_page(driver, "after_password_submit")

                # Handle verification code flow (either directly from email submit,
                # or after clicking "one-time code" on password page)
                WebDriverWait(driver, 15).until(
                    EC.presence_of_element_located((By.CSS_SELECTOR, "input[type=text]"))
                )
                print("STEP: code_input_ready", file=sys.stderr, flush=True)
                save_page(driver, "code_input_ready")
                print("STEP: waiting_for_code", flush=True, file=sys.stderr)
                CallbackHandler.email_code = None
                code_deadline = time.time() + args.timeout
                while time.time() < code_deadline and CallbackHandler.email_code is None:
                    time.sleep(1)
                if CallbackHandler.email_code is None:
                    print(json.dumps({"error": "timeout waiting for verification code"}), flush=True)
                    sys.exit(1)
                code = CallbackHandler.email_code
                print(f"STEP: entering_code", file=sys.stderr, flush=True)
                driver.find_element(By.CSS_SELECTOR, "input[type=text]").send_keys(code)
                try:
                    driver.find_element(By.CSS_SELECTOR, "button[type=submit]").click()
                    print("STEP: code_submitted", file=sys.stderr, flush=True)
                except Exception:
                    pass
                time.sleep(3)
            except Exception as e:
                import traceback
                traceback.print_exc(file=sys.stderr)
                print(json.dumps({"error": f"post-submit error: {e}"}), flush=True)
                sys.exit(1)
        else:
            driver.get("https://auth.openai.com/robots.txt")
            time.sleep(2)
            inject_cookies(driver, args.cookies)
            time.sleep(2)
            inject_cookies(driver, args.cookies)
            time.sleep(2)

        # Navigate to the OAuth authorize URL
        print(f"NAVIGATING to {args.auth_url[:120]}...", flush=True, file=sys.stderr)
        driver.get(args.auth_url)
        save_page(driver, "oauth_authorize")
        print(f"NAVIGATED OK, current URL: {driver.current_url[:200]}", flush=True, file=sys.stderr)
        print(f"PAGE TITLE: {driver.title}", flush=True, file=sys.stderr)
        try:
            body = driver.find_element(By.TAG_NAME, "body").text[:200]
            print(f"PAGE BODY: {body}", flush=True, file=sys.stderr)
        except Exception:
            print("PAGE BODY: (unavailable)", flush=True, file=sys.stderr)

        from urllib.parse import urlparse, parse_qs
        import re
        parsed = urlparse(driver.current_url)
        params = parse_qs(parsed.query)
        if params.get("code"):
            print(f"STEP: redirect_code_found", file=sys.stderr, flush=True)
            print(json.dumps({"code": params["code"][0]}))
            sys.exit(0)

        if "error" in driver.current_url.lower() or "ran into an issue" in (driver.find_element(By.TAG_NAME, "body").text or ""):
            body_text = driver.find_element(By.TAG_NAME, "body").text[:500]
            print(f"STEP: oauth_error_page url={driver.current_url[:200]} body={body_text}", file=sys.stderr, flush=True)
            print(json.dumps({"error": f"auth error page: {body_text[:200]}"}))
            sys.exit(1)

        # Handle "choose an account" page
        if "choose-an-account" in driver.current_url:
            print(f"STEP: account_selection_page", file=sys.stderr, flush=True)
            try:
                selectors = [
                    "button",
                    "a[href]",
                    "[role=button]",
                    "input[type=submit]",
                    "form button",
                ]
                for sel in selectors:
                    try:
                        btns = driver.find_elements(By.CSS_SELECTOR, sel)
                        for btn in btns:
                            txt = (btn.text or "").strip()
                            if txt and len(txt) < 100:
                                print(f"  button: '{txt}'", file=sys.stderr, flush=True)
                    except:
                        pass
                clicked_account = False
                for element in driver.find_elements(By.CSS_SELECTOR, "*"):
                    try:
                        txt = (element.text or "").strip()
                    except:
                        continue
                    if txt == "Select account" or txt.startswith("Select account"):
                        element.click()
                        clicked_account = True
                        break
                if not clicked_account:
                    link = driver.find_element(By.PARTIAL_LINK_TEXT, "Select account")
                    link.click()
                    clicked_account = True
                if clicked_account:
                    time.sleep(3)
                    save_page(driver, "after_account_select")
                    print(f"AFTER ACCOUNT SELECT: url={driver.current_url[:200]} title={driver.title}", flush=True, file=sys.stderr)
                    # Check for phone verification page
                    if "add-phone" in driver.current_url or "phone" in driver.title.lower():
                        save_page(driver, "phone_required")
                        body_text = driver.find_element(By.TAG_NAME, "body").text[:500]
                        print(f"STEP: phone_verification_page body={body_text}", file=sys.stderr, flush=True)

                        print("waiting_for_phone_number", file=sys.stderr, flush=True)
                        phone_deadline = time.time() + 30
                        while CallbackHandler.email_code is None and time.time() < phone_deadline:
                            time.sleep(0.5)
                        if CallbackHandler.email_code is None:
                            print(json.dumps({"error": "no phone number received from Go"}))
                            sys.exit(1)
                        phone_number = CallbackHandler.email_code
                        CallbackHandler.email_code = None

                        print(f"STEP: received_phone_number", file=sys.stderr, flush=True)
                        try:
                            phone_input = WebDriverWait(driver, 5).until(
                                EC.presence_of_element_located((By.CSS_SELECTOR, "input[type=tel], input[name=phone], input[placeholder*=phone i], input[placeholder*=mobile i]"))
                            )
                            phone_input.clear()
                            phone_input.send_keys(phone_number)
                            time.sleep(1)
                            submit_btn = driver.find_element(By.CSS_SELECTOR, "button[type=submit], input[type=submit]")
                            submit_btn.click()
                            time.sleep(3)
                            save_page(driver, "after_phone_submit")
                            print(f"PHONE SUBMITTED: url={driver.current_url[:200]}", file=sys.stderr, flush=True)
                        except Exception as e:
                            print(f"WARN: phone input failed: {e}", file=sys.stderr, flush=True)
                            print(json.dumps({"error": f"phone input failed: {e}"}))
                            sys.exit(1)

                        print("waiting_for_sms_code", file=sys.stderr, flush=True)
                        sms_deadline = time.time() + 180
                        while CallbackHandler.email_code is None and time.time() < sms_deadline:
                            time.sleep(0.5)
                        if CallbackHandler.email_code is None:
                            print(json.dumps({"error": "no sms code received from Go"}))
                            sys.exit(1)
                        sms_code = CallbackHandler.email_code
                        CallbackHandler.email_code = None

                        print(f"STEP: received_sms_code", file=sys.stderr, flush=True)
                        try:
                            code_input = WebDriverWait(driver, 10).until(
                                EC.presence_of_element_located((By.CSS_SELECTOR, "input[type=text], input[type=tel], input[placeholder*=code i], input[placeholder*=digit i]"))
                            )
                            code_input.clear()
                            code_input.send_keys(sms_code)
                            time.sleep(1)
                            submit_btn = driver.find_element(By.CSS_SELECTOR, "button[type=submit], input[type=submit]")
                            submit_btn.click()
                            time.sleep(3)
                            save_page(driver, "after_sms_code_submit")
                            print(f"SMS CODE SUBMITTED: url={driver.current_url[:200]}", file=sys.stderr, flush=True)
                        except Exception as e:
                            print(f"WARN: sms code input failed: {e}", file=sys.stderr, flush=True)
                            print(json.dumps({"error": f"sms code input failed: {e}"}))
                            sys.exit(1)

                        parsed4 = parse_qs(urlparse(driver.current_url).query)
                        if parsed4.get("code"):
                            print(f"STEP: code_after_phone_verify", file=sys.stderr, flush=True)
                            print(json.dumps({"code": parsed4["code"][0]}))
                            sys.exit(0)
                        # After phone verify, continue to consent/callback flow
                    # Check for code after account selection
                    parsed3 = parse_qs(urlparse(driver.current_url).query)
                    if parsed3.get("code"):
                        print(f"STEP: code_after_account_select", file=sys.stderr, flush=True)
                        print(json.dumps({"code": parsed3["code"][0]}))
                        sys.exit(0)
                else:
                    print(f"WARN: cannot find Select account button", file=sys.stderr, flush=True)
            except Exception as e:
                print(f"WARN: account select failed: {e}", file=sys.stderr, flush=True)

        # Try to click the consent/authorize button
        clicked = click_authorize(driver, timeout=5)
        print(f"STEP: consent_button {'clicked' if clicked else 'not_found'}", file=sys.stderr, flush=True)

        # If clicked, check URL again for code
        params2 = parse_qs(urlparse(driver.current_url).query)
        if params2.get("code"):
            print(f"STEP: consent_code_found", file=sys.stderr, flush=True)
            print(json.dumps({"code": params2["code"][0]}))
            sys.exit(0)

        page_text = driver.find_element(By.TAG_NAME, "body").text
        code_match = re.search(r'(?:code|verification code)[:\s]*([A-Za-z0-9]{20,})', page_text, re.IGNORECASE)
        if code_match:
            print(f"STEP: page_code_found", file=sys.stderr, flush=True)
            print(json.dumps({"code": code_match.group(1)}))
            sys.exit(0)

        # Wait for callback on localhost
        deadline = time.time() + args.timeout
        while time.time() < deadline:
            if CallbackHandler.code:
                print(json.dumps({"code": CallbackHandler.code}))
                sys.exit(0)
            if CallbackHandler.error:
                print(json.dumps({"error": f"authorization_error: {CallbackHandler.error}"}))
                sys.exit(1)
            time.sleep(1)

        print(json.dumps({"error": "timeout waiting for authorization callback"}))
        sys.exit(1)

    except Exception as e:
        print(json.dumps({"error": str(e)}), flush=True)
        sys.exit(1)
    finally:
        try:
            if 'driver' in dir() and driver is not None:
                driver.quit()
        except:
            pass
        try:
            server.shutdown()
        except:
            pass


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        import traceback
        print(json.dumps({"error": f"unhandled: {e}"}), flush=True)
        traceback.print_exc(file=sys.stderr)
        sys.exit(1)
