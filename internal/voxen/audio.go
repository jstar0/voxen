package voxen

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// AudioInfo is read from the audio bytes themselves, never from what was requested.
type AudioInfo struct {
	Format     string // mp3, wav, pcm, mulaw, alaw
	SampleRate int
	BitRate    int
	Channels   int
	Duration   float64
	Assumed    bool // duration derived from requested parameters, not from the stream
}

var (
	mp3Bitrates1 = []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	mp3Bitrates2 = []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
	mp3Rates     = map[int][]int{3: {44100, 48000, 32000}, 2: {22050, 24000, 16000}, 0: {11025, 12000, 8000}}
)

func extension(format string) string {
	switch format {
	case "mp3", "wav", "pcm", "opus":
		return "." + format
	case "mulaw", "ulaw":
		return ".ulaw"
	case "alaw":
		return ".alaw"
	}
	return ".audio"
}

func id3Size(b []byte) int {
	if len(b) >= 10 && string(b[:3]) == "ID3" {
		return 10 + int(b[6]&0x7f)<<21 | int(b[7]&0x7f)<<14 | int(b[8]&0x7f)<<7 | int(b[9]&0x7f)
	}
	return 0
}

type mp3Frame struct {
	length, sampleRate, bitRate, channels, samples int
}

func parseMP3Frame(b []byte) (mp3Frame, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return mp3Frame{}, false
	}
	version := int(b[1]>>3) & 3
	if version == 1 || (b[1]>>1)&3 != 1 { // reserved version, or not layer III
		return mp3Frame{}, false
	}
	bi, si := int(b[2]>>4), int(b[2]>>2)&3
	if bi == 0 || bi == 15 || si == 3 {
		return mp3Frame{}, false
	}
	bitrates := mp3Bitrates2
	samples, coeff := 576, 72
	if version == 3 {
		bitrates, samples, coeff = mp3Bitrates1, 1152, 144
	}
	rate := mp3Rates[version][si]
	bitRate := bitrates[bi] * 1000
	channels := 2
	if (b[3]>>6)&3 == 3 {
		channels = 1
	}
	return mp3Frame{length: coeff*bitRate/rate + int(b[2]>>1)&1, sampleRate: rate, bitRate: bitRate, channels: channels, samples: samples}, true
}

func detectFormat(b []byte) string {
	switch {
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return "wav"
	case len(b) >= 4 && string(b[:4]) == "OggS":
		if bytes.Contains(b[:min(len(b), 512)], []byte("OpusHead")) {
			return "opus"
		}
		return "ogg"
	case id3Size(b) > 0:
		return "mp3"
	}
	if _, ok := parseMP3Frame(b); ok {
		return "mp3"
	}
	return "raw"
}

type wavInfo struct {
	audioFormat, channels, bits uint16
	sampleRate                  uint32
	data                        []byte
}

func parseWAV(b []byte) (wavInfo, error) {
	var w wavInfo
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return w, fmt.Errorf("not a WAV file")
	}
	haveFmt := false
	for pos := 12; pos+8 <= len(b); {
		id, size := string(b[pos:pos+4]), int(binary.LittleEndian.Uint32(b[pos+4:pos+8]))
		body := pos + 8
		if id == "data" {
			end := body + size
			if size < 0 || end > len(b) || size == 0xFFFFFFFF {
				end = len(b)
			}
			w.data = b[body:end]
			if !haveFmt {
				return w, fmt.Errorf("WAV data chunk before fmt chunk")
			}
			return w, nil
		}
		if id == "fmt " && body+16 <= len(b) {
			w.audioFormat = binary.LittleEndian.Uint16(b[body:])
			w.channels = binary.LittleEndian.Uint16(b[body+2:])
			w.sampleRate = binary.LittleEndian.Uint32(b[body+4:])
			w.bits = binary.LittleEndian.Uint16(b[body+14:])
			haveFmt = true
		}
		pos = body + size + size&1
	}
	return w, fmt.Errorf("WAV has no data chunk")
}

