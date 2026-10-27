// helpdesk.go — ZURU helpdesk MCP 客户端 + 工单查询子命令。
//
// 认证：走 helpdesk 自己的授权服务器（auth.go），token 最终由 ZURU Entra 签发。
// MCP：streamable-http，直接 POST helpdesk.zuru.app/mcp/ 调 helpdesk 工具。
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// ── MCP 协议层 ──────────────────────────────────────────────

// mcpRequest 发一个 JSON-RPC 请求到 helpdesk MCP，返回 result 的原始 JSON。
func mcpRequest(method string, params any) ([]byte, error) {
	return mcpRequestAuth(method, params, false)
}

func mcpRequestAuth(method string, params any, retried bool) ([]byte, error) {
	h, err := bearerToken()
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		payload["params"] = params
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", mcpEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", h)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 401 && !retried && forceRefresh() {
		return mcpRequestAuth(method, params, true)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("MCP %s HTTP %d: %s", method, resp.StatusCode, clip(string(b), 300))
	}
	return extractResult(b)
}

// extractResult 从 MCP 响应（纯 JSON 或 SSE）里提取 result 字段。
func extractResult(b []byte) ([]byte, error) {
	raw := string(b)
	if json.Valid(b) {
		if res, err := parseResult(b); err == nil {
			return res, nil
		}
	}
	// SSE：收集 data: 行，逐个尝试解析。
	var last []byte
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		d := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if d == "" || d == "[DONE]" {
			continue
		}
		last = []byte(d)
		if res, err := parseResult([]byte(d)); err == nil {
			return res, nil
		}
	}
	if last != nil {
		if res, err := parseResult(last); err == nil {
			return res, nil
		}
	}
	return nil, fmt.Errorf("无法解析 MCP 响应: %s", clip(raw, 300))
}

func parseResult(b []byte) ([]byte, error) {
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.Error.Message != "" {
		return nil, errors.New(r.Error.Message)
	}
	if len(r.Result) == 0 || string(r.Result) == "null" {
		return nil, errors.New("无 result")
	}
	return r.Result, nil
}

// mcpCall 调一个 helpdesk 工具（tools/call），返回其文本负载的原始 JSON。
func mcpCall(name string, args map[string]any) ([]byte, error) {
	raw, err := mcpRequest("tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if len(r.Content) == 0 || r.Content[0].Text == "" {
		return nil, fmt.Errorf("工具 %s 没有返回文本内容", name)
	}
	return []byte(r.Content[0].Text), nil
}

// mcpCallInto 调工具并直接解码进 out。
func mcpCallInto(name string, args map[string]any, out any) error {
	b, err := mcpCall(name, args)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// ── 身份 ───────────────────────────────────────────────────

// tokenEmail 从 access token（JWT）payload 里解出邮箱，供 whoami / --mine 使用。
func tokenEmail() string {
	t, err := loadToken()
	if err != nil || t.AccessToken == "" {
		return ""
	}
	parts := strings.Split(t.AccessToken, ".")
	if len(parts) < 2 {
		return ""
	}
	for _, dec := range []func(string) ([]byte, error){
		base64.RawURLEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
	} {
		if b, e := dec(parts[1]); e == nil {
			var m map[string]any
			if json.Unmarshal(b, &m) == nil {
				for _, k := range []string{"email", "upn", "preferred_username"} {
					if v, ok := m[k].(string); ok && v != "" {
						return v
					}
				}
			}
		}
	}
	return ""
}

// printWhoami 由 auth.go 的 login 调用；用 get_person 解析当前身份。
func printWhoami() {
	email := tokenEmail()
	if email == "" {
		return
	}
	var out struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		PersonID    string `json:"person_id"`
		Department  string `json:"department"`
		JobTitle    string `json:"job_title"`
	}
	if err := mcpCallInto("get_person", map[string]any{"email": email}, &out); err == nil && out.Email != "" {
		name := out.DisplayName
		if name == "" {
			name = out.Email
		}
		fmt.Printf("   身份：%s（%s）\n", out.Email, name)
	}
}

func cmdWhoami(args []string) {
	email := tokenEmail()
	if email == "" {
		fatal("无法从 token 解析身份，先 zuru-helpdesk login")
	}
	var out struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		PersonID    string `json:"person_id"`
		Department  string `json:"department"`
		JobTitle    string `json:"job_title"`
	}
	if err := mcpCallInto("get_person", map[string]any{"email": email}, &out); err != nil {
		fatal("查询身份失败：%v", err)
	}
	name := out.DisplayName
	if name == "" {
		name = out.Email
	}
	fmt.Printf("身份：%s（%s）\n", out.Email, name)
	if out.PersonID != "" {
		fmt.Printf("  person_id=%s\n", out.PersonID)
	}
	if out.Department != "" {
		fmt.Printf("  部门=%s\n", out.Department)
	}
	if out.JobTitle != "" {
		fmt.Printf("  职位=%s\n", out.JobTitle)
	}
}

// ── tools（列出 MCP 实际暴露的工具，用于校准工具名）──────────

