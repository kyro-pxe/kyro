// Copyright (C) 2026 Lukas Koch and Kyro contributors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This file is part of Kyro.
//
// Kyro is free software: you can redistribute it and/or modify it under the
// terms of the GNU General Public License as published by the Free Software
// Foundation, either version 3 of the License, or (at your option) any later
// version.
//
// Kyro is distributed in the hope that it will be useful, but WITHOUT ANY
// WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS
// FOR A PARTICULAR PURPOSE. See the GNU General Public License for more
// details.
//
// You should have received a copy of the GNU General Public License along
// with Kyro. If not, see <https://www.gnu.org/licenses/>.

package profiles

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxSize is the largest bundle or answer file that is accepted.
const MaxSize = 2 << 20

// unattendNS is the namespace of the root element of an answer file.
const unattendNS = "urn:schemas-microsoft-com:unattend"

// span is a run of character data in an answer file: a text node or a
// CDATA section, with the bytes it occupies in the file.
type span struct {
	start, end int
	cdata      bool
}

// token is one use of {{name}} or {{name:N}} in an answer file.
type token struct {
	start, end int
	name       string
	keep       int  // N of {{name:N}}, 0 if there is none
	cdata      bool // inside a CDATA section
}

// scanned is what one pass over an answer file finds.
type scanned struct {
	tokens []token
	// notes are things the person importing the file should know. None of
	// them stops the import.
	notes []string
	// wipes lists the reasons to believe that the file changes a disk.
	wipes []string
}

// The tokens Kyro replaces. The first three come from the booting machine,
// the last one from Kyro itself.
var tokenNames = map[string]bool{"mac": true, "serial": true, "uuid": true, "callback_url": true}

var (
	// Anything of the form {{...}} without braces inside.
	bracesRE = regexp.MustCompile(`\{\{[^{}]*\}\}`)
	tokenRE  = regexp.MustCompile(`^\{\{([a-z_]+)(?::([^{}]*))?\}\}$`)
)

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

// scan checks an answer file and finds its tokens. The checks are those of
// ADR 0002: UTF-8, well-formed, no DOCTYPE, no processing instructions,
// the right root element, and no malformed use of a known token.
func scan(data []byte) (*scanned, error) {
	if len(data) > MaxSize {
		return nil, fmt.Errorf("the answer file is larger than %d bytes", MaxSize)
	}
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		return nil, errors.New("the answer file is UTF-16; save it as UTF-8")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("the answer file is not valid UTF-8")
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("the answer file contains a NUL byte")
	}
	base := 0
	if bytes.HasPrefix(data, utf8BOM) {
		base = len(utf8BOM)
	}

	dec := xml.NewDecoder(bytes.NewReader(data[base:]))
	dec.Strict = true
	var (
		spans    []span
		depth    int
		sawRoot  bool
		rootDone bool
		elem     string // local name of the innermost open element
		res      = &scanned{}
		wipe     = map[string]bool{}
	)
	for {
		start := base + int(dec.InputOffset())
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("the answer file is not well-formed XML: %v", err)
		}
		end := base + int(dec.InputOffset())
		switch t := tok.(type) {
		case xml.Directive:
			// <!DOCTYPE ...> and with it entity declarations, external
			// entities and expansion bombs.
			return nil, errors.New("the answer file contains a DOCTYPE or another <!...> declaration, which is not allowed")
		case xml.ProcInst:
			if t.Target != "xml" || start != base {
				return nil, fmt.Errorf("the answer file contains the processing instruction <?%s ...?>, which is not allowed", clean(t.Target))
			}
		case xml.StartElement:
			if rootDone {
				return nil, errors.New("the answer file has more than one root element")
			}
			if !sawRoot {
				if t.Name.Local != "unattend" || t.Name.Space != unattendNS {
					return nil, fmt.Errorf("the root element is <%s>, not <unattend> in the namespace %s; this is not an answer file", clean(t.Name.Local), unattendNS)
				}
				sawRoot = true
			}
			depth++
			if depth > 64 {
				return nil, errors.New("the answer file nests elements more than 64 deep")
			}
			elem = t.Name.Local
			if elem == "DiskConfiguration" {
				wipe["it has a <DiskConfiguration> section"] = true
			}
		case xml.EndElement:
			depth--
			if depth == 0 {
				rootDone = true
			}
			elem = ""
		case xml.CharData:
			if depth == 0 {
				if strings.TrimSpace(string(t)) != "" {
					return nil, errors.New("the answer file has text outside its root element")
				}
				continue
			}
			spans = append(spans, span{start, end, bytes.HasPrefix(data[start:end], []byte("<![CDATA["))})
			text := strings.ToLower(string(t))
			if elem == "WillWipeDisk" && strings.TrimSpace(text) == "true" {
				wipe["it sets <WillWipeDisk> to true"] = true
			}
			for _, word := range []string{"diskpart", "clear-disk", "initialize-disk", "format-volume", "new-partition"} {
				if strings.Contains(text, word) {
					wipe["it mentions "+word] = true
				}
			}
		}
	}
	if !sawRoot {
		return nil, errors.New("the answer file has no root element")
	}
	for reason := range wipe {
		res.wipes = append(res.wipes, reason)
	}
	sort.Strings(res.wipes)

	// Tokens are matched on the bytes of the file, not on decoded text, so
	// that &#123;&#123;mac}} stays a way to write a literal one.
	for _, loc := range bracesRE.FindAllIndex(data, -1) {
		raw := string(data[loc[0]:loc[1]])
		m := tokenRE.FindStringSubmatch(raw)
		if m == nil || !tokenNames[m[1]] {
			res.notes = append(res.notes, fmt.Sprintf("%s is not a Kyro token and is left as it is", clean(raw)))
			continue
		}
		t := token{start: loc[0], end: loc[1], name: m[1]}
		if strings.Contains(raw, ":") {
			n, err := strconv.Atoi(m[2])
			if err != nil || n < 1 || n > 64 || m[1] == "callback_url" || strconv.Itoa(n) != m[2] {
				return nil, fmt.Errorf("%s: the number after the colon must be 1 to 64, and {{callback_url}} takes none", clean(raw))
			}
			t.keep = n
		}
		inside := false
		for _, s := range spans {
			if t.start >= s.start && t.end <= s.end {
				inside, t.cdata = true, s.cdata
				break
			}
		}
		if !inside {
			res.notes = append(res.notes, fmt.Sprintf("%s is in an attribute, a comment or a tag; tokens are only replaced in text and CDATA, so this one is left as it is", raw))
			continue
		}
		res.tokens = append(res.tokens, t)
	}
	return res, nil
}