// Inspect reads the format, sample rate, bit rate, channels and duration from the
// bytes. Headerless audio can only be described from the request.
func Inspect(b []byte, requested Request) AudioInfo {
	switch detectFormat(b) {
	case "opus", "ogg":
		return inspectOgg(b)
	case "wav":
		info := AudioInfo{Format: "wav"}
		if w, err := parseWAV(b); err == nil {
			info.SampleRate, info.Channels = int(w.sampleRate), int(w.channels)
			if bytesPerSecond := int(w.sampleRate) * int(w.channels) * int(w.bits) / 8; bytesPerSecond > 0 {
				info.Duration = float64(len(w.data)) / float64(bytesPerSecond)
				info.BitRate = bytesPerSecond * 8
			}
		}
		return info
	case "mp3":
		info := AudioInfo{Format: "mp3"}
		pos := id3Size(b)
		var seconds float64
		var bits, frames int
		bitRates := map[int]bool{}
		for pos+4 <= len(b) {
			f, ok := parseMP3Frame(b[pos:])
			if !ok {
				break
			}
			if frames == 0 {
				info.SampleRate, info.Channels = f.sampleRate, f.channels
			}
			frames++
			bitRates[f.bitRate] = true
			bits += f.bitRate
			seconds += float64(f.samples) / float64(f.sampleRate)
			pos += f.length
		}
		if frames > 0 {
			info.Duration = seconds
			info.BitRate = bits / frames
			if len(bitRates) == 1 {
				for rate := range bitRates {
					info.BitRate = rate
				}
			}
		}
		return info
	}
	format := requested.Format
	if format == "ulaw" {
		format = "mulaw"
	}
	if !oneOf(format, "pcm", "mulaw", "alaw") {
		format = "pcm"
	}
	rate := requested.SampleRate
	if rate == 0 {
		rate = 24000
	}
	bytesPerSample := 2
	if format != "pcm" {
		bytesPerSample = 1
	}
	return AudioInfo{Format: format, SampleRate: rate, Channels: 1, Duration: float64(len(b)) / float64(rate*bytesPerSample), Assumed: true}
}

// ConcatAudio joins audio chunks into one file of the same format.
func ConcatAudio(parts [][]byte) ([]byte, error) {
	if len(parts) == 1 {
		return parts[0], nil
	}
	switch detectFormat(parts[0]) {
	case "mp3":
		var out bytes.Buffer
		out.Write(parts[0])
		for _, p := range parts[1:] {
			out.Write(p[id3Size(p):])
		}
		return out.Bytes(), nil
	case "opus":
		return concatOgg(parts)
	case "wav":
		first, err := parseWAV(parts[0])
		if err != nil {
			return nil, err
		}
		var data bytes.Buffer
		for _, p := range parts {
			w, err := parseWAV(p)
			if err != nil {
				return nil, err
			}
			if w.audioFormat != first.audioFormat || w.channels != first.channels || w.sampleRate != first.sampleRate || w.bits != first.bits {
				return nil, fmt.Errorf("cannot join WAV chunks with different formats")
			}
			data.Write(w.data)
		}
		header := make([]byte, 44)
		copy(header[0:], "RIFF")
		binary.LittleEndian.PutUint32(header[4:], uint32(36+data.Len()))
		copy(header[8:], "WAVEfmt ")
		binary.LittleEndian.PutUint32(header[16:], 16)
		binary.LittleEndian.PutUint16(header[20:], first.audioFormat)
		binary.LittleEndian.PutUint16(header[22:], first.channels)
		binary.LittleEndian.PutUint32(header[24:], first.sampleRate)
		blockAlign := first.channels * first.bits / 8
		binary.LittleEndian.PutUint32(header[28:], first.sampleRate*uint32(blockAlign))
		binary.LittleEndian.PutUint16(header[32:], blockAlign)
		binary.LittleEndian.PutUint16(header[34:], first.bits)
		copy(header[36:], "data")
		binary.LittleEndian.PutUint32(header[40:], uint32(data.Len()))
		return append(header, data.Bytes()...), nil
	}
	return bytes.Join(parts, nil), nil
}