func cmdTools(args []string) {
	raw, err := mcpRequest("tools/list", nil)
	if err != nil {
		fatal("tools/list 失败（先 login）：%v", err)
	}
	var r struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		fatal("解析 tools/list 失败：%v", err)
	}
	fmt.Printf("helpdesk MCP 暴露 %d 个工具：\n\n", len(r.Tools))
	for _, t := range r.Tools {
		fmt.Printf("  %s\n", t.Name)
		if desc := strings.TrimSpace(t.Description); desc != "" {
			first := desc
			if i := strings.Index(first, "\n"); i > 0 {
				first = first[:i]
			}
			fmt.Printf("      %s\n", clip(first, 90))
		}
	}
}

// ── tickets（list_tickets）─────────────────────────────────

func csvList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func statusLabel(s string) string {
	switch s {
	case "new":
		return "新建"
	case "triaged":
		return "已分诊"
	case "in_progress":
		return "处理中"
	case "in_review":
		return "评审中"
	case "blocked":
		return "阻塞"
	case "escalated":
		return "已升级"
	case "resolved":
		return "已解决"
	case "closed":
		return "已关闭"
	default:
		return s
	}
}

func cmdTickets(args []string) {
	fs := flag.NewFlagSet("tickets", flag.ExitOnError)
	status := fs.String("status", "", "状态（逗号分隔：new/triaged/in_progress/in_review/blocked/escalated/resolved/closed）")
	priority := fs.String("priority", "", "优先级（逗号分隔：p1/p2/p3/p4）")
	ttype := fs.String("type", "", "类型（逗号分隔：INCIDENT/REQUEST/CHANGE/BUILD/PROBLEM）")
	service := fs.String("service", "", "服务（逗号分隔，tech-register 产品 slug）")
	tier := fs.String("tier", "", "层级（逗号分隔：l1/l2/l3）")
	tag := fs.String("tag", "", "标签（逗号分隔）")
	requester := fs.String("requester", "", "请求人（邮箱/姓名/person_id）")
	assignee := fs.String("assignee", "", "处理人（邮箱/姓名）")
	q := fs.String("q", "", "标题模糊搜索")
	breached := fs.Bool("breached", false, "只看 SLA 超时")
	mine := fs.Bool("mine", false, "只看我的（requester=当前身份）")
	limit := fs.Int("limit", 50, "条数（最大 200）")
	sort := fs.String("sort", "created_at_desc", "排序（created_at_desc/asc, updated_at_desc/asc, sla_due_at_asc/desc）")
	jsonOut := fs.Bool("json", false, "输出原始 JSON")
	_ = fs.Parse(args)

	filters := map[string]any{}
	if v := csvList(*status); v != nil {
		filters["status"] = v
	}
	if v := csvList(*priority); v != nil {
		filters["priority"] = v
	}
	if v := csvList(*ttype); v != nil {
		filters["type"] = v
	}
	if v := csvList(*service); v != nil {
		filters["service"] = v
	}
	if v := csvList(*tier); v != nil {
		filters["support_tier"] = v
	}
	if v := csvList(*tag); v != nil {
		filters["tag"] = v
	}
	if *requester != "" {
		filters["requester"] = *requester
	} else if *mine {
		if e := tokenEmail(); e != "" {
			filters["requester"] = e
		}
	}
	if *assignee != "" {
		filters["assignee"] = *assignee
	}
	if *q != "" {
		filters["q"] = *q
	}
	if *breached {
		filters["breached"] = true
	}
	if *limit > 200 {
		*limit = 200
	}
	filters["limit"] = *limit
	filters["sort"] = *sort

	var out struct {
		TotalCount int `json:"total_count"`
		Returned   int `json:"returned"`
		Limit      int `json:"limit"`
		Offset     int `json:"offset"`
		Tickets    []struct {
			TicketID      string  `json:"ticket_id"`
			Title         string  `json:"title"`
			Status        string  `json:"status"`
			Priority      string  `json:"priority"`
			SupportTier   *string `json:"support_tier"`
			Type          string  `json:"type"`
			Service       *string `json:"service"`
			Requester     string  `json:"requester"`
			Assignee      string  `json:"assignee"`
			SlaDueAt      *string `json:"sla_due_at"`
			CreatedAt     string  `json:"created_at"`
			UpdatedAt     string  `json:"updated_at"`
		} `json:"tickets"`
	}
	if err := mcpCallInto("list_tickets", filters, &out); err != nil {
		fatal("查询工单失败（先 login）：%v", err)
	}

	if *jsonOut {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return
	}

	if out.TotalCount == 0 {
		fmt.Println("没有匹配的工单。")
		return
	}
	fmt.Printf("共 %d 条工单（返回 %d）：\n\n", out.TotalCount, out.Returned)
	fmt.Println("  " + padRight("状态", 9) + padRight("优先级", 5) + padRight("类型", 9) + padRight("服务", 12) + padRight("处理人", 16) + "标题")
	for _, t := range out.Tickets {
		svc := "-"
		if t.Service != nil && *t.Service != "" {
			svc = *t.Service
		}
		asg := t.Assignee
		if asg == "" {
			asg = "-"
		}
		fmt.Println("  " + padRight(statusLabel(t.Status), 9) + padRight(t.Priority, 5) + padRight(t.Type, 9) + padRight(svc, 12) + padRight(asg, 16) + t.Title)
	}
}

