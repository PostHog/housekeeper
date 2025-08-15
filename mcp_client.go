package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sirupsen/logrus"
)

// MCPClient represents a client connection to an MCP server
type MCPClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	reader *bufio.Reader
	
	reqID  atomic.Uint64
	reqMu  sync.Mutex
	reqs   map[string]chan json.RawMessage
	
	tools  []MCPTool
	toolMu sync.RWMutex
}

// MCPTool represents a tool exposed by the MCP server
type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// MCPRequest represents a JSON-RPC request
type MCPRequest struct {
	Jsonrpc string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// MCPResponse represents a JSON-RPC response
type MCPResponse struct {
	Jsonrpc string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *MCPError       `json:"error,omitempty"`
}

// MCPError represents a JSON-RPC error
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// NewMCPClient creates a new MCP client connected to the housekeeper server
func NewMCPClient(args []string) (*MCPClient, error) {
	// Build command with provided args
	cmd := exec.Command("housekeeper", args...)
	
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start MCP server: %w", err)
	}
	
	client := &MCPClient{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		reader: bufio.NewReader(stdout),
		reqs:   make(map[string]chan json.RawMessage),
	}
	
	// Start reading stderr for logs
	go client.readStderr()
	
	// Start response reader
	go client.readResponses()
	
	// Initialize the connection
	if err := client.initialize(); err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to initialize MCP connection: %w", err)
	}
	
	// List available tools
	if err := client.listTools(); err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to list tools: %w", err)
	}
	
	return client, nil
}

// readStderr reads and logs stderr output from the MCP server
func (c *MCPClient) readStderr() {
	scanner := bufio.NewScanner(c.stderr)
	for scanner.Scan() {
		logrus.WithField("source", "mcp_server").Debug(scanner.Text())
	}
}

// readResponses reads JSON-RPC responses from the MCP server
func (c *MCPClient) readResponses() {
	for {
		// Read Content-Length header
		line, err := c.reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				logrus.WithError(err).Error("Failed to read from MCP server")
			}
			break
		}
		
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Content-Length: ") {
			continue
		}
		
		lengthStr := strings.TrimPrefix(line, "Content-Length: ")
		contentLength, err := strconv.Atoi(lengthStr)
		if err != nil {
			logrus.WithError(err).Error("Invalid content length")
			continue
		}
		
		// Read empty line after header
		c.reader.ReadString('\n')
		
		// Read the JSON body
		body := make([]byte, contentLength)
		if _, err := io.ReadFull(c.reader, body); err != nil {
			logrus.WithError(err).Error("Failed to read response body")
			continue
		}
		
		// Parse response
		var resp MCPResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			logrus.WithError(err).Error("Failed to parse response")
			continue
		}
		
		// Route response to waiting channel
		if resp.ID != nil {
			c.reqMu.Lock()
			if ch, ok := c.reqs[fmt.Sprint(resp.ID)]; ok {
				if resp.Error != nil {
					logrus.WithField("error", resp.Error).Error("MCP request failed")
					ch <- nil
				} else {
					ch <- resp.Result
				}
				delete(c.reqs, fmt.Sprint(resp.ID))
			}
			c.reqMu.Unlock()
		}
	}
}

// sendRequest sends a JSON-RPC request and waits for the response
func (c *MCPClient) sendRequest(method string, params interface{}) (json.RawMessage, error) {
	id := c.reqID.Add(1)
	idStr := fmt.Sprint(id)
	
	req := MCPRequest{
		Jsonrpc: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	
	// Create response channel
	respCh := make(chan json.RawMessage, 1)
	c.reqMu.Lock()
	c.reqs[idStr] = respCh
	c.reqMu.Unlock()
	
	// Marshal request
	reqJSON, err := json.Marshal(req)
	if err != nil {
		c.reqMu.Lock()
		delete(c.reqs, idStr)
		c.reqMu.Unlock()
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	
	// Send request with Content-Length header
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(reqJSON))
	if _, err := c.stdin.Write([]byte(header)); err != nil {
		c.reqMu.Lock()
		delete(c.reqs, idStr)
		c.reqMu.Unlock()
		return nil, fmt.Errorf("failed to write header: %w", err)
	}
	
	if _, err := c.stdin.Write(reqJSON); err != nil {
		c.reqMu.Lock()
		delete(c.reqs, idStr)
		c.reqMu.Unlock()
		return nil, fmt.Errorf("failed to write request: %w", err)
	}
	
	// Wait for response
	result := <-respCh
	return result, nil
}

// initialize sends the initialize request to the MCP server
func (c *MCPClient) initialize() error {
	params := map[string]interface{}{
		"protocolVersion": "0.1.0",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "housekeeper-slack-bot",
			"version": "1.0.0",
		},
	}
	
	_, err := c.sendRequest("initialize", params)
	return err
}

// listTools retrieves the available tools from the MCP server
func (c *MCPClient) listTools() error {
	result, err := c.sendRequest("tools/list", nil)
	if err != nil {
		return err
	}
	
	var toolsResp struct {
		Tools []MCPTool `json:"tools"`
	}
	if err := json.Unmarshal(result, &toolsResp); err != nil {
		return fmt.Errorf("failed to parse tools response: %w", err)
	}
	
	c.toolMu.Lock()
	c.tools = toolsResp.Tools
	c.toolMu.Unlock()
	
	logrus.WithField("tools", len(c.tools)).Info("MCP tools loaded")
	for _, tool := range c.tools {
		logrus.WithField("tool", tool.Name).Debug(tool.Description)
	}
	
	return nil
}

// CallTool calls a tool on the MCP server
func (c *MCPClient) CallTool(toolName string, arguments interface{}) (json.RawMessage, error) {
	params := map[string]interface{}{
		"name":      toolName,
		"arguments": arguments,
	}
	
	result, err := c.sendRequest("tools/call", params)
	if err != nil {
		return nil, fmt.Errorf("tool call failed: %w", err)
	}
	
	return result, nil
}

// GetTools returns the list of available tools
func (c *MCPClient) GetTools() []MCPTool {
	c.toolMu.RLock()
	defer c.toolMu.RUnlock()
	return c.tools
}

// Close shuts down the MCP client connection
func (c *MCPClient) Close() error {
	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Kill()
	}
	return nil
}