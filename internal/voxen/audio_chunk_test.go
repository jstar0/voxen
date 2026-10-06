package voxen

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// fakeMP3 builds n valid MPEG-1 Layer III frames at 44.1 kHz, 128 kbps, mono.
func fakeMP3(n int, fill byte) []byte {
	var b []byte
	for range n {
		frame := make([]byte, 417)
		copy(frame, []byte{0xFF, 0xFB, 0x90, 0xC0})
		for i := 4; i < len(frame); i++ {
			frame[i] = fill
		}
		b = append(b, frame...)
	}
	return b
}

func fakeWAV(rate uint32, samples []byte) []byte {
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(samples)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], rate)
	binary.LittleEndian.PutUint32(h[28:], rate*2)
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(samples)))
	return append(h, samples...)
}

func oggPageBytes(headerType byte, granule uint64, serial, seq uint32, body []byte) []byte {
	p := make([]byte, 27+1, 28+len(body))
	copy(p, "OggS")
	p[5] = headerType
	binary.LittleEndian.PutUint64(p[6:], granule)
	binary.LittleEndian.PutUint32(p[14:], serial)
	binary.LittleEndian.PutUint32(p[18:], seq)
	p[26] = 1
	p[27] = byte(len(body))
	p = append(p, body...)
	binary.LittleEndian.PutUint32(p[22:], oggCRC(p))
	return p
}

// fakeOpus is one Opus stream: head, tags and two audio pages ending at granule.
func fakeOpus(serial uint32, granule uint64) []byte {
	head := append([]byte("OpusHead"), 1, 1, 0x38, 0x01, 0xC0, 0x5D, 0, 0, 0, 0, 0) // pre-skip 312, 24000 Hz
	var out []byte
	out = append(out, oggPageBytes(2, 0, serial, 0, head)...)
	out = append(out, oggPageBytes(0, 0, serial, 1, []byte("OpusTags"))...)
	out = append(out, oggPageBytes(0, granule/2, serial, 2, []byte{1, 2, 3})...)
	out = append(out, oggPageBytes(4, granule, serial, 3, []byte{4, 5, 6})...)
	return out
}

func TestSplitTextKeepsSentencesAndTagsIntact(t *testing.T) {
	text := "开头一句。<whisper>悄悄话第一句。悄悄话第二句。悄悄话第三句。</whisper>结尾一句[laugh]。"
	chunks := splitText(text, 14)
	if len(chunks) < 3 {
		t.Fatalf("expected several chunks, got %q", chunks)
	}
	for _, c := range chunks {
		if strings.Count(c, "<whisper>") != strings.Count(c, "</whisper>") {
			t.Fatalf("unbalanced wrapper in %q (all: %q)", c, chunks)
		}
		if strings.Contains(c, "<whisper></whisper>") {
			t.Fatalf("empty wrapper in %q", c)
		}
		if strings.Count(c, "[") != strings.Count(c, "]") {
			t.Fatalf("inline tag was cut in %q", c)
		}
	}
	joined := strings.Join(chunks, "")
	for _, want := range []string{"开头一句。", "悄悄话第一句。", "悄悄话第三句。", "结尾一句", "[laugh]"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("lost %q in %q", want, chunks)
		}
	}
}

func TestSplitTextShortAndLongSentences(t *testing.T) {
	if got := splitText("短文本。", 100); len(got) != 1 || got[0] != "短文本。" {
		t.Fatalf("got %q", got)
	}
	if got := splitText("whatever", 0); len(got) != 1 {
		t.Fatalf("max 0 must not split: %q", got)
	}
	long := strings.Repeat("没有句号的很长的一段话，", 20)
	chunks := splitText(long, 30)
	for _, c := range chunks {
		if n := len([]rune(c)); n > 30 {
			t.Fatalf("chunk of %d runes exceeds limit: %q", n, c)
		}
	}
	if strings.Join(chunks, "") != long {
		t.Fatalf("hard split changed the text")
	}
}

