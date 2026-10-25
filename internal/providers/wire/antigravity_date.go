package wire

import (
	"math"
	"strings"
)

// jsDateParse is Date.parse(text) as V8 reads it (src/date/dateparser-inl.h): the
// ES5 date-time string, then the legacy parser that follows it, which reads "0.5"
// as May of 2000, "- 5" as May of 2001 and a two-digit year 50 to 99 as 19xx. The
// pin calls it on a Retry-After that parseInt did not read as a positive number
// (executors/antigravity.js:364).
//
// A date with no zone is read in the host's zone by V8. It is read in UTC here, so
// the two differ only for a zone-less date within the host's offset (at most 14
// hours) of the moment it is compared with; every other answer (invalid, past, far
// future) is the same in any zone.
func jsDateParse(text string) (float64, bool) {
	if end := strings.IndexByte(text, 0); end >= 0 {
		text = text[:end]
	}
	scanner := &dateScanner{input: []rune(text)}
	scanner.advance()
	scanner.next = scanner.scan()
	var day dayComposer
	time := timeComposer{hourOffset: dateNone}
	zone := zoneComposer{sign: dateNone, hour: dateNone, minute: dateNone}
	token, ok := parseES5(scanner, &day, &time, &zone)
	if !ok {
		return 0, false
	}
	hasNumber := day.index > 0
	for ; token.kind != tokenEnd; token = scanner.take() {
		switch {
		case token.kind == tokenNumber:
			hasNumber = true
			n := token.number
			switch {
			case scanner.skipSymbol(':'):
				if scanner.skipSymbol(':') {
					if time.index != 0 {
						return 0, false
					}
					time.add(n)
					time.add(0)
				} else {
					if !time.add(n) {
						return 0, false
					}
					if scanner.next.isSymbol('.') {
						scanner.take()
					}
				}
			case scanner.skipSymbol('.') && time.expecting(n):
				time.add(n)
				if scanner.next.kind != tokenNumber {
					return 0, false
				}
				ms := readMilliseconds(scanner.take())
				time.addFinal(ms)
			case zone.expecting(n):
				zone.minute = n
			case time.expecting(n):
				time.addFinal(n)
				after := scanner.next
				if after.kind != tokenEnd && after.kind != tokenWhite && !after.isZ() && !after.isSign() {
					return 0, false
				}
			default:
				if !day.add(n) {
					return 0, false
				}
				scanner.skipSymbol('-')
			}
		case token.kind == tokenKeyword:
			switch {
			case token.keyword == keywordAMPM && time.index != 0:
				time.hourOffset = token.value
			case token.keyword == keywordMonth:
				day.namedMonth = token.value
				scanner.skipSymbol('-')
			case token.keyword == keywordZone && hasNumber:
				zone.set(token.value)
			default:
				if hasNumber || scanner.next.kind == tokenNumber {
					return 0, false
				}
			}
		case token.isSign() && (zone.isUTC() || time.index != 0):
			zone.sign = 1
			if token.symbol == '-' {
				zone.sign = -1
			}
			n, length := 0, 0
			if scanner.next.kind == tokenNumber {
				following := scanner.take()
				n, length = following.number, following.length
			}
			hasNumber = true
			switch {
			case scanner.next.isSymbol(':'):
				zone.hour, zone.minute = n, dateNone
			case length == 1 || length == 2:
				zone.hour, zone.minute = n, 0
			case length == 3 || length == 4:
				zone.hour, zone.minute = n/100, n%100
			default:
				return 0, false
			}
		case (token.isSign() || token.isSymbol(')')) && hasNumber:
			return 0, false
		}
	}
	year, month, dayOfMonth, ok := day.write()
	if !ok {
		return 0, false
	}
	hour, minute, second, millisecond, ok := time.write()
	if !ok {
		return 0, false
	}
	offset, zoned, ok := zone.write()
	if !ok {
		return 0, false
	}
	date := makeDay(year, month-1, dayOfMonth)*86400000 + float64(((hour*60+minute)*60+second)*1000+millisecond)
	if zoned {
		date -= float64(offset) * 1000
	}
	if math.IsNaN(date) || math.Abs(date) > 8.64e15 {
		return 0, false
	}
	return date, true
}

