package core

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"math/bits"
	"net/url"
	"strconv"
	"strings"
)

const hongguoPlayerLicenseID = 1611921764

const hongguoPlayerArgusSignKey = "fc78e0a9657a0c748ce51559903ccf03510e51d3cff232d71343e88a321c5304"
const hongguoPlayerArgusAESKey = "d31e3718288a1027baab59f146a09a9c"
const hongguoPlayerArgusAESIV = "ea180a0336ed352fcd24e4d50018ae54"

var hongguoPlayerArgusMagic = []byte{0xf2, 0xf7, 0xfc, 0xff, 0xf2, 0xf7, 0xfc, 0xff}
var hongguoPlayerArgusTail = []byte{0xea, 0xfb, 0x2c, 0xfe, 0x85, 0x68, 0x51, 0x91, 0x54}

type hongguoPlayerProto struct {
	buf []byte
}

func (p *hongguoPlayerProto) varint(field, value uint64) {
	p.buf = binary.AppendUvarint(p.buf, field<<3)
	p.buf = binary.AppendUvarint(p.buf, value)
}

func (p *hongguoPlayerProto) bytes(field uint64, value []byte) {
	p.buf = binary.AppendUvarint(p.buf, field<<3|2)
	p.buf = binary.AppendUvarint(p.buf, uint64(len(value)))
	p.buf = append(p.buf, value...)
}

func (p *hongguoPlayerProto) text(field uint64, value string) {
	p.bytes(field, []byte(value))
}

func (p *hongguoPlayerProto) nested(field uint64, value *hongguoPlayerProto) {
	p.bytes(field, value.buf)
}

func hongguoPlayerQueryValue(query, name string) string {
	for _, part := range strings.Split(query, "&") {
		key, value, _ := strings.Cut(part, "=")
		if key != name {
			continue
		}
		if decoded, err := url.PathUnescape(value); err == nil {
			return decoded
		}
		return value
	}
	return ""
}

func hongguoPlayerEncodeQuery(values [][2]string) string {
	const hexDigits = "0123456789ABCDEF"
	encode := func(value string) string {
		var out strings.Builder
		for i := 0; i < len(value); i++ {
			c := value[i]
			if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
				c == '-' || c == '_' || c == '.' || c == '~' || c == '*' {
				out.WriteByte(c)
				continue
			}
			out.WriteByte('%')
			out.WriteByte(hexDigits[c>>4])
			out.WriteByte(hexDigits[c&15])
		}
		return out.String()
	}
	parts := make([]string, 0, len(values))
	for _, pair := range values {
		parts = append(parts, encode(pair[0])+"="+encode(pair[1]))
	}
	return strings.Join(parts, "&")
}

func hongguoPlayerLadon(timestamp uint32, licenseID, aid string, prefix []byte) string {
	seed := md5.Sum(append(append([]byte(nil), prefix...), aid...))
	digest := []byte(hex.EncodeToString(seed[:]))
	left := binary.LittleEndian.Uint64(digest)
	right := binary.LittleEndian.Uint64(digest[8:])
	queue := []uint64{binary.LittleEndian.Uint64(digest[16:]), binary.LittleEndian.Uint64(digest[24:])}
	var keys [34]uint64
	for i := range keys {
		keys[i] = left
		mixed := (bits.RotateLeft64(right, -8) + left) ^ uint64(i)
		queue = append(queue, mixed)
		left = mixed ^ bits.RotateLeft64(left, 3)
		right, queue = queue[0], queue[1:]
	}
	text := []byte(strconv.FormatUint(uint64(timestamp), 10) + "-" + licenseID + "-" + aid)
	padding := 16 - len(text)%16
	data := make([]byte, len(text)+padding)
	copy(data, text)
	for i := len(text); i < len(data); i++ {
		data[i] = byte(padding)
	}
	for offset := 0; offset < len(data); offset += 16 {
		l, r := binary.LittleEndian.Uint64(data[offset:]), binary.LittleEndian.Uint64(data[offset+8:])
		for _, key := range keys {
			r = key ^ (l + bits.RotateLeft64(r, -8))
			l = r ^ bits.RotateLeft64(l, 3)
		}
		binary.LittleEndian.PutUint64(data[offset:], l)
		binary.LittleEndian.PutUint64(data[offset+8:], r)
	}
	return base64.StdEncoding.EncodeToString(append(append([]byte(nil), prefix...), data...))
}

