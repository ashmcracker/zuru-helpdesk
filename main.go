// zuru-helpdesk — ZURU Helpdesk 本地 CLI（Go 版）
//
// 让工程师 / AI 通过命令行查询 ZURU helpdesk 工单（tickets / ticket / sla / whoami）。
// 认证：走 helpdesk 授权服务器 + ZURU Entra 登录（`login` 浏览器授权一次，长期自动续期）。
package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Print(`zuru-helpdesk — ZURU Helpdesk 本地 CLI

子命令：
  login    浏览器授权一次，会话长期自动续期（走 ZURU Entra）
  logout   删除已保存的会话
  whoami   查看当前登录身份
  tickets  查询工单列表（多条件过滤）
  ticket   查看单个工单详情（含状态历史 / SLA / 事件）
  sla      查看单个工单的 SLA 状态
  tools    列出 helpdesk MCP 实际暴露的工具（校准用）

认证：
  login 保存的会话（~/.zuru-helpdesk/token.json，自动刷新）
  登录会跳转到 Microsoft Entra（ZURU 账号），一次授权长期有效。

示例：
  zuru-helpdesk login
  zuru-helpdesk whoami
  zuru-helpdesk tickets --mine
  zuru-helpdesk tickets --status in_progress,blocked --priority p1,p2
  zuru-helpdesk tickets --type CHANGE
  zuru-helpdesk ticket abf6035d-0ad9-4930-af30-da0b6205e837 --events
  zuru-helpdesk sla abf6035d-0ad9-4930-af30-da0b6205e837

各子命令的完整参数，用 "<子命令> -h" 查看。
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "login":
		cmdLogin(os.Args[2:])
	case "logout":
		cmdLogout(os.Args[2:])
	case "whoami":
		cmdWhoami(os.Args[2:])
	case "tickets":
		cmdTickets(os.Args[2:])
	case "ticket":
		cmdTicket(os.Args[2:])
	case "sla":
		cmdSla(os.Args[2:])
	case "tools":
		cmdTools(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知子命令：%s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}
