package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

var (
	errDeepSeekSSEDoneMissing = errors.New("DeepSeek SSE stream ended before [DONE]")
	errDeepSeekSSETooLarge    = errors.New("DeepSeek SSE stream exceeds the configured limit")
)

type deepSeekStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// decodeDeepSeekSSEData 校验一个 data 字段，并提取首个候选的增量文本。
// 返回 done=true 表示收到了 DeepSeek 的终止事件 [DONE]。
func decodeDeepSeekSSEData(data string) (done bool, content string, err error) {
	data = strings.TrimSpace(data)
	if data == "[DONE]" {
		return true, "", nil
	}

	var chunk deepSeekStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return false, "", fmt.Errorf("decode DeepSeek SSE data: %w", err)
	}
	if len(chunk.Choices) == 0 {
		return false, "", errors.New("DeepSeek SSE data has no choices")
	}
	return false, chunk.Choices[0].Delta.Content, nil
}

// forwardDeepSeekSSE 按 SSE 空行边界读取上游事件，校验事件后再原样转发。
// 它不把 Read 的边界当   作事件边界，也不会把完整流读入内存。
func forwardDeepSeekSSE(src io.Reader, dst io.Writer, maxBytes int64) (bool, error) {
	if maxBytes <= 0 {
		return false, errDeepSeekSSETooLarge
	}

	// 多读一个字节用于识别“刚好达到上限”和“超过上限”，但探测字节不会转发。
	limited := &io.LimitedReader{R: src, N: maxBytes + 1}
	reader := bufio.NewReader(limited)
	var event bytes.Buffer
	var forwarded int64

	for {
		remaining := maxBytes + 1 - forwarded - int64(event.Len())
		line, terminated, err := readSSELine(reader, remaining)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if limited.N == 0 {
					return false, errDeepSeekSSETooLarge
				}
				return false, errDeepSeekSSEDoneMissing
			}
			return false, err
		}
		event.Write(line)
		if !terminated {
			if limited.N == 0 {
				return false, errDeepSeekSSETooLarge
			}
			return false, io.ErrUnexpectedEOF
		}

		if !isSSEBlankLine(line) {
			continue
		}
		// 弄完了
		if forwarded+int64(event.Len()) > maxBytes {
			return false, errDeepSeekSSETooLarge
		}
		data, hasData := sseEventData(event.Bytes())
		isDone := false
		if hasData {
			var content string
			isDone, content, err = decodeDeepSeekSSEData(data)
			if err != nil {
				return false, err
			}
			// content 是本层解析出的增量文本；当前网关仍原样转发上游 SSE。
			_ = content
		}

		written, err := dst.Write(event.Bytes())
		forwarded += int64(written)
		if err != nil {
			return false, fmt.Errorf("write downstream SSE: %w", err)
		}
		if written != event.Len() {
			return false, io.ErrShortWrite
		}
		event.Reset()
		if isDone {
			return true, nil
		}
	}
}

func readSSELine(reader *bufio.Reader, maxBytes int64) ([]byte, bool, error) {
	if maxBytes <= 0 {
		return nil, false, errDeepSeekSSETooLarge
	}

	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if int64(len(line)+len(part)) > maxBytes {
			return nil, false, errDeepSeekSSETooLarge
		}
		line = append(line, part...)
		switch err {
		case nil:
			return line, true, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(line) == 0 {
				return nil, false, io.EOF
			}
			return line, false, nil
		default:
			return nil, false, err
		}
	}
}

func isSSEBlankLine(line []byte) bool {
	return bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n"))
}

func sseEventData(event []byte) (string, bool) {
	var values []string
	for _, line := range bytes.Split(event, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) < len("data:") || !bytes.Equal(line[:len("data:")], []byte("data:")) {
			continue
		}
		value := line[len("data:"):]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		values = append(values, string(value))
	}
	if len(values) == 0 {
		return "", false
	}
	return strings.Join(values, "\n"), true
}