// makeDay is the days from 1970-01-01 to the day of a month that may be past 12.
func makeDay(year, month, day int) float64 {
	y := int64(year) + int64(math.Floor(float64(month)/12))
	m := int64(month) - 12*int64(math.Floor(float64(month)/12))
	if m < 2 {
		y--
	}
	era := y / 400
	if y < 0 && y%400 != 0 {
		era--
	}
	yoe := y - era*400
	shifted := m - 2
	if shifted < 0 {
		shifted += 12
	}
	doy := (153*shifted + 2) / 5
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return float64(era*146097 + doe - 719468 + int64(day) - 1)
}

const dateNone = -1 << 30

type tokenKind int

const (
	tokenEnd tokenKind = iota
	tokenNumber
	tokenSymbol
	tokenKeyword
	tokenWhite
	tokenUnknown
)

type keywordKind int

const (
	keywordInvalid keywordKind = iota
	keywordMonth
	keywordZone
	keywordSeparator
	keywordAMPM
)

type dateToken struct {
	kind    tokenKind
	number  int
	length  int
	symbol  rune
	keyword keywordKind
	value   int
}

func (t dateToken) isSymbol(r rune) bool { return t.kind == tokenSymbol && t.symbol == r }
func (t dateToken) isSign() bool         { return t.isSymbol('+') || t.isSymbol('-') }
func (t dateToken) isZ() bool {
	return t.kind == tokenKeyword && t.keyword == keywordZone && t.length == 1
}
func (t dateToken) fixed(length int) bool {
	return t.kind == tokenNumber && t.length == length
}

type keywordEntry struct {
	word  string
	kind  keywordKind
	value int
}

// The words V8 knows: a month by its first three letters, the rest in full.
var dateKeywords = []keywordEntry{
	{"jan", keywordMonth, 1}, {"feb", keywordMonth, 2}, {"mar", keywordMonth, 3}, {"apr", keywordMonth, 4},
	{"may", keywordMonth, 5}, {"jun", keywordMonth, 6}, {"jul", keywordMonth, 7}, {"aug", keywordMonth, 8},
	{"sep", keywordMonth, 9}, {"oct", keywordMonth, 10}, {"nov", keywordMonth, 11}, {"dec", keywordMonth, 12},
	{"am", keywordAMPM, 0}, {"pm", keywordAMPM, 12}, {"ut", keywordZone, 0}, {"utc", keywordZone, 0},
	{"z", keywordZone, 0}, {"gmt", keywordZone, 0}, {"cdt", keywordZone, -5}, {"cst", keywordZone, -6},
	{"edt", keywordZone, -4}, {"est", keywordZone, -5}, {"mdt", keywordZone, -6}, {"mst", keywordZone, -7},
	{"pdt", keywordZone, -7}, {"pst", keywordZone, -8}, {"t", keywordSeparator, 0},
}

type dateScanner struct {
	input []rune
	index int
	ch    rune
	next  dateToken
}

func (s *dateScanner) advance() {
	if s.index < len(s.input) {
		s.ch = s.input[s.index]
	} else {
		s.ch = 0
	}
	s.index++
}

func (s *dateScanner) take() dateToken {
	token := s.next
	s.next = s.scan()
	return token
}

func (s *dateScanner) skipSymbol(r rune) bool {
	if s.next.isSymbol(r) {
		s.next = s.scan()
		return true
	}
	return false
}

