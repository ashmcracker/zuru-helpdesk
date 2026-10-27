// zuru-helpdesk 的 OAuth 登录：
//
// 在 ZURU helpdesk 授权服务器上动态注册一个 public client（RFC 7591），
// 走一次浏览器授权（PKCE + loopback 回调，最终落到 Microsoft Entra 登录），
// 把 access/refresh token 持久化到 ~/.zuru-helpdesk/token.json，之后自动刷新。
//
// 认证链路：helpdesk.zuru.app/authorize → 302 login.microsoftonline.com（ZURU Entra tenant）
//         → 用户登录 → helpdesk 回调 → CLI loopback → helpdesk.zuru.app/token 换 token。
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	authBase = "https://helpdesk.zuru.app"
	// helpdesk MCP 是 streamable-http，端点必须带尾斜杠，否则 307。
	mcpEndpoint = authBase + "/mcp/"
	registerEP  = authBase + "/register"
	authorizeEP = authBase + "/authorize"
	tokenEP     = authBase + "/token"
	// helpdesk 资源要求的 scope（来自 .well-known/oauth-protected-resource/mcp）。
	helpdeskScope = "api://b7daeec6-9e18-445e-a313-841c35fa50bd/.default"
	callbackPort  = 8972
)

var errNoToken = errors.New("no token (run `zuru-helpdesk login`)")