// inspectOgg walks every Ogg page. Joined chunks form a chained stream, so each
// stream's length (last granule position at 48 kHz, less the Opus pre-skip) is added.
func inspectOgg(b []byte) AudioInfo {
	info := AudioInfo{Format: "ogg"}
	preSkip := 0
	var samples int64
	for pos := 0; pos+27 <= len(b) && string(b[pos:pos+4]) == "OggS"; {
		headerType, granule := b[pos+5], int64(binary.LittleEndian.Uint64(b[pos+6:]))
		segments := int(b[pos+26])
		if pos+27+segments > len(b) {
			break
		}
		body := 0
		for _, l := range b[pos+27 : pos+27+segments] {
			body += int(l)
		}
		page := b[pos+27+segments:]
		if headerType&2 != 0 && len(page) >= 16 && string(page[:8]) == "OpusHead" {
			if info.Format != "opus" {
				info.Format, info.Channels = "opus", int(page[9])
				info.SampleRate = int(binary.LittleEndian.Uint32(page[12:]))
			}
			preSkip = int(binary.LittleEndian.Uint16(page[10:]))
		}
		if headerType&4 != 0 && granule > 0 {
			samples += granule - int64(preSkip)
		}
		pos += 27 + segments + body
	}
	if info.Format == "opus" {
		info.Duration = float64(samples) / 48000
	}
	return info
}

type oggPage struct {
	raw        []byte
	headerType byte
	granule    uint64
}

var oggCRCTable = func() (t [256]uint32) {
	for i := range t {
		r := uint32(i) << 24
		for range 8 {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04C11DB7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return
}()

func oggCRC(page []byte) uint32 {
	var crc uint32
	for _, b := range page {
		crc = crc<<8 ^ oggCRCTable[byte(crc>>24)^b]
	}
	return crc
}

func readOggPages(b []byte) ([]oggPage, error) {
	var pages []oggPage
	for pos := 0; pos < len(b); {
		if pos+27 > len(b) || string(b[pos:pos+4]) != "OggS" {
			return nil, fmt.Errorf("malformed Ogg stream")
		}
		segments := int(b[pos+26])
		if pos+27+segments > len(b) {
			return nil, fmt.Errorf("truncated Ogg page")
		}
		body := 0
		for _, l := range b[pos+27 : pos+27+segments] {
			body += int(l)
		}
		end := pos + 27 + segments + body
		if end > len(b) {
			return nil, fmt.Errorf("truncated Ogg page")
		}
		pages = append(pages, oggPage{raw: b[pos:end], headerType: b[pos+5], granule: binary.LittleEndian.Uint64(b[pos+6:])})
		pos = end
	}
	return pages, nil
}

// concatOgg joins separately encoded Opus streams into one logical stream: later
// streams lose their two header pages, and the remaining pages get the first
// stream's serial number, continuing page numbers and offset granule positions.
func concatOgg(parts [][]byte) ([]byte, error) {
	var out []byte
	var serial, seq uint32
	var offset uint64
	for i, part := range parts {
		pages, err := readOggPages(part)
		if err != nil {
			return nil, err
		}
		if len(pages) < 3 {
			return nil, fmt.Errorf("Opus chunk has no audio pages")
		}
		var lastGranule uint64
		for j, pg := range pages {
			if i == 0 && j == 0 {
				serial = binary.LittleEndian.Uint32(pg.raw[14:])
			}
			if i > 0 && j < 2 { // OpusHead and OpusTags
				continue
			}
			raw := append([]byte(nil), pg.raw...)
			raw[5] = pg.headerType &^ 4 // end-of-stream only on the very last page
			if i == len(parts)-1 && j == len(pages)-1 {
				raw[5] |= 4
			}
			if pg.granule != ^uint64(0) {
				lastGranule = pg.granule
				binary.LittleEndian.PutUint64(raw[6:], pg.granule+offset)
			}
			binary.LittleEndian.PutUint32(raw[14:], serial)
			binary.LittleEndian.PutUint32(raw[18:], seq)
			seq++
			binary.LittleEndian.PutUint32(raw[22:], 0)
			binary.LittleEndian.PutUint32(raw[22:], oggCRC(raw))
			out = append(out, raw...)
		}
		offset += lastGranule
	}
	return out, nil
}