func TestInspectAndConcatMP3(t *testing.T) {
	a, b := fakeMP3(10, 1), fakeMP3(20, 2)
	info := Inspect(a, Request{})
	want := 10 * 1152.0 / 44100
	if info.Format != "mp3" || info.SampleRate != 44100 || info.BitRate != 128000 || info.Channels != 1 || math.Abs(info.Duration-want) > 1e-6 {
		t.Fatalf("info = %+v", info)
	}
	joined, err := ConcatAudio([][]byte{a, b})
	if err != nil || !bytes.Equal(joined, append(append([]byte{}, a...), b...)) {
		t.Fatalf("mp3 join err=%v", err)
	}
	if got := Inspect(joined, Request{}).Duration; math.Abs(got-30*1152.0/44100) > 1e-6 {
		t.Fatalf("joined duration = %v", got)
	}
}

func TestInspectAndConcatWAV(t *testing.T) {
	a, b := fakeWAV(16000, make([]byte, 32000)), fakeWAV(16000, bytes.Repeat([]byte{7}, 16000))
	if info := Inspect(a, Request{}); info.Format != "wav" || info.SampleRate != 16000 || math.Abs(info.Duration-1) > 1e-9 {
		t.Fatalf("info = %+v", info)
	}
	joined, err := ConcatAudio([][]byte{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if info := Inspect(joined, Request{}); math.Abs(info.Duration-1.5) > 1e-9 || len(joined) != 44+48000 {
		t.Fatalf("joined info = %+v len=%d", info, len(joined))
	}
	if _, err := ConcatAudio([][]byte{a, fakeWAV(8000, make([]byte, 100))}); err == nil {
		t.Fatalf("different WAV formats must not be joined")
	}
}

func TestInspectAndConcatOpus(t *testing.T) {
	a, b := fakeOpus(11, 48312), fakeOpus(22, 96312) // 1 s and 2 s after pre-skip
	if info := Inspect(a, Request{}); info.Format != "opus" || math.Abs(info.Duration-1) > 1e-9 || info.Channels != 1 {
		t.Fatalf("info = %+v", info)
	}
	joined, err := ConcatAudio([][]byte{a, b})
	if err != nil {
		t.Fatal(err)
	}
	pages, err := readOggPages(joined)
	if err != nil || len(pages) != 6 {
		t.Fatalf("pages=%d err=%v", len(pages), err)
	}
	for i, p := range pages {
		if got := binary.LittleEndian.Uint32(p.raw[14:]); got != 11 {
			t.Fatalf("page %d serial %d", i, got)
		}
		if got := binary.LittleEndian.Uint32(p.raw[18:]); got != uint32(i) {
			t.Fatalf("page %d sequence %d", i, got)
		}
		crc := binary.LittleEndian.Uint32(p.raw[22:])
		check := append([]byte(nil), p.raw...)
		binary.LittleEndian.PutUint32(check[22:], 0)
		if oggCRC(check) != crc {
			t.Fatalf("page %d checksum is wrong", i)
		}
		if end := p.headerType&4 != 0; end != (i == len(pages)-1) {
			t.Fatalf("page %d end-of-stream flag = %v", i, end)
		}
	}
	if info := Inspect(joined, Request{}); math.Abs(info.Duration-(144624.0-312)/48000) > 1e-9 {
		t.Fatalf("joined duration = %v", info.Duration)
	}
}

func TestInspectHeaderlessAudioIsAssumed(t *testing.T) {
	info := Inspect(make([]byte, 48000), Request{Format: "mulaw", SampleRate: 8000})
	if info.Format != "mulaw" || !info.Assumed || math.Abs(info.Duration-6) > 1e-9 {
		t.Fatalf("info = %+v", info)
	}
	if info := Inspect(make([]byte, 48000), Request{Format: "ulaw"}); info.Format != "mulaw" {
		t.Fatalf("ulaw alias: %+v", info)
	}
}