func (s *dateScanner) scan() dateToken {
	if s.ch == 0 {
		return dateToken{kind: tokenEnd}
	}
	if s.ch >= '0' && s.ch <= '9' {
		start := s.index
		for s.ch == '0' {
			s.advance()
		}
		n, significant := 0, 0
		for s.ch >= '0' && s.ch <= '9' {
			if significant < 9 {
				n = n*10 + int(s.ch-'0')
			}
			significant++
			s.advance()
		}
		return dateToken{kind: tokenNumber, number: n, length: s.index - start}
	}
	switch s.ch {
	case ':', '-', '+', '.', ')':
		symbol := s.ch
		s.advance()
		return dateToken{kind: tokenSymbol, symbol: symbol}
	}
	if s.ch >= 'A' && !strings.ContainsRune(jsSpace, s.ch) {
		var prefix [3]rune
		length := 0
		for ; s.ch >= 'A' && !strings.ContainsRune(jsSpace, s.ch); s.advance() {
			if length < 3 {
				prefix[length] = s.ch
				if s.ch >= 'A' && s.ch <= 'Z' {
					prefix[length] = s.ch | 0x20
				}
			}
			length++
		}
		token := dateToken{kind: tokenKeyword, length: length}
		for _, entry := range dateKeywords {
			word := []rune(entry.word)
			match := true
			for i := 0; i < 3; i++ {
				var want rune
				if i < len(word) {
					want = word[i]
				}
				if prefix[i] != want {
					match = false
					break
				}
			}
			if match && (length <= 3 || entry.kind == keywordMonth) {
				token.keyword, token.value = entry.kind, entry.value
				break
			}
		}
		return token
	}
	if strings.ContainsRune(jsSpace, s.ch) {
		s.advance()
		return dateToken{kind: tokenWhite}
	}
	if s.ch == '(' {
		balance := 0
		for {
			if s.ch == ')' {
				balance--
			} else if s.ch == '(' {
				balance++
			}
			s.advance()
			if balance <= 0 || s.ch == 0 {
				break
			}
		}
		return dateToken{kind: tokenUnknown}
	}
	s.advance()
	return dateToken{kind: tokenUnknown}
}

func between(x, low, high int) bool { return x >= low && x <= high }

type dayComposer struct {
	comp       [3]int
	index      int
	namedMonth int
	iso        bool
}

func (d *dayComposer) add(n int) bool {
	if d.index >= len(d.comp) {
		return false
	}
	d.comp[d.index] = n
	d.index++
	return true
}

func (d *dayComposer) write() (year, month, day int, ok bool) {
	if d.index < 1 {
		return 0, 0, 0, false
	}
	for d.index < len(d.comp) {
		d.comp[d.index] = 1
		d.index++
	}
	month, day = dateNone, dateNone
	if d.namedMonth == 0 {
		if d.iso || !between(d.comp[0], 1, 31) {
			year, month, day = d.comp[0], d.comp[1], d.comp[2]
		} else {
			month, day, year = d.comp[0], d.comp[1], d.comp[2]
		}
	} else {
		month = d.namedMonth
		if !between(d.comp[0], 1, 31) {
			year, day = d.comp[0], d.comp[1]
		} else {
			day, year = d.comp[0], d.comp[1]
		}
	}
	if !d.iso {
		if between(year, 0, 49) {
			year += 2000
		} else if between(year, 50, 99) {
			year += 1900
		}
	}
	if !between(month, 1, 12) || !between(day, 1, 31) {
		return 0, 0, 0, false
	}
	return year, month, day, true
}

type timeComposer struct {
	comp       [4]int
	index      int
	hourOffset int
}

func (t *timeComposer) expecting(n int) bool {
	return (t.index == 1 && between(n, 0, 59)) || (t.index == 2 && between(n, 0, 59)) || (t.index == 3 && between(n, 0, 999))
}

func (t *timeComposer) add(n int) bool {
	if t.index >= len(t.comp) {
		return false
	}
	t.comp[t.index] = n
	t.index++
	return true
}

func (t *timeComposer) addFinal(n int) bool {
	if !t.add(n) {
		return false
	}
	for t.index < len(t.comp) {
		t.comp[t.index] = 0
		t.index++
	}
	return true
}

func (t *timeComposer) write() (hour, minute, second, millisecond int, ok bool) {
	for t.index < len(t.comp) {
		t.comp[t.index] = 0
		t.index++
	}
	hour, minute, second, millisecond = t.comp[0], t.comp[1], t.comp[2], t.comp[3]
	if t.hourOffset != dateNone {
		if !between(hour, 0, 12) {
			return 0, 0, 0, 0, false
		}
		hour = hour%12 + t.hourOffset
	}
	if !between(hour, 0, 23) || !between(minute, 0, 59) || !between(second, 0, 59) || !between(millisecond, 0, 999) {
		if hour != 24 || minute != 0 || second != 0 || millisecond != 0 {
			return 0, 0, 0, 0, false
		}
	}
	return hour, minute, second, millisecond, true
}

type zoneComposer struct {
	sign, hour, minute int
}

func (z *zoneComposer) set(hours int) {
	z.sign = 1
	if hours < 0 {
		z.sign = -1
		hours = -hours
	}
	z.hour, z.minute = hours, 0
}

func (z *zoneComposer) expecting(n int) bool {
	return z.hour != dateNone && z.minute == dateNone && between(n, 0, 59)
}