// ── ticket（get_ticket 详情）───────────────────────────────

func cmdTicket(args []string) {
	fs := flag.NewFlagSet("ticket", flag.ExitOnError)
	id := fs.String("id", "", "工单 UUID")
	events := fs.Bool("events", false, "显示事件/审计日志")
	jsonOut := fs.Bool("json", false, "输出原始 JSON")
	_ = fs.Parse(args)

	pos := fs.Args()
	if *id == "" && len(pos) > 0 {
		*id = pos[0]
	}
	if *id == "" {
		fatal("ticket 需要工单 id（zuru-helpdesk ticket <id>）")
	}

	argsMap := map[string]any{"ticket_id": *id}
	if *events {
		argsMap["include_events"] = true
	}
	var out struct {
		Ticket        map[string]any   `json:"ticket"`
		StatusHistory []map[string]any `json:"status_history"`
		Sla           map[string]any   `json:"sla"`
		Events        []map[string]any `json:"events"`
	}
	if err := mcpCallInto("get_ticket", argsMap, &out); err != nil {
		fatal("查询工单详情失败：%v", err)
	}

	if *jsonOut {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return
	}

	t := out.Ticket
	get := func(k string) string {
		if v, ok := t[k].(string); ok {
			return v
		}
		return ""
	}
	fmt.Printf("工单：%s\n", get("title"))
	fmt.Printf("  id=%s\n", get("ticket_id"))
	fmt.Printf("  状态=%s  优先级=%s  类型=%s\n", statusLabel(get("status")), get("priority"), get("type"))
	if s := get("service"); s != "" {
		fmt.Printf("  服务=%s\n", s)
	}
	fmt.Printf("  请求人=%s\n", get("requester"))
	if a := get("assignee"); a != "" {
		fmt.Printf("  处理人=%s\n", a)
	}
	if d := get("description"); d != "" {
		fmt.Printf("\n描述：\n%s\n", d)
	}
	if len(out.StatusHistory) > 0 {
		fmt.Printf("\n状态历史：\n")
		for _, h := range out.StatusHistory {
			from, _ := h["from_status"].(string)
			to, _ := h["to_status"].(string)
			at, _ := h["changed_at"].(string)
			fmt.Printf("  %s → %s（%s）\n", statusLabel(from), statusLabel(to), at)
		}
	}
	if out.Sla != nil {
		if resp, ok := out.Sla["response"].(map[string]any); ok {
			if state, _ := resp["state"].(string); state != "" {
				fmt.Printf("\nSLA 响应=%s\n", state)
			}
		}
		if reso, ok := out.Sla["resolution"].(map[string]any); ok {
			if state, _ := reso["state"].(string); state != "" {
				fmt.Printf("SLA 解决=%s\n", state)
			}
		}
	}
	if *events && len(out.Events) > 0 {
		fmt.Printf("\n事件（%d）：\n", len(out.Events))
		for _, e := range out.Events {
			et, _ := e["event_type"].(string)
			at, _ := e["occurred_at"].(string)
			fmt.Printf("  %s  %s\n", at, et)
		}
	}
}

// ── sla（get_sla_status）───────────────────────────────────

func cmdSla(args []string) {
	fs := flag.NewFlagSet("sla", flag.ExitOnError)
	id := fs.String("id", "", "工单 UUID")
	jsonOut := fs.Bool("json", false, "输出原始 JSON")
	_ = fs.Parse(args)
	pos := fs.Args()
	if *id == "" && len(pos) > 0 {
		*id = pos[0]
	}
	if *id == "" {
		fatal("sla 需要工单 id")
	}
	var out map[string]any
	if err := mcpCallInto("get_sla_status", map[string]any{"ticket_id": *id}, &out); err != nil {
		fatal("查询 SLA 失败：%v", err)
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return
	}
	resp, _ := out["response"].(map[string]any)
	reso, _ := out["resolution"].(map[string]any)
	if resp == nil && reso == nil {
		fmt.Println("该工单没有 SLA 政策。")
		return
	}
	if resp != nil {
		state, _ := resp["state"].(string)
		fmt.Printf("响应 SLA：%s\n", state)
	}
	if reso != nil {
		state, _ := reso["state"].(string)
		fmt.Printf("解决 SLA：%s\n", state)
	}
}

// ── 公共辅助（供 auth.go 引用）────────────────────────────

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "❌ "+format+"\n", a...)
	os.Exit(1)
}

// clip 截断长字符串（按 rune 计）。
func clip(s string, maxRunes int) string {
	rs := []rune(s)
	if len(rs) <= maxRunes {
		return s
	}
	return string(rs[:maxRunes]) + "…"
}

// dispWidth 按终端显示宽度计算（CJK 等全角字符计 2 列）。
func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		if r >= 0x2E80 {
			w += 2
		} else {
			w++
		}
	}
	return w
}

func padRight(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-dispWidth(s)))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