func hongguoPlayerSimonEncrypt(input, signKey []byte) []byte {
	const z uint64 = 0x3DC94C3A046D678B
	var rounds [72]uint64
	for i := 0; i < 4; i++ {
		rounds[i] = binary.LittleEndian.Uint64(signKey[i*8:])
	}
	for i := 4; i < len(rounds); i++ {
		mixed := bits.RotateLeft64(rounds[i-1], -3) ^ rounds[i-3]
		mixed ^= bits.RotateLeft64(mixed, -1)
		rounds[i] = ^rounds[i-4] ^ mixed ^ (z>>uint((i-4)%62))&1 ^ 3
	}
	out := append([]byte(nil), input...)
	for offset := 0; offset+16 <= len(out); offset += 16 {
		left, right := binary.LittleEndian.Uint64(out[offset:]), binary.LittleEndian.Uint64(out[offset+8:])
		for _, round := range rounds {
			left, right = right, left^(bits.RotateLeft64(right, 1)&bits.RotateLeft64(right, 8))^bits.RotateLeft64(right, 2)^round
		}
		binary.LittleEndian.PutUint64(out[offset:], left)
		binary.LittleEndian.PutUint64(out[offset+8:], right)
	}
	return out
}

func hongguoPlayerArgus(query, stub string, timestamp uint32, aid, deviceID, installID string, randField uint32) string {
	bodyInput := make([]byte, 16)
	if stub != "" {
		if decoded, err := hex.DecodeString(stub); err == nil {
			bodyInput = decoded
		}
	}
	bodyHash := hongguoSM3(bodyInput)
	queryHash := hongguoSM3([]byte(query))
	if aid == "" || aid == "0" {
		aid = "1967"
	}
	licenseID := strconv.Itoa(hongguoPlayerLicenseID)
	p := &hongguoPlayerProto{}
	p.varint(1, 1077940818)
	p.varint(2, 2)
	p.varint(3, uint64(randField))
	p.text(4, aid)
	p.text(5, deviceID)
	p.text(6, licenseID)
	p.text(7, hongguoPlayerQueryValue(query, "version_name"))
	p.text(8, "v04.04.05-ov-android")
	p.varint(9, 134744640)
	p.bytes(10, make([]byte, 8))
	p.varint(11, 0)
	p.varint(12, (uint64(timestamp)<<1)&0xffffffff)
	p.bytes(13, bodyHash[:6])
	p.bytes(14, queryHash[:6])
	sub15 := &hongguoPlayerProto{}
	sub15.varint(1, 1)
	sub15.varint(2, 1)
	sub15.varint(3, 1)
	sub15.varint(7, 0xC792ECCC)
	p.nested(15, sub15)
	p.text(16, installID)
	p.text(20, "none")
	p.varint(21, 738)
	sub23 := &hongguoPlayerProto{}
	sub23.text(1, "NX551J")
	sub23.varint(2, 8196)
	sub23.varint(4, 0x80E0D800)
	p.nested(23, sub23)
	p.varint(25, 2)

	signKey, _ := hex.DecodeString(hongguoPlayerArgusSignKey)
	encrypted := hongguoPlayerSimonEncrypt(hongguoCommentPad(p.buf), signKey)
	combined := make([]byte, 0, len(hongguoPlayerArgusMagic)+len(encrypted)+len(hongguoPlayerArgusTail))
	combined = append(combined, hongguoPlayerArgusMagic...)
	combined = append(combined, encrypted...)
	combined = append(combined, hongguoPlayerArgusTail...)
	buf := make([]byte, len(combined))
	copy(buf, hongguoPlayerArgusMagic)
	for i := 8; i < len(combined); i++ {
		buf[i] = combined[i] ^ hongguoPlayerArgusMagic[i%8]
	}
	for left, right := 0, len(buf)-1; left < right; left, right = left+1, right-1 {
		buf[left], buf[right] = buf[right], buf[left]
	}
	container := hongguoCommentPad(append(buf, 0x61, 0x6f))
	key, _ := hex.DecodeString(hongguoPlayerArgusAESKey)
	iv, _ := hex.DecodeString(hongguoPlayerArgusAESIV)
	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(container, container)
	return base64.StdEncoding.EncodeToString(append([]byte{0xf2, 0x81}, container...))
}

func hongguoPlayerSignHeaders(query string, body []byte, timestamp uint32) map[string]string {
	ladonRandom := make([]byte, 4)
	randField := make([]byte, 4)
	if _, err := rand.Read(ladonRandom); err != nil {
		return nil
	}
	if _, err := rand.Read(randField); err != nil {
		return nil
	}
	aid := hongguoPlayerQueryValue(query, "aid")
	if aid == "" {
		aid = "1967"
	}
	stub := ""
	if len(body) > 0 {
		sum := md5.Sum(body)
		stub = hex.EncodeToString(sum[:])
	}
	headers := map[string]string{
		"X-SS-Req-Ticket": strconv.FormatInt(int64(timestamp)*1000, 10),
		"X-Khronos":       strconv.FormatUint(uint64(timestamp), 10),
		"X-Ladon":         hongguoPlayerLadon(timestamp, strconv.Itoa(hongguoPlayerLicenseID), aid, ladonRandom),
		"X-Argus": hongguoPlayerArgus(query, stub, timestamp, aid,
			hongguoPlayerQueryValue(query, "device_id"), hongguoPlayerQueryValue(query, "iid"),
			binary.LittleEndian.Uint32(randField)),
	}
	if stub != "" {
		headers["X-SS-Stub"] = strings.ToUpper(stub)
	}
	return headers
}