func (z *zoneComposer) isUTC() bool { return z.hour == 0 && z.minute == 0 }

func (z *zoneComposer) write() (offset int, zoned, ok bool) {
	if z.sign == dateNone {
		return 0, false, true
	}
	hour, minute := max(z.hour, 0), max(z.minute, 0)
	// V8 does this sum in 32-bit unsigned arithmetic, so a large hour wraps before the bound.
	total := uint32(hour)*3600 + uint32(minute)*60
	if total > 1<<30-1 {
		return 0, false, false
	}
	offset = int(total)
	if z.sign < 0 {
		offset = -offset
	}
	return offset, true, true
}

// readMilliseconds reads the three most significant digits of a numeral, counting
// its leading zeros.
func readMilliseconds(token dateToken) int {
	number, length := token.number, token.length
	switch {
	case length == 1:
		number *= 100
	case length == 2:
		number *= 10
	case length > 3:
		length = min(length, 9)
		factor := 1
		for ; length > 3; length-- {
			factor *= 10
		}
		number /= factor
	}
	return number
}

// parseES5 is ParseES5DateTime: the ISO form, which hands whatever it cannot read
// on to the legacy loop. ok is false for a string that is invalid outright.
func parseES5(s *dateScanner, day *dayComposer, time *timeComposer, zone *zoneComposer) (dateToken, bool) {
	if s.next.isSign() {
		sign := s.take()
		if !s.next.fixed(6) {
			return sign, true
		}
		year := s.take().number
		if sign.symbol == '-' && year == 0 {
			return sign, true
		}
		if sign.symbol == '-' {
			year = -year
		}
		day.add(year)
	} else if s.next.fixed(4) {
		day.add(s.take().number)
	} else {
		return s.take(), true
	}
	if s.skipSymbol('-') {
		if !s.next.fixed(2) || !between(s.next.number, 1, 12) {
			return s.take(), true
		}
		day.add(s.take().number)
		if s.skipSymbol('-') {
			if !s.next.fixed(2) || !between(s.next.number, 1, 31) {
				return s.take(), true
			}
			day.add(s.take().number)
		}
	}
	if !(s.next.kind == tokenKeyword && s.next.keyword == keywordSeparator) {
		if s.next.kind != tokenEnd {
			return s.take(), true
		}
	} else {
		s.take()
		if !s.next.fixed(2) || !between(s.next.number, 0, 24) {
			return dateToken{}, false
		}
		hour24 := s.next.number == 24
		time.add(s.take().number)
		if !s.skipSymbol(':') {
			return dateToken{}, false
		}
		if !s.next.fixed(2) || !between(s.next.number, 0, 59) || (hour24 && s.next.number > 0) {
			return dateToken{}, false
		}
		time.add(s.take().number)
		if s.skipSymbol(':') {
			if !s.next.fixed(2) || !between(s.next.number, 0, 59) || (hour24 && s.next.number > 0) {
				return dateToken{}, false
			}
			time.add(s.take().number)
			if s.skipSymbol('.') {
				if s.next.kind != tokenNumber || (hour24 && s.next.number > 0) {
					return dateToken{}, false
				}
				time.add(readMilliseconds(s.take()))
			}
		}
		switch {
		case s.next.isZ():
			s.take()
			zone.set(0)
		case s.next.isSign():
			zone.sign = 1
			if s.take().symbol == '-' {
				zone.sign = -1
			}
			if s.next.fixed(4) {
				hourMinute := s.take().number
				if !between(hourMinute/100, 0, 23) || !between(hourMinute%100, 0, 59) {
					return dateToken{}, false
				}
				zone.hour, zone.minute = hourMinute/100, hourMinute%100
			} else {
				if !s.next.fixed(2) || !between(s.next.number, 0, 23) {
					return dateToken{}, false
				}
				zone.hour = s.take().number
				if !s.skipSymbol(':') || !s.next.fixed(2) || !between(s.next.number, 0, 59) {
					return dateToken{}, false
				}
				zone.minute = s.take().number
			}
		}
		if s.next.kind != tokenEnd {
			return dateToken{}, false
		}
	}
	if zone.hour == dateNone && time.index == 0 {
		zone.set(0)
	}
	day.iso = true
	return dateToken{kind: tokenEnd}, true
}