type tokenStore struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	RedirectURI  string `json:"redirect_uri"`
	Scope        string `json:"scope,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
}

func tokenFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".zuru-helpdesk-token.json"
	}
	return filepath.Join(home, ".zuru-helpdesk", "token.json")
}

func loadToken() (*tokenStore, error) {
	b, err := os.ReadFile(tokenFile())
	if err != nil {
		return nil, err
	}
	var t tokenStore
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func saveToken(t *tokenStore) error {
	p := tokenFile()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	b, _ := json.MarshalIndent(t, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

func haveToken() bool {
	t, err := loadToken()
	return err == nil && (t.AccessToken != "" || t.RefreshToken != "")
}

// bearerToken 返回可直接使用的 Authorization 头；access token 缺失或临期时先刷新。
func bearerToken() (string, error) {
	t, err := loadToken()
	if err != nil || (t.AccessToken == "" && t.RefreshToken == "") {
		return "", errNoToken
	}
	if t.AccessToken == "" || (t.ExpiresAt > 0 && time.Now().Add(90*time.Second).Unix() > t.ExpiresAt) {
		if t.RefreshToken == "" {
			return "", errNoToken
		}
		if err := refreshFlow(t); err != nil {
			return "", err
		}
	}
	return "Bearer " + t.AccessToken, nil
}

// forceRefresh 在 401 之后强制刷新一次（服务器可能提前作废了 access token）。
func forceRefresh() bool {
	t, err := loadToken()
	if err != nil || t.RefreshToken == "" {
		return false
	}
	return refreshFlow(t) == nil
}

func postForm(u string, form url.Values) (int, []byte) {
	req, err := http.NewRequest("POST", u, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func refreshFlow(t *tokenStore) error {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.RefreshToken)
	form.Set("client_id", t.ClientID)
	if t.ClientSecret != "" {
		form.Set("client_secret", t.ClientSecret)
	}
	if t.Scope != "" {
		form.Set("scope", t.Scope)
	}
	code, b := postForm(tokenEP, form)
	if code != 200 {
		return fmt.Errorf("刷新会话失败（HTTP %d）: %s", code, clip(string(b), 200))
	}
	return applyTokenResponse(t, b)
}

func applyTokenResponse(t *tokenStore, b []byte) error {
	var r struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	if r.AccessToken == "" {
		return errors.New("token 响应里没有 access_token")
	}
	t.AccessToken = r.AccessToken
	if r.RefreshToken != "" {
		t.RefreshToken = r.RefreshToken // 轮换式 refresh：旧的随即作废
	}
	if r.ExpiresIn > 0 {
		t.ExpiresAt = time.Now().Unix() + r.ExpiresIn
	}
	if r.Scope != "" {
		t.Scope = r.Scope
	}
	return saveToken(t)
}

func randB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func openBrowser(u string) {
	if runtime.GOOS == "windows" {
		// Windows cmd 的 start 会把未转义的 '&' 当作命令分隔符截断 URL；
		// 将 '&' 转义为 '^&'，让 cmd.exe 保持完整参数传给 start。
		_ = exec.Command("cmd", "/c", "start", "", strings.ReplaceAll(u, "&", "^&")).Start()
		return
	}
	cmds := [][]string{
		{"wslview", u},
		{"xdg-open", u},
		{"/mnt/c/Windows/explorer.exe", u},
	}
	for _, c := range cmds {
		if path, err := exec.LookPath(c[0]); err == nil {
			_ = exec.Command(path, c[1:]...).Start()
			return
		}
	}
}

// cmdLogin：先尝试静默刷新，不行再走完整浏览器授权。
func cmdLogin(args []string) {
	if t, err := loadToken(); err == nil && t.RefreshToken != "" {
		if err := refreshFlow(t); err == nil {
			fmt.Println("✅ 会话已刷新（无需浏览器授权）")
			printWhoami()
			return
		}
		fmt.Println("⚠️ 已保存的会话失效，重新登录")
	}

	redirect := fmt.Sprintf("http://localhost:%d/callback", callbackPort)
	t := &tokenStore{RedirectURI: redirect, Scope: helpdeskScope}

	reg := map[string]any{
		"client_name":                "zuru-helpdesk CLI",
		"redirect_uris":              []string{redirect},
		"response_types":             []string{"code"},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_method": "client_secret_post",
		"scope":                      helpdeskScope,
	}
	rb, _ := json.Marshal(reg)
	req, _ := http.NewRequest("POST", registerEP, strings.NewReader(string(rb)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "❌ 连不上授权服务器:", err)
		os.Exit(1)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		fmt.Fprintf(os.Stderr, "❌ 客户端注册失败（HTTP %d）: %s\n", resp.StatusCode, clip(string(b), 300))
		os.Exit(1)
	}
	var rr struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	_ = json.Unmarshal(b, &rr)
	t.ClientID = rr.ClientID
	t.ClientSecret = rr.ClientSecret

	verifier := randB64(48)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randB64(16)

	codeCh := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		q := r.URL.Query()
		if q.Get("state") != state {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "state 不匹配，请重试登录")
			return
		}
		if e := q.Get("error"); e != "" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, "授权被拒绝: %s", e)
			codeCh <- ""
			return
		}
		fmt.Fprint(w, "<h3>✅ 登录成功 — 可以关闭此页面回到终端</h3>")
		codeCh <- q.Get("code")
	})
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", callbackPort), Handler: mux}
	go func() { _ = srv.ListenAndServe() }()
	defer srv.Close()

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", t.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", helpdeskScope)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	authURL := authorizeEP + "?" + q.Encode()

	openBrowser(authURL)
	fmt.Printf("正在打开浏览器登录…\n若没有自动打开，请手动访问下面的 URL 完成登录：\n\n  %s\n\n（最多等待 5 分钟）\n", authURL)

	select {
	case code := <-codeCh:
		if code == "" {
			fmt.Fprintln(os.Stderr, "❌ 授权被拒绝")
			os.Exit(1)
		}
		form := url.Values{}
		form.Set("grant_type", "authorization_code")
		form.Set("code", code)
		form.Set("redirect_uri", redirect)
		form.Set("client_id", t.ClientID)
		form.Set("code_verifier", verifier)
		if t.ClientSecret != "" {
			form.Set("client_secret", t.ClientSecret)
		}
		tc, tb := postForm(tokenEP, form)
		if tc != 200 {
			fmt.Fprintf(os.Stderr, "❌ 换取 token 失败（HTTP %d）: %s\n", tc, clip(string(tb), 300))
			os.Exit(1)
		}
		if err := applyTokenResponse(t, tb); err != nil {
			fmt.Fprintln(os.Stderr, "❌ 保存会话失败:", err)
			os.Exit(1)
		}
		fmt.Println("✅ 登录成功，会话已保存到", tokenFile())
		printWhoami()
	case <-time.After(5 * time.Minute):
		fmt.Fprintln(os.Stderr, "❌ 等待超时（5 分钟未完成授权）")
		os.Exit(1)
	}
}

func cmdLogout(args []string) {
	if err := os.Remove(tokenFile()); err != nil {
		fmt.Println("没有已保存的会话")
		return
	}
	fmt.Println("✅ 已退出登录，会话已删除")
}
