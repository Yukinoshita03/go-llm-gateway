package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

type fixedChunkReader struct {
	data  []byte
	sizes []int
	index int
	pos   int
}

func (r *fixedChunkReader) Read(p []byte) (int, error) {
	if r.pos == len(r.data) {
		return 0, io.EOF
	}

	size := len(p)
	if r.index < len(r.sizes) && r.sizes[r.index] < size {
		size = r.sizes[r.index]
	}
	r.index++
	if remaining := len(r.data) - r.pos; size > remaining {
		size = remaining
	}

	n := copy(p[:size], r.data[r.pos:r.pos+size])
	r.pos += n
	return n, nil
}

func TestForwardDeepSeekSSEHandlesReadBoundaries(t *testing.T) {
	first := `data: {"choices":[{"delta":{"role":"assistant","content":"你"}}]}` + "\n\n"
	second := `data: {"choices":[{"delta":{"content":"好"}}]}` + "\n\n"
	done := "data: [DONE]\n\n"
	input := first + second + done

	reader := &fixedChunkReader{
		data:  []byte(input),
		sizes: []int{1, 2, 1, 3, 5, 2, 7, 1, 4},
	}
	var output bytes.Buffer

	sawDone, err := forwardDeepSeekSSE(reader, &output, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !sawDone {
		t.Fatal("stream completed without observing [DONE]")
	}
	if output.String() != input {
		t.Fatalf("forwarded SSE = %q, want %q", output.String(), input)
	}
}

func TestDecodeDeepSeekSSEDataExtractsDeltaContent(t *testing.T) {
	done, content, err := decodeDeepSeekSSEData(`{"choices":[{"delta":{"content":"hello"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("ordinary chunk marked as done")
	}
	if content != "hello" {
		t.Fatalf("content = %q, want hello", content)
	}

	done, content, err = decodeDeepSeekSSEData("[DONE]")
	if err != nil {
		t.Fatal(err)
	}
	if !done || content != "" {
		t.Fatalf("done = %v, content = %q, want true and empty content", done, content)
	}
}

func TestForwardDeepSeekSSERequiresDone(t *testing.T) {
	input := `data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n"
	var output bytes.Buffer

	sawDone, err := forwardDeepSeekSSE(strings.NewReader(input), &output, 1<<20)
	if !errors.Is(err, errDeepSeekSSEDoneMissing) {
		t.Fatalf("error = %v, want errDeepSeekSSEDoneMissing", err)
	}
	if sawDone {
		t.Fatal("stream without [DONE] marked as complete")
	}
	if output.String() != input {
		t.Fatalf("already complete event was not forwarded: %q", output.String())
	}
}

func TestForwardDeepSeekSSERejectsMalformedChunk(t *testing.T) {
	input := "data: not-json\n\n"
	var output bytes.Buffer

	if _, err := forwardDeepSeekSSE(strings.NewReader(input), &output, 1<<20); err == nil {
		t.Fatal("malformed SSE JSON accepted")
	}
	if output.Len() != 0 {
		t.Fatalf("malformed event was forwarded: %q", output.String())
	}
}

func TestForwardDeepSeekSSERejectsOversizedEventBeforeForwarding(t *testing.T) {
	input := `data: {"choices":[{"delta":{"content":"hello"}}]}` + "\n\n"
	var output bytes.Buffer

	if _, err := forwardDeepSeekSSE(strings.NewReader(input), &output, int64(len(input)-1)); err == nil {
		t.Fatal("oversized SSE event accepted")
	}
	if output.Len() != 0 {
		t.Fatalf("oversized event was forwarded: %q", output.String())
	}
}
