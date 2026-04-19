package httplog

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ReassembleAnthropicSSE converts Anthropic-format SSE event bytes into the
// equivalent non-streaming JSON response. Returns the original data unchanged
// if parsing fails.
func ReassembleAnthropicSSE(sseData []byte) ([]byte, error) {
	var msg reassembledMessage
	msg.Type = "message"
	msg.Role = "assistant"

	var currentBlock *contentBlock
	scanner := newLineScanner(sseData)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		data := bytes.TrimPrefix(line, []byte("data: "))
		if bytes.Equal(data, []byte("[DONE]")) {
			continue
		}

		var evt sseEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			continue
		}

		switch evt.Type {
		case "message_start":
			if evt.Message != nil {
				msg.ID = evt.Message.ID
				msg.Model = evt.Message.Model
				if evt.Message.Role != "" {
					msg.Role = evt.Message.Role
				}
				msg.Usage.InputTokens = evt.Message.Usage.InputTokens
				msg.Usage.CacheCreationInputTokens = evt.Message.Usage.CacheCreationInputTokens
				msg.Usage.CacheReadInputTokens = evt.Message.Usage.CacheReadInputTokens
			}

		case "content_block_start":
			if evt.ContentBlock != nil {
				currentBlock = &contentBlock{Type: evt.ContentBlock.Type}
				if evt.ContentBlock.Type == "tool_use" {
					currentBlock.ID = evt.ContentBlock.ID
					currentBlock.Name = evt.ContentBlock.Name
				}
			}

		case "content_block_delta":
			if currentBlock != nil && evt.Delta != nil {
				switch evt.Delta.Type {
				case "text_delta":
					currentBlock.textBuf.WriteString(evt.Delta.Text)
				case "input_json_delta":
					currentBlock.jsonBuf.WriteString(evt.Delta.PartialJSON)
				case "thinking_delta":
					currentBlock.textBuf.WriteString(evt.Delta.Thinking)
				}
			}

		case "content_block_stop":
			if currentBlock != nil {
				msg.Content = append(msg.Content, currentBlock.finalize())
				currentBlock = nil
			}

		case "message_delta":
			if evt.Delta != nil {
				msg.StopReason = evt.Delta.StopReason
			}
			msg.Usage.OutputTokens = evt.Usage.OutputTokens
		}
	}

	return json.Marshal(msg)
}

type sseEvent struct {
	Type         string        `json:"type"`
	Message      *sseMessage   `json:"message,omitempty"`
	ContentBlock *sseBlock     `json:"content_block,omitempty"`
	Delta        *sseDelta     `json:"delta,omitempty"`
	Usage        sseUsage      `json:"usage,omitempty"`
}

type sseMessage struct {
	ID    string   `json:"id"`
	Model string   `json:"model"`
	Role  string   `json:"role"`
	Usage sseUsage `json:"usage"`
}

type sseBlock struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type sseDelta struct {
	Type        string `json:"type,omitempty"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

type sseUsage struct {
	InputTokens              int64 `json:"input_tokens,omitempty"`
	OutputTokens             int64 `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
}

type reassembledMessage struct {
	ID         string                   `json:"id"`
	Type       string                   `json:"type"`
	Role       string                   `json:"role"`
	Content    []map[string]interface{} `json:"content"`
	Model      string                   `json:"model"`
	StopReason string                   `json:"stop_reason,omitempty"`
	Usage      sseUsage                 `json:"usage"`
}

type contentBlock struct {
	Type    string
	ID      string
	Name    string
	textBuf strings.Builder
	jsonBuf strings.Builder
}

func (b *contentBlock) finalize() map[string]interface{} {
	m := map[string]interface{}{"type": b.Type}
	switch b.Type {
	case "text":
		m["text"] = b.textBuf.String()
	case "thinking":
		m["thinking"] = b.textBuf.String()
	case "tool_use":
		m["id"] = b.ID
		m["name"] = b.Name
		var input interface{}
		if json.Unmarshal([]byte(b.jsonBuf.String()), &input) == nil {
			m["input"] = input
		} else {
			m["input"] = json.RawMessage(b.jsonBuf.String())
		}
	}
	return m
}

type lineScanner struct {
	data []byte
	pos  int
	line []byte
}

func newLineScanner(data []byte) *lineScanner {
	return &lineScanner{data: data}
}

func (s *lineScanner) Scan() bool {
	if s.pos >= len(s.data) {
		return false
	}
	idx := bytes.IndexByte(s.data[s.pos:], '\n')
	if idx < 0 {
		s.line = s.data[s.pos:]
		s.pos = len(s.data)
		return len(s.line) > 0
	}
	s.line = s.data[s.pos : s.pos+idx]
	s.pos += idx + 1
	return true
}

func (s *lineScanner) Bytes() []byte {
	return s.line
}
