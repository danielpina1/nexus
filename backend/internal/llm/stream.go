package llm

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

var errStreamDone = errors.New("stream done")

// readSSE reads a text/event-stream body and calls handle with each event's
// data payload. "[DONE]" ends the stream. Comment lines and non-data fields
// are ignored. Data lines are joined per event; if a joined payload is not
// valid JSON (servers that omit blank separators) each line is delivered on
// its own.
func readSSE(r io.Reader, handle func(data []byte) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var lines []string
	dispatch := func() error {
		if len(lines) == 0 {
			return nil
		}
		defer func() { lines = lines[:0] }()
		joined := strings.Join(lines, "\n")
		if len(lines) > 1 && !json.Valid([]byte(joined)) {
			for _, l := range lines {
				if err := deliver(l, handle); err != nil {
					return err
				}
			}
			return nil
		}
		return deliver(joined, handle)
	}
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if derr := dispatch(); derr != nil {
					if derr == errStreamDone {
						return nil
					}
					return derr
				}
			case strings.HasPrefix(line, ":"):
				// comment / keep-alive
			case strings.HasPrefix(line, "data:"):
				lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			default:
				// event:, id:, retry: — not used by OpenAI-compatible streams.
			}
		}
		if err == io.EOF {
			if derr := dispatch(); derr != nil && derr != errStreamDone {
				return derr
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func deliver(payload string, handle func([]byte) error) error {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil
	}
	if payload == "[DONE]" {
		return errStreamDone
	}
	return handle([]byte(payload))
}

type wireChunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls []wireToolCall  `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage      `json:"usage"`
	Error json.RawMessage `json:"error"`
}

type toolBuilder struct {
	index int
	id    string
	name  strings.Builder
	args  strings.Builder
}

// streamAccumulator assembles content and tool calls from chunks.
type streamAccumulator struct {
	onDelta  func(string)
	content  strings.Builder
	calls    map[int]*toolBuilder
	lastIdx  int
	nextAuto int
	usage    *wireUsage
	finish   string
}

func newStreamAccumulator(onDelta func(string)) *streamAccumulator {
	return &streamAccumulator{onDelta: onDelta, calls: map[int]*toolBuilder{}, lastIdx: -1}
}

func (a *streamAccumulator) handle(data []byte) error {
	var ch wireChunk
	if err := json.Unmarshal(data, &ch); err != nil {
		return fmt.Errorf("bad chunk %q: %w", truncate(string(data), 200), err)
	}
	if len(ch.Error) > 0 && string(ch.Error) != "null" {
		return &APIError{Message: "stream error: " + errorMessage([]byte(`{"error":`+string(ch.Error)+`}`))}
	}
	if ch.Usage != nil {
		a.usage = ch.Usage
	}
	for _, c := range ch.Choices {
		if c.Index != 0 {
			continue // only the first choice is used
		}
		if c.FinishReason != nil && *c.FinishReason != "" {
			a.finish = *c.FinishReason
		}
		if txt := contentText(c.Delta.Content); txt != "" {
			a.content.WriteString(txt)
			if a.onDelta != nil {
				a.onDelta(txt)
			}
		}
		for _, tc := range c.Delta.ToolCalls {
			idx := a.indexFor(tc)
			b, ok := a.calls[idx]
			if !ok {
				b = &toolBuilder{index: idx}
				a.calls[idx] = b
			}
			if tc.ID != "" {
				b.id = tc.ID
			}
			b.name.WriteString(tc.Function.Name)
			b.args.WriteString(tc.Function.Arguments)
		}
	}
	return nil
}

// indexFor picks the builder for a tool call delta: by explicit index, else
// by id (a new id starts a new call), else continue the last one.
func (a *streamAccumulator) indexFor(tc wireToolCall) int {
	if tc.Index != nil {
		a.lastIdx = *tc.Index
		if *tc.Index >= a.nextAuto {
			a.nextAuto = *tc.Index + 1
		}
		return *tc.Index
	}
	if tc.ID != "" {
		for i, b := range a.calls {
			if b.id == tc.ID {
				a.lastIdx = i
				return i
			}
		}
		a.lastIdx = a.nextAuto
		a.nextAuto++
		return a.lastIdx
	}
	if a.lastIdx < 0 {
		a.lastIdx = a.nextAuto
		a.nextAuto++
	}
	return a.lastIdx
}

func (a *streamAccumulator) toolCalls() []*toolBuilder {
	out := make([]*toolBuilder, 0, len(a.calls))
	for _, b := range a.calls {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].index < out[j].index })
	return out
}

func asAPI(err error, target **APIError) bool { return errors.As(err, target) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