// Values are what the tokens of an answer file are replaced with for one
// client. MAC, UUID and Serial come from the client and are untrusted;
// Render cleans them again, whatever the caller did.
type Values struct {
	MAC         string
	UUID        string
	Serial      string
	CallbackURL string
}

// ErrMissingValue means that an answer file uses a token for which the
// client sent no usable value.
var ErrMissingValue = errors.New("no value for a token")

var (
	macRE      = regexp.MustCompile(`^[0-9A-F]{12}$`)
	uuidRE     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	serialRE   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?$`)
	callbackRE = regexp.MustCompile(`^http://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9_.-]+)+$`)
)

// value returns the cleaned value of a token, or "" if there is none.
func (v Values) value(name string) string {
	switch name {
	case "mac":
		if s := strings.ToUpper(strings.NewReplacer(":", "", "-", "").Replace(v.MAC)); macRE.MatchString(s) {
			return s
		}
	case "uuid":
		if s := strings.ToLower(v.UUID); uuidRE.MatchString(s) {
			return s
		}
	case "serial":
		if serialRE.MatchString(v.Serial) {
			return v.Serial
		}
	case "callback_url":
		if callbackRE.MatchString(v.CallbackURL) {
			return v.CallbackURL
		}
	}
	return ""
}

// Render returns the answer file with its tokens replaced. Everything else
// is returned byte for byte.
//
// A value passes two layers before it is written. First it must have the
// form its token allows, which leaves letters, digits and hyphens (and, for
// the callback URL, what a plain http URL needs). Then it is escaped for
// the place it lands in. With those characters the escaping changes
// nothing; it is there in case the first layer is ever wrong.
func Render(data []byte, v Values) ([]byte, error) {
	res, err := scan(data)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	pos := 0
	for _, t := range res.tokens {
		val := v.value(t.name)
		if val == "" {
			return nil, fmt.Errorf("%w: the answer file uses {{%s}} and the client sent none that can be used", ErrMissingValue, t.name)
		}
		// {{name:N}} keeps the last N characters. The first ones are the
		// vendor part of a MAC address and the model part of most serial
		// numbers, the same across a fleet.
		if t.keep > 0 && len(val) > t.keep {
			val = strings.TrimLeft(val[len(val)-t.keep:], "-")
		}
		out.Write(data[pos:t.start])
		if t.cdata {
			out.WriteString(strings.ReplaceAll(val, "]]>", "]]]]><![CDATA[>"))
		} else {
			xml.EscapeText(&out, []byte(val))
		}
		pos = t.end
	}
	out.Write(data[pos:])
	return out.Bytes(), nil
}

// clean makes a string from an untrusted file safe to print: control
// characters are dropped and the length is limited.
func clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 500 {
			b.WriteString("...")
			break
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == utf8.RuneError {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
